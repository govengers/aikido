// chatbot is an interactive Q&A loop over an embedded markdown corpus.
//
// The knowledge files under knowledge/ are compiled into the binary using
// the embed package. The agent only sees read-tools (read_file, list_files,
// search): write_file and delete_file are not registered, so the corpus is
// immutable at runtime. Defense in depth: vfs/embedfs also rejects mutations.
//
//	go run ./examples/chatbot
//
// Set OPENROUTER_API_KEY in the environment first.
package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
	"github.com/govengers/aikido/tools"
	"github.com/govengers/aikido/vfs"
	"github.com/govengers/aikido/vfs/embedfs"
)

//go:embed knowledge/*.md
var knowledgeFS embed.FS

const systemPrompt = `You are a knowledge bot for the Go programming language. You have a small ` +
	`corpus of curated markdown files in your virtual workspace. Use list_files to enumerate them, ` +
	`search to find files mentioning a term, and read_file to read content before answering. ` +
	`Always cite the source file when answering. If the corpus does not contain an answer, say so ` +
	`plainly; do not speculate. Be concise.`

const (
	turnTimeout                = 2 * time.Minute
	maximumLoggedArgumentBytes = 120
	maximumPromptBytes         = 1024 * 1024
)

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
		XTitle:      "aikido chatbot example",
	})
	if err != nil {
		return err
	}
	knowledge, err := fs.Sub(knowledgeFS, "knowledge")
	if err != nil {
		return err
	}
	storage, err := embedfs.New(&embedfs.Options{FileSystem: knowledge})
	if err != nil {
		return err
	}
	session, err := newSession(client, storage)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := printCorpus(ctx, storage); err != nil {
		return err
	}
	return chat(ctx, session)
}

func newSession(client llm.Client, storage vfs.Storage) (*agent.Session, error) {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: true,
		ReadOnly:        true, // chat-only: write_file and delete_file are not registered
	}); err != nil {
		return nil, err
	}
	return agent.NewLocalSession(&agent.SessionOptions{
		ID:           "chatbot",
		Client:       client,
		Tools:        registry,
		Model:        "anthropic/claude-haiku-4.5",
		SystemPrompt: systemPrompt,
		MaximumTurns: 10,
	})
}

func printCorpus(ctx context.Context, storage vfs.Storage) error {
	files, err := storage.ListFiles(ctx)
	if err != nil {
		return err
	}
	fmt.Println("aikido chatbot - ask anything about Go.")
	fmt.Println("Knowledge corpus:")
	for _, file := range files {
		fmt.Printf("  - %s (%d bytes)\n", file.Path, file.Size)
	}
	fmt.Println("Type 'exit' to quit.")
	return nil
}

// chat answers one prompt per stdin line until exit, quit or end of input.
func chat(ctx context.Context, session *agent.Session) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), maximumPromptBytes)
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		prompt := strings.TrimSpace(scanner.Text())
		if prompt == "exit" || prompt == "quit" {
			return nil
		}
		if prompt != "" {
			answer(ctx, session, prompt)
		}
	}
}

// answer runs one turn and prints it; a failed turn is reported and the chat
// goes on.
func answer(ctx context.Context, session *agent.Session, prompt string) {
	turnContext, cancel := context.WithTimeout(ctx, turnTimeout)
	defer cancel()
	events, err := session.Run(turnContext, prompt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return
	}
	for event := range events {
		printEvent(event)
	}
}

func printEvent(event agent.Event) {
	switch event.Kind {
	case agent.EventText:
		fmt.Print(event.Text)
	case agent.EventToolCall:
		fmt.Printf("\n  · %s(%s)\n", event.ToolCall.Name, truncate(event.ToolCall.Arguments, maximumLoggedArgumentBytes))
	case agent.EventError:
		fmt.Fprintf(os.Stderr, "\n[error] %v\n", event.Err)
	case agent.EventEnd:
		if event.EndReason != agent.EndReasonStop {
			fmt.Printf("\n[end: %s]\n", event.EndReason)
		}
	default:
		// Thinking, tool results, images and usage stay off the chat.
	}
}

func truncate(text string, maximumLength int) string {
	if len(text) <= maximumLength {
		return text
	}
	return text[:maximumLength] + "…"
}
