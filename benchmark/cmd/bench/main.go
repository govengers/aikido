// Command bench runs coding tasks against agent harnesses and records how
// each run went. See benchmark/README.md.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// harnessFunc runs one task in j.WorkDir and fills m with what it can observe.
type harnessFunc func(ctx context.Context, j *job, m *metrics) error

var harnesses = map[string]harnessFunc{
	"aikido":      runAikido(false),
	"aikido-bash": runAikido(true),
	"opencode":    runOpencode,
}

type task struct {
	Name     string
	Dir      string
	Category string
	Prompt   string
}

type job struct {
	Harness  string
	Model    string
	Task     task
	Rep      int
	Root     string // per-run scratch directory, outside the work dir
	WorkDir  string // the fixture copy the agent works in
	LogDir   string // transcript and logs, kept in the results dir
	Timeout  time.Duration
	APIKey   string
	Opencode string
}

func (j *job) id() string {
	return fmt.Sprintf("%s__%s__%s__%d", j.Harness, strings.ReplaceAll(j.Model, "/", "_"), j.Task.Name, j.Rep)
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
	Rep              int            `json:"rep"`
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
		harnessFlag = flag.String("harness", "aikido,aikido-bash,opencode", "comma-separated harnesses")
		modelFlag   = flag.String("model", "anthropic/claude-haiku-5.5", "comma-separated OpenRouter model ids")
		taskGlob    = flag.String("task", "*", "glob over task names")
		reps        = flag.Int("reps", 1, "repetitions per (harness, model, task)")
		parallel    = flag.Int("parallel", 4, "concurrent runs")
		timeout     = flag.Duration("timeout", 10*time.Minute, "wall-clock limit per run")
		tasksDir    = flag.String("tasks", "tasks", "task directory")
		resultsDir  = flag.String("results", "results", "results directory")
		runID       = flag.String("run-id", time.Now().Format("20060102-150405"), "name of the results subdirectory")
		opencodeBin = flag.String("opencode", "opencode", "opencode binary")
		keep        = flag.Bool("keep", false, "keep the work directories (paths are logged)")
		validate    = flag.Bool("validate", false, "check that every check.sh fails on the fixture and passes on solve.sh; no LLM calls")
		summarize   = flag.String("summarize", "", "rewrite summary.md of an existing results directory and exit")
	)
	flag.Parse()

	if *summarize != "" {
		exitOn(writeSummary(*summarize))
		return
	}
	tasks, err := loadTasks(*tasksDir, *taskGlob)
	exitOn(err)
	if *validate {
		if !validateTasks(tasks) {
			os.Exit(1)
		}
		return
	}

	hs := splitList(*harnessFlag)
	for _, h := range hs {
		if harnesses[h] == nil {
			exitOn(fmt.Errorf("unknown harness %q", h))
		}
	}
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		key = os.Getenv("CLAUDE_OPENROUTER_API_KEY")
	}
	if key == "" {
		exitOn(errors.New("set OPENROUTER_API_KEY (or CLAUDE_OPENROUTER_API_KEY)"))
	}
	if strings.Contains(*harnessFlag, "opencode") {
		bin, err := exec.LookPath(*opencodeBin)
		exitOn(err)
		*opencodeBin = bin
	}

	runDir := filepath.Join(*resultsDir, *runID)
	exitOn(os.MkdirAll(filepath.Join(runDir, "runs"), 0o755))
	exitOn(writeRunInfo(runDir, *opencodeBin))

	var jobs []*job
	for rep := 1; rep <= *reps; rep++ {
		for _, t := range tasks {
			for _, model := range splitList(*modelFlag) {
				for _, h := range hs {
					jobs = append(jobs, &job{Harness: h, Model: model, Task: t, Rep: rep, Timeout: *timeout, APIKey: key, Opencode: *opencodeBin})
				}
			}
		}
	}

	out, err := os.OpenFile(filepath.Join(runDir, "results.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	exitOn(err)
	defer out.Close()

	var (
		mu   sync.Mutex
		done int
		wg   sync.WaitGroup
		sem  = make(chan struct{}, max(1, *parallel))
	)
	for _, j := range jobs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			j.LogDir = filepath.Join(runDir, "runs", j.id())
			r := execute(j, *keep)
			line, _ := json.Marshal(r)
			mu.Lock()
			defer mu.Unlock()
			done++
			_, _ = out.Write(append(line, '\n'))
			status := "FAIL"
			if r.Pass {
				status = "PASS"
			}
			fmt.Fprintf(os.Stderr, "[%d/%d] %-4s %-11s %-28s %-20s rep%d %6.1fs $%.4f %s\n",
				done, len(jobs), status, j.Harness, j.Model, j.Task.Name, j.Rep, r.WallSeconds, r.CostUSD, r.Error)
		}()
	}
	wg.Wait()
	exitOn(writeSummary(runDir))
	fmt.Fprintf(os.Stderr, "results: %s\n", filepath.Join(runDir, "summary.md"))
}

// execute runs one job end to end and never panics the whole run.
func execute(j *job, keep bool) (r Result) {
	r = Result{Harness: j.Harness, Model: j.Model, Task: j.Task.Name, Category: j.Task.Category, Rep: j.Rep}
	fail := func(err error) Result { r.Error = err.Error(); return r }
	if err := os.MkdirAll(j.LogDir, 0o755); err != nil {
		return fail(err)
	}
	root, err := os.MkdirTemp("", "aikido-bench-")
	if err != nil {
		return fail(err)
	}
	if keep {
		_ = os.WriteFile(filepath.Join(j.LogDir, "workdir.txt"), []byte(root+"\n"), 0o644)
	} else {
		defer os.RemoveAll(root)
	}
	j.Root = root
	j.WorkDir = filepath.Join(root, "work")
	fixture := filepath.Join(j.Task.Dir, "fixture")
	if err := os.CopyFS(j.WorkDir, os.DirFS(fixture)); err != nil {
		return fail(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), j.Timeout)
	defer cancel()
	var m metrics
	start := time.Now()
	runErr := harnesses[j.Harness](ctx, j, &m)
	r.WallSeconds = time.Since(start).Seconds()
	if runErr == nil && ctx.Err() != nil {
		runErr = fmt.Errorf("timeout after %s", j.Timeout)
	}
	if p := modifiedSince(j.Task.Dir, start); p != "" {
		// The agent escaped its work dir (absolute paths); fail loudly.
		runErr = fmt.Errorf("task directory modified during the run: %s (restore it with git)", p)
	}
	if runErr != nil {
		r.Error = runErr.Error()
	}
	r.LLMCalls, r.InputTokens, r.OutputTokens = m.LLMCalls, m.InputTokens, m.OutputTokens
	r.CacheReadTokens, r.CacheWriteTokens, r.CostUSD = m.CacheReadTokens, m.CacheWriteTokens, m.CostUSD
	r.ToolCalls, r.ToolErrors, r.EndReason = m.ToolCalls, m.ToolErrors, m.EndReason

	// Measure the agent's edits before check.sh adds hidden tests.
	r.FilesChanged, r.BytesChanged, err = diffTrees(fixture, j.WorkDir)
	if err != nil && r.Error == "" {
		r.Error = err.Error()
	}
	pass, output := runScript(j.Task, "check.sh", j.WorkDir)
	_ = os.WriteFile(filepath.Join(j.LogDir, "check.log"), []byte(output), 0o644)
	r.Pass = pass
	if !pass {
		r.Check = tail(output, 600)
	}
	return r
}

func loadTasks(dir, glob string) ([]task, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	var tasks []task
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if ok, err := filepath.Match(glob, e.Name()); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		d := filepath.Join(abs, e.Name())
		prompt, err := os.ReadFile(filepath.Join(d, "task.md"))
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", e.Name(), err)
		}
		cat, err := os.ReadFile(filepath.Join(d, "category"))
		if err != nil {
			return nil, fmt.Errorf("task %s: %w", e.Name(), err)
		}
		tasks = append(tasks, task{Name: e.Name(), Dir: d, Category: strings.TrimSpace(string(cat)), Prompt: string(prompt)})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("no task in %s matches %q", dir, glob)
	}
	sort.Slice(tasks, func(a, b int) bool { return tasks[a].Name < tasks[b].Name })
	return tasks, nil
}

// runScript runs a task script (check.sh or solve.sh) in dir.
func runScript(t task, script, dir string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(t.Dir, script))
	cmd.Dir = dir
	cmd.Env = append(childEnv(dir), "TASK_DIR="+t.Dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		out = append(out, []byte("\nexit: "+err.Error()+"\n")...)
	}
	return err == nil, string(out)
}

// validateTasks proves each checker rejects the untouched fixture and accepts
// the reference solution.
func validateTasks(tasks []task) bool {
	ok := true
	for _, t := range tasks {
		var fixtureFail string
		res := func() string {
			for _, solve := range []bool{false, true} {
				dir, err := os.MkdirTemp("", "aikido-bench-validate-")
				if err != nil {
					return err.Error()
				}
				defer os.RemoveAll(dir)
				if err := os.CopyFS(dir, os.DirFS(filepath.Join(t.Dir, "fixture"))); err != nil {
					return err.Error()
				}
				if solve {
					if passed, out := runScript(t, "solve.sh", dir); !passed {
						return "solve.sh failed: " + tail(out, 400)
					}
				}
				passed, out := runScript(t, "check.sh", dir)
				if !solve {
					fixtureFail = firstFailLine(out)
				}
				if passed != solve {
					return fmt.Sprintf("check.sh pass=%v on %s: %s", passed, map[bool]string{false: "fixture", true: "solution"}[solve], tail(out, 400))
				}
			}
			return ""
		}()
		status := "ok  "
		if res != "" {
			status, ok = "BAD ", false
		} else {
			res = "fixture: " + oneLine(fixtureFail)
		}
		fmt.Printf("%s %-22s %s\n", status, t.Name, res)
	}
	return ok
}

func writeRunInfo(runDir, opencodeBin string) error {
	info := map[string]any{"args": os.Args[1:], "started": time.Now().Format(time.RFC3339), "system_prompt": systemPrompt}
	if out, err := exec.Command(opencodeBin, "--version").Output(); err == nil {
		info["opencode_version"] = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "rev-parse", "HEAD").Output(); err == nil {
		info["aikido_commit"] = strings.TrimSpace(string(out))
	}
	b, _ := json.MarshalIndent(info, "", "  ")
	return os.WriteFile(filepath.Join(runDir, "run.json"), append(b, '\n'), 0o644)
}

// childEnv is the environment for a process running in dir: no OpenRouter
// keys, and PWD pointing at dir. opencode resolves its project from $PWD, so
// an inherited PWD would send it into the benchmark checkout.
func childEnv(dir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name := strings.SplitN(kv, "=", 2)[0]
		if !strings.Contains(name, "OPENROUTER") && name != "PWD" {
			env = append(env, kv)
		}
	}
	return append(env, "PWD="+dir)
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
}
