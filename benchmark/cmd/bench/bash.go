package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

// Prototype bash tool for the aikido-bash harness. It lives here, not in the
// library, because it only exists to measure what a CLI tool would change.
// ponytail: no sandbox, the command runs as the benchmark user; a library
// version would need policy (allow-list, network, filesystem scope).
const (
	bashTimeout     = 60 * time.Second
	bashOutputLimit = 30 << 10
)

func bashToolDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "bash",
		Description: fmt.Sprintf("Run a shell command with `bash -c` in the project root and return its combined stdout and stderr plus the exit code. "+
			"Every call starts a fresh shell (no state carries over), stdin is empty, the timeout is %s and the output is cut after %d KB.",
			bashTimeout, bashOutputLimit>>10),
		Parameters: tools.Object(map[string]any{
			"command": tools.String("The command line to run, e.g. `go test ./...` or `grep -rn foo .`."),
		}, "command"),
	}
}

func bashHandler(dir string) tools.Handler {
	return func(ctx context.Context, args json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var v struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(args, &v); err != nil {
			return tools.Result{}, fmt.Errorf("bash args: %w", err)
		}
		if v.Command == "" {
			return tools.Result{}, errors.New("bash: command is empty")
		}
		ctx, cancel := context.WithTimeout(ctx, bashTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", v.Command)
		cmd.Dir = dir
		cmd.Env = childEnv(dir)
		killGroup(cmd)
		out := &cappedBuffer{limit: bashOutputLimit}
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()

		exitCode := 0
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			exitCode = exitErr.ExitCode()
		case err != nil:
			return tools.Result{}, fmt.Errorf("bash: %w", err)
		}
		output := string(out.buf)
		if out.total > len(out.buf) {
			output += fmt.Sprintf("\n[output truncated: first %d of %d bytes shown]", len(out.buf), out.total)
		}
		res := map[string]any{"output": output, "exit_code": exitCode}
		if ctx.Err() != nil {
			res["timed_out"] = true
		}
		return tools.Result{Content: res}, nil
	}
}

// killGroup puts the command in its own process group and kills the whole
// group on cancel, so `go test` children or background jobs do not linger.
func killGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}

// cappedBuffer keeps the first limit bytes and counts the rest.
type cappedBuffer struct {
	limit int
	buf   []byte
	total int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.total += len(p)
	if room := b.limit - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}
