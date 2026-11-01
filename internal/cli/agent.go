package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"

	urfavecli "github.com/urfave/cli/v3"
)

const defaultAgentSystemPrompt = "You are a focused assistant working in a virtual " +
	"file system. Use your tools to read, write, list, delete, and search files. Files " +
	"are markdown by default. Be concise."

// workerSystemPrompt opens the system prompt when --system-file supplies the
// instructions and --system is not set.
const workerSystemPrompt = "You are an autonomous agent working unattended in the repository that is your " +
	"workspace. Nobody answers questions during the run: decide from the repository's instructions below, " +
	"use your tools to inspect and change files and to run commands, and finish the task end to end. " +
	"Never claim a check you did not run."

// maximumLoggedArgumentBytes keeps a tool call's arguments to one readable line.
const maximumLoggedArgumentBytes = 200

func agentCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:  "agent",
		Usage: "Run an agent loop with built-in VFS tools over an in-memory workspace or, with --dir, a directory",
		Description: "Without --dir the agent works on an empty in-memory workspace. With --dir it\n" +
			"works on a copy-on-write overlay of the directory: at the end the CLI prints the\n" +
			"changes and a unified diff, and writes them only with --apply. --mount name=path\n" +
			"adds a directory under name/ (append :ro to make it read-only); it is written\n" +
			"with --apply like --dir.",
		ArgsUsage: "<prompt...>",
		Flags:     agentFlags(),
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			mounts, err := parseMountFlags(command.StringSlice("mount"))
			if err != nil {
				return err
			}
			system := command.String("system")
			if command.String("system-file") != "" && !command.IsSet("system") {
				system = workerSystemPrompt
			}
			system, err = systemPrompt(system, command.String("system-file"))
			if err != nil {
				return err
			}
			client, err := factory()
			if err != nil {
				return err
			}
			return runAgent(withProviderFlag(ctx, command), agentOptions{
				model:         command.String("model"),
				system:        system,
				sessionID:     command.String("session"),
				prompt:        promptFromArguments(command),
				maximumTokens: command.Int("max-tokens"),
				temperature:   command.Float("temperature"),
				maximumTurns:  command.Int("max-turns"),
				hideHidden:    command.Bool("hide-hidden"),
				directory:     command.String("dir"),
				apply:         command.Bool("apply"),
				mounts:        mounts,
				worker: workerOptions{
					shell:       command.Bool("shell"),
					direct:      command.Bool("direct"),
					allowed:     command.StringSlice("allow"),
					environment: command.StringSlice("env"),
				},
			}, resolveWriter(command), client)
		},
	}
}

func agentFlags() []urfavecli.Flag {
	return []urfavecli.Flag{
		&urfavecli.StringFlag{Name: "model", Aliases: []string{"m"}, Value: "anthropic/claude-sonnet-4.6", Usage: "OpenRouter model id"},
		&urfavecli.StringFlag{Name: "system", Aliases: []string{"s"}, Value: defaultAgentSystemPrompt, Usage: "System prompt"},
		&urfavecli.StringFlag{Name: "session", Value: "cli-session", Usage: "Session id (also used as History key)"},
		&urfavecli.IntFlag{Name: "max-tokens", Value: 4096, Usage: "Max output tokens per turn"},
		&urfavecli.FloatFlag{Name: "temperature", Aliases: []string{"t"}, Value: -1, Usage: "Sampling temperature; <0 leaves provider default"},
		newProviderFlag(),
		&urfavecli.IntFlag{Name: "max-turns", Value: 20, Usage: "Cap before EndReasonMaxTurns"},
		&urfavecli.BoolFlag{Name: "hide-hidden", Value: true, Usage: "Hide files whose path segments start with _ or ."},
		&urfavecli.StringFlag{Name: "dir", Usage: "Work on this directory through an overlay; nothing is written without --apply"},
		&urfavecli.BoolFlag{Name: "apply", Usage: "Write the changes of the run to --dir and the writable --mount directories"},
		&urfavecli.StringSliceFlag{Name: "mount", Usage: "Extra directory as name=path, read-only with name=path:ro (repeatable)"},
		&urfavecli.BoolFlag{Name: "shell", Usage: "Give the agent a virtual bash over the workspace (tools/shell): no host access"},
		&urfavecli.BoolFlag{Name: "direct", Usage: "With --dir: write changes to disk as they happen instead of through an overlay (no dry run)"},
		&urfavecli.StringSliceFlag{Name: "allow", Usage: "Host program the bash tool may run, as name (from PATH) or name=path; gives the agent everything that program can do (git: config and program options refused); needs --shell and --direct (repeatable)"},
		&urfavecli.StringSliceFlag{Name: "env", Usage: "Variable for the allowed host programs, as NAME (copied from this environment) or NAME=value (repeatable)"},
		&urfavecli.StringFlag{Name: "system-file", Usage: "Read the instructions from this file (e.g. the repo's CLAUDE.md) and append them to the system prompt"},
	}
}

type agentOptions struct {
	model         string
	system        string
	sessionID     string
	prompt        string
	maximumTokens int
	temperature   float64 // negative leaves the provider default
	maximumTurns  int
	hideHidden    bool
	directory     string // empty: in-memory workspace
	apply         bool
	mounts        []mountSpec
	worker        workerOptions
}

func runAgent(ctx context.Context, options agentOptions, out io.Writer, client llm.Client) error {
	if options.prompt == "" {
		return errors.New("missing prompt: aikido agent [flags] <prompt...>")
	}
	if options.apply && options.directory == "" && len(options.mounts) == 0 {
		return errors.New("--apply needs --dir or --mount: the in-memory workspace has nothing to write to")
	}
	if err := validateWorkerOptions(options); err != nil {
		return err
	}
	workspace, err := openWorkspace(options.directory, options.mounts, options.worker.direct)
	if err != nil {
		return err
	}
	defer workspace.close()
	session, err := newAgentSession(options, workspace.storage, client)
	if err != nil {
		return err
	}
	events, err := session.Run(ctx, options.prompt)
	if err != nil {
		return err
	}
	endReason := agent.EndReasonError
	var runError error
	for event := range events {
		printAgentEvent(out, event)
		switch event.Kind {
		case agent.EventError:
			if runError == nil {
				runError = event.Err
			}
		case agent.EventEnd:
			endReason = event.EndReason
		default:
		}
	}
	reportError := workspace.report(ctx, out, applyDecision(options.apply, endReason))
	return errors.Join(runFailure(endReason, runError), reportError)
}

// runFailure is the error the command exits with when the run ended with an
// error, a timeout or a cancel, dry run or not; cause is the run's EventError.
// A run that stopped or hit the turn cap is no failure here.
func runFailure(endReason agent.EndReason, cause error) error {
	switch endReason {
	case agent.EndReasonError, agent.EndReasonTimeout, agent.EndReasonCancelled:
		if cause != nil {
			return fmt.Errorf("the run ended with %s: %w", endReason, cause)
		}
		return fmt.Errorf("the run ended with %s", endReason)
	default:
		return nil
	}
}

// applyDecision allows --apply only for a run the model finished itself: a run
// cut off by an error, a timeout, a cancel or the turn cap may have left a
// half-done edit, which stays a dry run.
func applyDecision(applyRequested bool, endReason agent.EndReason) commitDecision {
	switch {
	case !applyRequested:
		return commitDecision{}
	case endReason != agent.EndReasonStop:
		return commitDecision{refusedReason: string(endReason)}
	default:
		return commitDecision{apply: true}
	}
}

// commitDecision says whether the workspace writes its changes; refusedReason
// is set when --apply was requested but the run did not end normally.
type commitDecision struct {
	apply         bool
	refusedReason string
}

func newAgentSession(options agentOptions, storage vfs.Storage, client llm.Client) (*agent.Session, error) {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: options.hideHidden,
	}); err != nil {
		return nil, fmt.Errorf("RegisterVFSTools: %w", err)
	}
	if err := registerShell(registry, options, storage); err != nil {
		return nil, err
	}
	sessionOptions := &agent.SessionOptions{
		ID:            options.sessionID,
		Client:        client,
		Tools:         registry,
		Model:         options.model,
		SystemPrompt:  options.system,
		MaximumTokens: options.maximumTokens,
		MaximumTurns:  options.maximumTurns,
	}
	if options.temperature >= 0 {
		sessionOptions.Temperature = new(float32(options.temperature))
	}
	return agent.NewLocalSession(sessionOptions)
}

func printAgentEvent(out io.Writer, event agent.Event) {
	switch event.Kind {
	case agent.EventText:
		fmt.Fprint(out, event.Text)
	case agent.EventToolCall:
		fmt.Fprintf(out, "\n[tool-call] %s(%s)\n", event.ToolCall.Name, truncateForLog(event.ToolCall.Arguments, maximumLoggedArgumentBytes))
	case agent.EventToolResult:
		status := "ok"
		if !event.ToolResult.OK {
			status = "fail: " + event.ToolResult.Error
		}
		fmt.Fprintf(out, "[tool-result] %s %s\n", event.ToolResult.Name, status)
	case agent.EventUsage:
		fmt.Fprintf(out, "\n%s\n", formatUsage(event.Usage))
	case agent.EventError:
		fmt.Fprintf(out, "\n[error] %v\n", event.Err)
	case agent.EventEnd:
		fmt.Fprintf(out, "\n[end] %s\n", event.EndReason)
	default:
		// Thinking and image events have no line in the CLI transcript.
	}
}

func printWorkspace(out io.Writer, files []vfs.FileMetadata) {
	if len(files) == 0 {
		return
	}
	fmt.Fprintln(out, "\n--- final VFS state ---")
	for _, file := range files {
		fmt.Fprintf(out, "- %s (%d bytes, %s)\n", file.Path, file.Size, file.ContentType)
	}
}

func truncateForLog(text string, maximumLength int) string {
	if len(text) <= maximumLength {
		return text
	}
	return text[:maximumLength] + "…"
}
