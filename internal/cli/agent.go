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
	vfsmem "github.com/govengers/aikido/vfs/memory"

	urfavecli "github.com/urfave/cli/v3"
)

const defaultAgentSystemPrompt = "You are a focused assistant working in a virtual " +
	"file system. Use your tools to read, write, list, delete, and search files. Files " +
	"are markdown by default. Be concise."

// maximumLoggedArgumentBytes keeps a tool call's arguments to one readable line.
const maximumLoggedArgumentBytes = 200

func agentCommand(factory ClientFactory) *urfavecli.Command {
	return &urfavecli.Command{
		Name:      "agent",
		Usage:     "Run an agent loop with built-in VFS tools over an in-memory workspace",
		ArgsUsage: "<prompt...>",
		Flags: []urfavecli.Flag{
			&urfavecli.StringFlag{Name: "model", Aliases: []string{"m"}, Value: "anthropic/claude-sonnet-4.6", Usage: "OpenRouter model id"},
			&urfavecli.StringFlag{Name: "system", Aliases: []string{"s"}, Value: defaultAgentSystemPrompt, Usage: "System prompt"},
			&urfavecli.StringFlag{Name: "session", Value: "cli-session", Usage: "Session id (also used as History key)"},
			&urfavecli.IntFlag{Name: "max-tokens", Value: 4096, Usage: "Max output tokens per turn"},
			&urfavecli.FloatFlag{Name: "temperature", Aliases: []string{"t"}, Value: -1, Usage: "Sampling temperature; <0 leaves provider default"},
			newProviderFlag(),
			&urfavecli.IntFlag{Name: "max-turns", Value: 20, Usage: "Cap before EndReasonMaxTurns"},
			&urfavecli.BoolFlag{Name: "hide-hidden", Value: true, Usage: "Hide files whose path segments start with _ or ."},
		},
		Action: func(ctx context.Context, command *urfavecli.Command) error {
			client, err := factory()
			if err != nil {
				return err
			}
			return runAgent(withProviderFlag(ctx, command), agentOptions{
				model:         command.String("model"),
				system:        command.String("system"),
				sessionID:     command.String("session"),
				prompt:        promptFromArguments(command),
				maximumTokens: command.Int("max-tokens"),
				temperature:   command.Float("temperature"),
				maximumTurns:  command.Int("max-turns"),
				hideHidden:    command.Bool("hide-hidden"),
			}, resolveWriter(command), client)
		},
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
}

func runAgent(ctx context.Context, options agentOptions, out io.Writer, client llm.Client) error {
	if options.prompt == "" {
		return errors.New("missing prompt: aikido agent [flags] <prompt...>")
	}
	storage := vfsmem.NewStorage()
	session, err := newAgentSession(options, storage, client)
	if err != nil {
		return err
	}
	events, err := session.Run(ctx, options.prompt)
	if err != nil {
		return err
	}
	for event := range events {
		printAgentEvent(out, event)
	}
	// memory.Storage fails only on a done ctx, and then the run was
	// interrupted and there is no final state worth printing.
	if files, err := storage.ListFiles(ctx); err == nil {
		printWorkspace(out, files)
	}
	return nil
}

func newAgentSession(options agentOptions, storage vfs.Storage, client llm.Client) (*agent.Session, error) {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: options.hideHidden,
	}); err != nil {
		return nil, fmt.Errorf("RegisterVFSTools: %w", err)
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
