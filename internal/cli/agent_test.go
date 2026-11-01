package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

func TestRunAgent_RoundTrip(t *testing.T) {
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{
				ID: "c1", Name: "write_file",
				Arguments: `{"path":"hello.md","content":"hi there"}`,
			}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "wrote it"},
			{Kind: llm.EventEnd},
		}},
	)
	var out bytes.Buffer
	err := runAgent(context.Background(), agentOptions{
		model: "m", sessionID: "s1", prompt: "do the thing",
		maximumTokens: 100, temperature: -1, maximumTurns: 5,
	}, &out, stub)
	if err != nil {
		t.Fatalf("runAgent: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "wrote it") {
		t.Errorf("missing assistant text: %q", got)
	}
	if !strings.Contains(got, "[tool-call] write_file") {
		t.Errorf("missing tool-call: %q", got)
	}
	if !strings.Contains(got, "[end] stop") {
		t.Errorf("missing end: %q", got)
	}
	if !strings.Contains(got, "hello.md") {
		t.Errorf("VFS state not printed: %q", got)
	}
}

func TestRunAgent_RequiresPrompt(t *testing.T) {
	stub := llmtest.NewStubClient()
	var out bytes.Buffer
	if err := runAgent(context.Background(), agentOptions{model: "m"}, &out, stub); err == nil {
		t.Error("expected error for missing prompt")
	}
}

func TestPrintAgentEvent(t *testing.T) {
	testCases := []struct {
		name  string
		event agent.Event
		want  string
	}{
		{"text", agent.Event{Kind: agent.EventText, Text: "hi"}, "hi"},
		{"failed tool", agent.Event{Kind: agent.EventToolResult, ToolResult: &agent.ToolResult{Name: "read_file", Error: "missing"}}, "[tool-result] read_file fail: missing\n"},
		{"error", agent.Event{Kind: agent.EventError, Err: errors.New("boom")}, "\n[error] boom\n"},
		{"usage", agent.Event{Kind: agent.EventUsage, Usage: &llm.Usage{PromptTokens: 1}}, "\n[usage] prompt=1 completion=0 cost=$0.000000\n"},
		{"thinking is not printed", agent.Event{Kind: agent.EventThinking, Text: "hmm"}, ""},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			var out bytes.Buffer
			printAgentEvent(&out, testCase.event)
			if out.String() != testCase.want {
				t.Errorf("printed %q, want %q", out.String(), testCase.want)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("abcdef", 3); got != "abc…" {
		t.Errorf("truncateForLog = %q, want abc…", got)
	}
	if got := truncateForLog("abc", 3); got != "abc" {
		t.Errorf("truncateForLog = %q, want abc", got)
	}
}
