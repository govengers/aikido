// Command bench runs coding tasks against agent harnesses and records how
// each run went. See benchmark/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Results and scratch files are only for the benchmark user.
const (
	directoryPermissions = 0o750
	filePermissions      = 0o600
)

// harnessRunner runs one task in run.WorkDirectory and fills observed with
// what it can observe.
type harnessRunner func(ctx context.Context, run *job, observed *metrics) error

var harnesses = map[string]harnessRunner{
	"aikido":      runAikido(false),
	"aikido-bash": runAikido(true),
	"opencode":    runOpencode,
}

type task struct {
	Name      string
	Directory string
	Category  string
	Prompt    string
}

type job struct {
	Harness        string
	Model          string
	Task           task
	Repetition     int
	Root           string // per-run scratch directory, outside the work directory
	WorkDirectory  string // the fixture copy the agent works in
	LogDirectory   string // transcript and logs, kept in the results directory
	Timeout        time.Duration
	APIKey         string
	OpencodeBinary string
}

func (j *job) id() string {
	return fmt.Sprintf("%s__%s__%s__%d", j.Harness, strings.ReplaceAll(j.Model, "/", "_"), j.Task.Name, j.Repetition)
}

type metrics struct {
	LLMCalls         int
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	CostUSD          float64
	ToolCalls        map[string]int
	ToolErrors       int
	EndReason        string
}

func (m *metrics) tool(name string, failed bool) {
	if m.ToolCalls == nil {
		m.ToolCalls = map[string]int{}
	}
	m.ToolCalls[name]++
	if failed {
		m.ToolErrors++
	}
}

// Result is one line of results.jsonl.
type Result struct {
	Harness          string         `json:"harness"`
	Model            string         `json:"model"`
	Task             string         `json:"task"`
	Category         string         `json:"category"`
	Repetition       int            `json:"rep"`
	Pass             bool           `json:"pass"`
	WallSeconds      float64        `json:"wall_seconds"`
	LLMCalls         int            `json:"llm_calls"`
	InputTokens      int            `json:"input_tokens"`
	OutputTokens     int            `json:"output_tokens"`
	CacheReadTokens  int            `json:"cache_read_tokens"`
	CacheWriteTokens int            `json:"cache_write_tokens"`
	CostUSD          float64        `json:"cost_usd"`
	ToolCalls        map[string]int `json:"tool_calls"`
	ToolErrors       int            `json:"tool_errors"`
	FilesChanged     int            `json:"files_changed"`
	BytesChanged     int            `json:"bytes_changed"`
	EndReason        string         `json:"end_reason,omitempty"`
	Error            string         `json:"error,omitempty"`
	Check            string         `json:"check,omitempty"` // tail of the check output on failure
}

func main() {
	var (
		harnessFlag      = flag.String("harness", "aikido,aikido-bash,opencode", "comma-separated harnesses")
		modelFlag        = flag.String("model", "anthropic/claude-haiku-5.5", "comma-separated OpenRouter model ids")
		taskGlob         = flag.String("task", "*", "comma-separated globs over task names")
		repetitions      = flag.Int("reps", 1, "repetitions per (harness, model, task)")
		parallel         = flag.Int("parallel", 4, "concurrent runs")
		timeout          = flag.Duration("timeout", 10*time.Minute, "wall-clock limit per run")
		tasksDirectory   = flag.String("tasks", "tasks", "task directory")
		resultsDirectory = flag.String("results", "results", "results directory")
		runID            = flag.String("run-id", time.Now().Format("20060102-150405"), "name of the results subdirectory")
		opencodeBinary   = flag.String("opencode", "opencode", "opencode binary")
		keep             = flag.Bool("keep", false, "keep the work directories (paths are logged)")
		validate         = flag.Bool("validate", false, "check that every check.sh fails on the fixture and passes on solve.sh; no LLM calls")
		summarize        = flag.String("summarize", "", "rewrite summary.md of an existing results directory and exit")
	)
	flag.Parse()
	ctx := context.Background()

	if *summarize != "" {
		exitOn(writeSummary(*summarize))
		return
	}
	tasks, err := loadTasks(*tasksDirectory, *taskGlob)
	exitOn(err)
	if *validate {
		if !validateTasks(ctx, tasks) {
			os.Exit(1)
		}
		return
	}

	harnessNames := splitList(*harnessFlag)
	for _, name := range harnessNames {
		if harnesses[name] == nil {
			exitOn(fmt.Errorf("unknown harness %q", name))
		}
	}
	apiKey, err := openRouterKey()
	exitOn(err)
	if strings.Contains(*harnessFlag, "opencode") {
		binary, err := exec.LookPath(*opencodeBinary)
		exitOn(err)
		*opencodeBinary = binary
	}

	runDirectory := filepath.Join(*resultsDirectory, *runID)
	exitOn(os.MkdirAll(filepath.Join(runDirectory, "runs"), directoryPermissions))
	exitOn(writeRunInfo(ctx, runDirectory, *opencodeBinary))

	jobs := planJobs(tasks, harnessNames, splitList(*modelFlag), *repetitions, job{Timeout: *timeout, APIKey: apiKey, OpencodeBinary: *opencodeBinary})
	exitOn(runJobs(ctx, runDirectory, jobs, *parallel, *keep))
	exitOn(writeSummary(runDirectory))
	fmt.Fprintf(os.Stderr, "results: %s\n", filepath.Join(runDirectory, "summary.md"))
}

func openRouterKey() (string, error) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("CLAUDE_OPENROUTER_API_KEY")
	}
	if apiKey == "" {
		return "", errors.New("set OPENROUTER_API_KEY (or CLAUDE_OPENROUTER_API_KEY)")
	}
	return apiKey, nil
}

// planJobs builds one job per repetition, task, model and harness, in that
// nesting order; template carries the fields every job shares.
func planJobs(tasks []task, harnessNames, models []string, repetitions int, template job) []*job {
	var jobs []*job
	for repetition := 1; repetition <= repetitions; repetition++ {
		for _, benchmarkTask := range tasks {
			for _, model := range models {
				for _, harness := range harnessNames {
					planned := template
					planned.Harness, planned.Model, planned.Task, planned.Repetition = harness, model, benchmarkTask, repetition
					jobs = append(jobs, &planned)
				}
			}
		}
	}
	return jobs
}

// runJobs executes the jobs with at most parallel at a time, appends each
// result to results.jsonl and logs one progress line per finished job.
func runJobs(ctx context.Context, runDirectory string, jobs []*job, parallel int, keep bool) error {
	//nolint:gosec // G304: results.jsonl in the run directory main just created
	resultsFile, err := os.OpenFile(filepath.Join(runDirectory, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, filePermissions)
	if err != nil {
		return err
	}
	defer resultsFile.Close()

	var (
		mutex     sync.Mutex
		done      int
		waitGroup sync.WaitGroup
		semaphore = make(chan struct{}, max(1, parallel))
	)
	for _, run := range jobs {
		waitGroup.Add(1)
		semaphore <- struct{}{}
		go func() {
			defer func() { <-semaphore; waitGroup.Done() }()
			run.LogDirectory = filepath.Join(runDirectory, "runs", run.id())
			result := execute(ctx, run, keep)
			line, _ := json.Marshal(result)
			mutex.Lock()
			defer mutex.Unlock()
			done++
			_, _ = resultsFile.Write(append(line, '\n'))
			status := "FAIL"
			if result.Pass {
				status = "PASS"
			}
			fmt.Fprintf(os.Stderr, "[%d/%d] %-4s %-11s %-28s %-20s rep%d %6.1fs $%.4f %s\n",
				done, len(jobs), status, run.Harness, run.Model, run.Task.Name, run.Repetition, result.WallSeconds, result.CostUSD, result.Error)
		}()
	}
	waitGroup.Wait()
	return nil
}

// execute runs one job end to end and never panics the whole run.
func execute(ctx context.Context, run *job, keep bool) (result Result) {
	result = Result{Harness: run.Harness, Model: run.Model, Task: run.Task.Name, Category: run.Task.Category, Repetition: run.Repetition}
	fail := func(err error) Result { result.Error = err.Error(); return result }
	if err := os.MkdirAll(run.LogDirectory, directoryPermissions); err != nil {
		return fail(err)
	}
	root, err := os.MkdirTemp("", "aikido-bench-")
	if err != nil {
		return fail(err)
	}
	if keep {
		_ = os.WriteFile(filepath.Join(run.LogDirectory, "workdir.txt"), []byte(root+"\n"), filePermissions)
	} else {
		defer os.RemoveAll(root)
	}
	run.Root = root
	run.WorkDirectory = filepath.Join(root, "work")
	fixture := filepath.Join(run.Task.Directory, "fixture")
	if err := os.CopyFS(run.WorkDirectory, os.DirFS(fixture)); err != nil {
		return fail(err)
	}

	runContext, cancel := context.WithTimeout(ctx, run.Timeout)
	defer cancel()
	var observed metrics
	start := time.Now()
	runError := harnesses[run.Harness](runContext, run, &observed)
	result.WallSeconds = time.Since(start).Seconds()
	if runError == nil && runContext.Err() != nil {
		runError = fmt.Errorf("timeout after %s", run.Timeout)
	}
	if path := modifiedSince(run.Task.Directory, start); path != "" {
		// The agent escaped its work directory (absolute paths); fail loudly.
		runError = fmt.Errorf("task directory modified during the run: %s (restore it with git)", path)
	}
	if runError != nil {
		result.Error = runError.Error()
	}
	result.LLMCalls, result.InputTokens, result.OutputTokens = observed.LLMCalls, observed.InputTokens, observed.OutputTokens
	result.CacheReadTokens, result.CacheWriteTokens, result.CostUSD = observed.CacheReadTokens, observed.CacheWriteTokens, observed.CostUSD
	result.ToolCalls, result.ToolErrors, result.EndReason = observed.ToolCalls, observed.ToolErrors, observed.EndReason

	// Measure the agent's edits before check.sh adds hidden tests.
	result.FilesChanged, result.BytesChanged, err = diffTrees(fixture, run.WorkDirectory)
	if err != nil && result.Error == "" {
		result.Error = err.Error()
	}
	// The check gets its own timeout, not what is left of the run's.
	pass, output := runScript(ctx, run.Task, "check.sh", run.WorkDirectory)
	_ = os.WriteFile(filepath.Join(run.LogDirectory, "check.log"), []byte(output), filePermissions)
	result.Pass = pass
	if !pass {
		result.Check = tail(output, 600)
	}
	return result
}

func loadTasks(directory, glob string) ([]task, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(absolute)
	if err != nil {
		return nil, err
	}
	var tasks []task
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		matched, err := matchesAny(entry.Name(), splitList(glob))
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		taskDirectory := filepath.Join(absolute, entry.Name())
		taskFiles := os.DirFS(taskDirectory)
		prompt, err := fs.ReadFile(taskFiles, "task.md")
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", entry.Name(), err)
		}
		category, err := fs.ReadFile(taskFiles, "category")
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", entry.Name(), err)
		}
		tasks = append(tasks, task{Name: entry.Name(), Directory: taskDirectory, Category: strings.TrimSpace(string(category)), Prompt: string(prompt)})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no task in %s matches %q", directory, glob)
	}
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].Name < tasks[right].Name })
	return tasks, nil
}

// matchesAny reports whether name matches one of the globs. Every glob is
// checked, so a malformed one is an error even after an earlier match.
func matchesAny(name string, globs []string) (bool, error) {
	matched := false
	for _, glob := range globs {
		ok, err := filepath.Match(glob, name)
		if err != nil {
			return false, err
		}
		matched = matched || ok
	}
	return matched, nil
}

// runScript runs a task script (check.sh or solve.sh) in directory.
func runScript(ctx context.Context, benchmarkTask task, script, directory string) (bool, string) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	//nolint:gosec // G204: the script is a file of the benchmark's own task directory
	command := exec.CommandContext(ctx, "bash", filepath.Join(benchmarkTask.Directory, script))
	command.Dir = directory
	command.Env = append(childEnv(directory), "TASK_DIR="+benchmarkTask.Directory)
	output, err := command.CombinedOutput()
	if err != nil {
		output = append(output, []byte("\nexit: "+err.Error()+"\n")...)
	}
	return err == nil, string(output)
}

// validateTasks proves each checker rejects the untouched fixture and accepts
// the reference solution.
func validateTasks(ctx context.Context, tasks []task) bool {
	allValid := true
	for _, benchmarkTask := range tasks {
		fixtureFailure, problem := validateTask(ctx, benchmarkTask)
		status, detail := "ok  ", "fixture: "+oneLine(fixtureFailure)
		if problem != "" {
			status, detail, allValid = "BAD ", problem, false
		}
		fmt.Printf("%s %-22s %s\n", status, benchmarkTask.Name, detail)
	}
	return allValid
}

// validateTask returns the first failure line of check.sh on the fixture, and
// a problem when check.sh passes the fixture or fails the solution.
func validateTask(ctx context.Context, benchmarkTask task) (fixtureFailure, problem string) {
	for _, solve := range []bool{false, true} {
		passed, output, err := checkFreshCopy(ctx, benchmarkTask, solve)
		if err != nil {
			return fixtureFailure, err.Error()
		}
		if !solve {
			fixtureFailure = firstFailLine(output)
		}
		if passed != solve {
			return fixtureFailure, fmt.Sprintf("check.sh pass=%v on %s: %s", passed, map[bool]string{false: "fixture", true: "solution"}[solve], tail(output, 400))
		}
	}
	return fixtureFailure, ""
}

// checkFreshCopy runs check.sh on a fresh copy of the fixture, after solve.sh
// when solve is set.
func checkFreshCopy(ctx context.Context, benchmarkTask task, solve bool) (passed bool, output string, err error) {
	directory, err := os.MkdirTemp("", "aikido-bench-validate-")
	if err != nil {
		return false, "", err
	}
	defer os.RemoveAll(directory)
	if err := os.CopyFS(directory, os.DirFS(filepath.Join(benchmarkTask.Directory, "fixture"))); err != nil {
		return false, "", err
	}
	if solve {
		if solved, solveOutput := runScript(ctx, benchmarkTask, "solve.sh", directory); !solved {
			return false, "", errors.New("solve.sh failed: " + tail(solveOutput, 400))
		}
	}
	passed, output = runScript(ctx, benchmarkTask, "check.sh", directory)
	return passed, output, nil
}

func writeRunInfo(ctx context.Context, runDirectory, opencodeBinary string) error {
	info := map[string]any{"args": os.Args[1:], "started": time.Now().Format(time.RFC3339), "system_prompt": systemPrompt}
	if version, err := exec.CommandContext(ctx, opencodeBinary, "--version").Output(); err == nil {
		info["opencode_version"] = strings.TrimSpace(string(version))
	}
	if commit, err := exec.CommandContext(ctx, "git", "rev-parse", "HEAD").Output(); err == nil {
		info["aikido_commit"] = strings.TrimSpace(string(commit))
	}
	encoded, _ := json.MarshalIndent(info, "", "  ")
	return os.WriteFile(filepath.Join(runDirectory, "run.json"), append(encoded, '\n'), filePermissions)
}

// childEnv is the environment for a process running in directory: no
// OpenRouter keys, and PWD pointing at directory. opencode resolves its project
// from $PWD, so an inherited PWD would send it into the benchmark checkout.
func childEnv(directory string) []string {
	var environment []string
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		if !strings.Contains(name, "OPENROUTER") && name != "PWD" {
			environment = append(environment, variable)
		}
	}
	return append(environment, "PWD="+directory)
}

func splitList(list string) []string {
	var items []string
	for _, item := range strings.Split(list, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func tail(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) > limit {
		return "..." + text[len(text)-limit:]
	}
	return text
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}
