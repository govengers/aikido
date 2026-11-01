// agent-overlay lets an agent edit a real directory through a copy-on-write
// overlay, prints the change list and a unified diff, and writes the changes
// to disk only after you confirm.
//
// Without an argument it works on a fresh temporary directory with one file.
// Set OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/agent-overlay [directory]
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/osfs"
	"github.com/govengers/aikido/vfs/overlay"
)

const prompt = "Read every file in the workspace. Fix spelling mistakes in place and add a file " +
	"SUMMARY.md with one sentence per file describing it."

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
		XTitle:      "aikido agent-overlay example",
	})
	if err != nil {
		return err
	}
	directory, err := workingDirectory()
	if err != nil {
		return err
	}

	disk, err := osfs.New(&osfs.Options{Directory: directory})
	if err != nil {
		return err
	}
	defer func() { _ = disk.Close() }() // a read handle; closing it loses nothing
	workspace, err := overlay.New(&overlay.Options{Base: disk})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := runSession(ctx, client, workspace); err != nil {
		return err
	}
	return review(ctx, workspace, directory)
}

// workingDirectory is the first argument, or a temporary directory seeded
// with a file to fix.
func workingDirectory() (string, error) {
	if len(os.Args) > 1 {
		return os.Args[1], nil
	}
	directory, err := os.MkdirTemp("", "aikido-agent-overlay-")
	if err != nil {
		return "", err
	}
	notes := "# Shoping list\n\n- milk\n- egs\n- bread\n"
	if err := os.WriteFile(filepath.Join(directory, "notes.md"), []byte(notes), 0o600); err != nil {
		return "", err
	}
	fmt.Println("working on", directory)
	return directory, nil
}

func runSession(ctx context.Context, client llm.Client, storage vfs.Storage) error {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: storage, HideHiddenPaths: true}); err != nil {
		return err
	}
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID:           "overlay-demo",
		Client:       client,
		Tools:        registry,
		Model:        "anthropic/claude-sonnet-4.6",
		SystemPrompt: "You edit the files of a project directory with your tools. Change only what the task asks for.",
	})
	if err != nil {
		return err
	}
	events, err := session.Run(ctx, prompt)
	if err != nil {
		return err
	}
	for event := range events {
		switch event.Kind {
		case agent.EventText:
			fmt.Print(event.Text)
		case agent.EventToolCall:
			fmt.Printf("\n[tool-call] %s\n", event.ToolCall.Name)
		case agent.EventError:
			fmt.Printf("\n[error] %v\n", event.Err)
		default:
			// Results, usage and the end event are not needed for the review.
		}
	}
	return nil
}

// review prints what the agent changed and commits it to the directory only
// when the user answers yes.
func review(ctx context.Context, workspace *overlay.Storage, directory string) error {
	changes, err := workspace.Changes(ctx)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		fmt.Println("\nno changes")
		return nil
	}
	fmt.Println("\n--- changes ---")
	for _, change := range changes {
		fmt.Printf("%-6s %s\n", change.Kind, change.Path)
	}
	fmt.Println("\n--- diff ---")
	fmt.Print(overlay.UnifiedDiff(changes))

	fmt.Printf("\nwrite these changes to %s? [y/N] ", directory)
	// A read error or EOF returns what was typed so far; nothing counts as no.
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(strings.ToLower(answer)) != "y" {
		workspace.Discard()
		fmt.Println("discarded, nothing written")
		return nil
	}
	if err := workspace.Commit(ctx); err != nil {
		return err
	}
	fmt.Println("written")
	return nil
}
