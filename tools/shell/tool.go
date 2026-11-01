package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
)

// toolOutput is what the model receives for one call.
type toolOutput struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

// Register adds the shell as one tool (named Options.ToolName, "bash" by
// default) to registry. The tool takes {"command": "...", "description":
// "..."}; the description is for logs and is ignored. Every call runs on the
// same Shell, so the working directory persists between calls.
//
// It returns an error for a nil registry, the errors of New, and an error
// matching tools.ErrDuplicateTool if the name is taken.
func Register(registry *tools.Registry, options *Options) error {
	if registry == nil {
		return errors.New("aikido/tools/shell: Register: nil registry")
	}
	shell, err := New(options)
	if err != nil {
		return err
	}
	definition := llm.ToolDefinition{
		Name:        shell.options.ToolName,
		Description: shell.description(),
		Parameters: tools.Object(map[string]any{
			"command":     tools.String("The bash script to run."),
			"description": tools.String("What the command does, in 5-10 words."),
		}, "command"),
	}
	if err := registry.Register(definition, shell.handle); err != nil {
		return fmt.Errorf("aikido/tools/shell: Register: %w", err)
	}
	return nil
}

// handle is the tool handler: it runs the command and returns its output
// and exit code. A failing script is a normal result, not a tool error.
func (s *Shell) handle(ctx context.Context, arguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(arguments, &parsed); err != nil {
		return tools.Result{}, fmt.Errorf("%w for %s: argument \"command\" must be a string", tools.ErrInvalidArguments, s.options.ToolName)
	}
	result, err := s.Run(ctx, parsed.Command)
	if err != nil {
		return tools.Result{}, err
	}
	return tools.Result{Content: toolOutput(result)}, nil
}

// description is the tool description the model reads: what the shell is,
// its commands from their summaries, and what it cannot do.
func (s *Shell) description() string {
	var text strings.Builder
	text.WriteString("Runs bash over the project workspace (\"/\" is the workspace root). Pipes, &&, ||, loops, " +
		"functions, variables, $(...), <(...), here-docs, redirects and globs work as in bash; the working directory " +
		"persists between calls, variables do not. /tmp is in-memory scratch space (64 MiB) that persists between calls. " +
		"Shell builtins: cd, pwd, echo, printf, test/[, read, export, set, exit. Available commands: ")
	for index, name := range s.commandNames() {
		if index > 0 {
			text.WriteString("; ")
		}
		text.WriteString(name + ": " + s.commands[name].Summary)
	}
	text.WriteString(". No network, no package managers, no compilers or interpreters unless listed.")
	return text.String()
}
