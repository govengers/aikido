// chat-oneshot demonstrates a non-streaming completion via llm.Collect.
//
// Set OPENROUTER_API_KEY to run against the real provider:
//
//	go run ./examples/chat-oneshot
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/openrouter"
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
		XTitle:      "aikido chat-oneshot example",
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	response, err := llm.Collect(ctx, client, llm.Request{
		Model: "anthropic/claude-haiku-4.5",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Be concise. Reply in one sentence."},
			{Role: llm.RoleUser, Content: "Give me one fun fact about the Go programming language."},
		},
		MaximumTokens: 200,
		Temperature:   new(float32(0.7)),
	})
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	fmt.Println("---")
	fmt.Println(response.Text)
	fmt.Println("---")
	if usage := response.Usage; usage != nil {
		fmt.Printf("tokens: prompt=%d completion=%d cost=$%.6f\n",
			usage.PromptTokens, usage.CompletionTokens, usage.CostUSD)
	}
	return nil
}
