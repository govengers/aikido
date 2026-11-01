// agent-vfs runs a session over an in-memory VFS with the built-in tools.
//
// The model is asked to write a notes file and then read it back. Set
// OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/agent-vfs
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	vfsmem "github.com/govengers/aikido/vfs/memory"
)

const prompt = "Create a file called facts.md with three short facts about the Go programming language, " +
	"one per bullet point. Then read the file back and tell me what you wrote."

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		return errors.New("OPENROUTER_API_KEY not set")
	}
	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido agent-vfs example",
	})
	if err != nil {
		return err
	}

	storage := vfsmem.NewStorage()
	session, err := newSession(client, storage)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	events, err := session.Run(ctx, prompt)
	if err != nil {
		return err
	}
	for event := range events {
		printEvent(event)
	}

	files, err := storage.ListFiles(ctx)
	if err != nil {
		return err
	}
	fmt.Println("\n--- final VFS state ---")
	for _, file := range files {
		fmt.Printf("- %s (%d bytes)\n", file.Path, file.Size)
	}
	return nil
}

func newSession(client llm.Client, storage vfs.Storage) (*agent.Session, error) {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: true,
	}); err != nil {
		return nil, err
	}
	return agent.NewLocalSession(&agent.SessionOptions{
		ID:     "demo",
		Client: client,
		Tools:  registry,
		Model:  "anthropic/claude-sonnet-4.6",
		SystemPrompt: "You are a research assistant working in a virtual file system. " +
			"You can read, write, list, delete, and search files using your tools. " +
			"Files end in .md and use markdown formatting.",
	})
}

func printEvent(event agent.Event) {
	switch event.Kind {
	case agent.EventText:
		fmt.Print(event.Text)
	case agent.EventToolCall:
		fmt.Printf("\n[tool-call] %s(%s)\n", event.ToolCall.Name, event.ToolCall.Arguments)
	case agent.EventToolResult:
		status := "✓"
		if !event.ToolResult.OK {
			status = "✗"
		}
		fmt.Printf("[tool-result] %s %s\n", status, event.ToolResult.Name)
	case agent.EventUsage:
		fmt.Printf("\n[usage] prompt=%d completion=%d cost=$%.6f\n",
			event.Usage.PromptTokens, event.Usage.CompletionTokens, event.Usage.CostUSD)
	case agent.EventError:
		fmt.Printf("\n[error] %v\n", event.Err)
	case agent.EventEnd:
		fmt.Printf("\n[end] %s\n", event.EndReason)
	default:
		// Thinking and image events are not shown; EventThinking carries the
		// model's reasoning fragments in event.Text.
	}
}
