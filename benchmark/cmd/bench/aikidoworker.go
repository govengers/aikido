package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// aikidoWorkerFlags are the `aikido agent` flags the aikido-worker harness
// drives (virtual bash plus allowed host programs, writing straight to --dir).
var aikidoWorkerFlags = []string{"--direct", "--shell", "--allow", "--env", "--system-file", "--max-turns"}

const aikidoWorkerMaximumTurns = "80"

// prepareAikidoWorker builds the aikido CLI from this checkout and checks that
// it has the flags; without them the harness is skipped.
func prepareAikidoWorker(ctx context.Context, binaries string, template *job) error {
	repository, err := filepath.Abs("..")
	if err != nil {
		return err
	}
	binary, err := filepath.Abs(filepath.Join(binaries, "aikido"))
	if err != nil {
		return err
	}
	//nolint:gosec // G204: building this checkout's own CLI into the run directory
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/aikido")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build aikido: %w: %s", err, tail(string(output), 300))
	}
	//nolint:gosec // G204: the binary just built from this checkout
	help, err := exec.CommandContext(ctx, binary, "agent", "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("aikido agent --help: %w", err)
	}
	var missing []string
	for _, name := range aikidoWorkerFlags {
		if !regexp.MustCompile(regexp.QuoteMeta(name) + `\b`).Match(help) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("aikido agent of this checkout lacks %s (the virtual-shell CLI is not merged yet)", strings.Join(missing, ", "))
	}
	template.AikidoBinary = binary
	return nil
}

func aikidoWorkerArguments(run *job) []string {
	arguments := []string{
		"agent", "--dir", run.WorkDirectory, "--direct", "--shell",
		"--allow", "git", "--allow", "kubectl", "--allow", "logcli",
	}
	for _, name := range workerVariables {
		arguments = append(arguments, "--env", name)
	}
	return append(arguments, "--system-file", "CLAUDE.md", "--max-turns", aikidoWorkerMaximumTurns,
		"--model", run.Model, run.Task.Prompt)
}

// runAikidoWorker runs the aikido CLI on the worker clone with its virtual
// bash and the host programs git, kubectl and logcli, and reads its transcript
// lines ([tool-result], [usage], [end]).
func runAikidoWorker(ctx context.Context, run *job, observed *metrics) error {
	//nolint:gosec // G204: the binary built from this checkout; the prompt is the task's task.md
	command := exec.CommandContext(ctx, run.AikidoBinary, aikidoWorkerArguments(run)...)
	command.Dir = run.WorkDirectory
	command.Env = append(append(childEnv(run.WorkDirectory), run.Env...), "OPENROUTER_API_KEY="+run.APIKey)
	killGroup(command)
	err := runStreaming(ctx, run, command, streamParser{transcript: "transcript.log", parse: func(stream io.Reader) error {
		return parseAikidoLines(stream, observed)
	}}, observed)
	if err == nil && observed.EndReason != "stop" {
		return fmt.Errorf("run ended with %s", observed.EndReason)
	}
	return err
}

var (
	aikidoUsageLine  = regexp.MustCompile(`^\[usage\] prompt=(\d+) completion=(\d+) cost=\$([0-9.]+)`)
	aikidoResultLine = regexp.MustCompile(`^\[tool-result\] (\S+) (ok|fail)`)
	aikidoEndLine    = regexp.MustCompile(`^\[end\] (\S+)`)
	aikidoErrorLine  = regexp.MustCompile(`^\[error\] (.*)`)
)

// parseAikidoLines reads the CLI transcript (internal/cli/agent.go). The CLI
// reports no cache tokens, so those stay zero.
func parseAikidoLines(stream io.Reader, observed *metrics) error {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	var runError error
	for scanner.Scan() {
		line := scanner.Text()
		if match := aikidoUsageLine.FindStringSubmatch(line); match != nil {
			prompt, _ := strconv.Atoi(match[1])
			completion, _ := strconv.Atoi(match[2])
			cost, _ := strconv.ParseFloat(match[3], 64)
			observed.LLMCalls++
			observed.InputTokens += prompt
			observed.OutputTokens += completion
			observed.CostUSD += cost
		} else if match := aikidoResultLine.FindStringSubmatch(line); match != nil {
			observed.tool(match[1], match[2] == "fail")
		} else if match := aikidoEndLine.FindStringSubmatch(line); match != nil {
			observed.EndReason = match[1]
		} else if match := aikidoErrorLine.FindStringSubmatch(line); match != nil && runError == nil {
			runError = errors.New(match[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return runError
}
