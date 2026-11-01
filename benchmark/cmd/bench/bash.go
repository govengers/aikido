package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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

func bashHandler(directory string) tools.Handler {
	return func(ctx context.Context, arguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var input struct {
			Command string `json:"command"`
		}
		if err := json.Unmarshal(arguments, &input); err != nil {
			return tools.Result{}, fmt.Errorf("bash args: %w", err)
		}
		if input.Command == "" {
			return tools.Result{}, errors.New("bash: command is empty")
		}
		ctx, cancel := context.WithTimeout(ctx, bashTimeout)
		defer cancel()
		//nolint:gosec // G204: running the model's command line is what this tool is for (see the ponytail note above)
		command := exec.CommandContext(ctx, "bash", "-c", input.Command)
		command.Dir = directory
		command.Env = childEnv(directory)
		killGroup(command)
		output := &cappedBuffer{limit: bashOutputLimit}
		command.Stdout, command.Stderr = output, output
		err := command.Run()

		exitCode := 0
		var exitError *exec.ExitError
		switch {
		case errors.As(err, &exitError):
			exitCode = exitError.ExitCode()
		case err != nil:
			return tools.Result{}, fmt.Errorf("bash: %w", err)
		}
		text := string(output.buffer)
		if output.total > len(output.buffer) {
			text += fmt.Sprintf("\n[output truncated: first %d of %d bytes shown]", len(output.buffer), output.total)
		}
		result := map[string]any{"output": text, "exit_code": exitCode}
		if ctx.Err() != nil {
			result["timed_out"] = true
		}
		return tools.Result{Content: result}, nil
	}
}

// cappedBuffer keeps the first limit bytes and counts the rest.
type cappedBuffer struct {
	limit  int
	buffer []byte
	total  int
}

func (b *cappedBuffer) Write(data []byte) (int, error) {
	b.total += len(data)
	if room := b.limit - len(b.buffer); room > 0 {
		b.buffer = append(b.buffer, data[:min(room, len(data))]...)
	}
	return len(data), nil
}
