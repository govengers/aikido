package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func TestRunChat_HappyPath(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "concise reply"},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 4, CompletionTokens: 2, CostUSD: 0.0001}},
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	err := runChat(context.Background(), chatOptions{
		model:         "x",
		prompt:        "say hi",
		maximumTokens: 100,
		temperature:   -1,
	}, &out, stub)
	if err != nil {
		t.Fatalf("runChat: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "concise reply") {
		t.Errorf("missing reply: %q", got)
	}
	if !strings.Contains(got, "[usage]") {
		t.Errorf("missing usage line: %q", got)
	}
	requests := stub.Requests()
	if len(requests) != 1 || requests[0].Model != "x" {
		t.Errorf("model not threaded: %+v", requests)
	}
	if requests[0].Temperature != nil {
		t.Errorf("temperature should be nil when negative, got %v", *requests[0].Temperature)
	}
}

func TestRunChat_ExplicitTemperature(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	if err := runChat(context.Background(), chatOptions{prompt: "x", model: "m", temperature: 0.5}, &out, stub); err != nil {
		t.Fatalf("runChat: %v", err)
	}
	requests := stub.Requests()
	if requests[0].Temperature == nil || *requests[0].Temperature != 0.5 {
		t.Errorf("temperature not threaded: %+v", requests[0].Temperature)
	}
}

func TestRunChat_RequiresPrompt(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	if err := runChat(context.Background(), chatOptions{model: "m"}, &out, stub); err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestRunChat_PropagatesStreamError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventError, Err: errors.New("boom")},
		{Kind: llm.EventEnd},
	}})
	var out bytes.Buffer
	err := runChat(context.Background(), chatOptions{prompt: "hi", model: "m"}, &out, stub)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v; want boom", err)
	}
}

func TestRunChat_SystemPromptThreaded(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventEnd}}})
	var out bytes.Buffer
	_ = runChat(context.Background(), chatOptions{prompt: "x", model: "m", system: "be terse"}, &out, stub)
	requests := stub.Requests()
	if len(requests[0].Messages) != 2 {
		t.Fatalf("messages = %+v", requests[0].Messages)
	}
	if requests[0].Messages[0].Role != llm.RoleSystem || requests[0].Messages[0].Content != "be terse" {
		t.Errorf("system message = %+v", requests[0].Messages[0])
	}
}
