//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/agent"
	historymemory "github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/tools"
	vfsmemory "github.com/govengers/aikido/vfs/memory"
)

// workspaceWith is an in-memory workspace holding one file.
func workspaceWith(t *testing.T, path, content string) *vfsmemory.Storage {
	t.Helper()
	storage := vfsmemory.New()
	if err := storage.WriteFile(t.Context(), path, []byte(content), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	return storage
}

// toolResultEvent returns the EventToolResult answering callID.
func toolResultEvent(t *testing.T, events []agent.Event, callID string) agent.ToolResult {
	t.Helper()
	for _, event := range events {
		if event.Kind == agent.EventToolResult && event.ToolResult.CallID == callID {
			return *event.ToolResult
		}
	}
	t.Fatalf("no EventToolResult for %s in %+v", callID, events)
	return agent.ToolResult{}
}

func TestToolResultsOverTheCapReachTheModelTruncatedButTheCallerInFull(t *testing.T) {
	const (
		resultCap = 1024
		lastLine  = "line 200 of the report"
	)
	var report strings.Builder
	for line := 1; line <= 200; line++ {
		fmt.Fprintf(&report, "line %d of the report\n", line)
	}
	fake := newFakeOpenRouter(t)
	fake.queue(
		streamedToolCalls(scriptedToolCall{id: "call_read", name: "read_file", arguments: `{"path":"report.md"}`}),
		streamedText("The report has 200 lines."),
	)
	session := newSession(t, fake, workspaceWith(t, "report.md", report.String()), historymemory.New(),
		func(options *agent.SessionOptions) { options.MaximumToolResultBytes = resultCap })

	events := runToEnd(t, session, "How long is the report?")

	if reason := endReason(events); reason != agent.EndReasonStop {
		t.Fatalf("end reason = %q; want stop", reason)
	}
	full := toolResultEvent(t, events, "call_read")
	fullContent, _ := full.Content.(map[string]any)
	if content, _ := fullContent["content"].(string); !strings.Contains(content, lastLine) {
		t.Errorf("EventToolResult content lacks %q; want the full result", lastLine)
	}
	sent, ok := fake.chatRequests()[1].toolResult("call_read")
	if !ok {
		t.Fatal("the second request carries no read_file result")
	}
	if sent != full.ModelContent {
		t.Errorf("tool message = %q; want EventToolResult.ModelContent %q", sent, full.ModelContent)
	}
	var truncated struct {
		Truncated  bool `json:"truncated"`
		TotalBytes int  `json:"total_bytes"`
	}
	if len(sent) > resultCap || json.Unmarshal([]byte(sent), &truncated) != nil {
		t.Fatalf("tool message (%d bytes) is not JSON within %d bytes: %.300s", len(sent), resultCap, sent)
	}
	if !truncated.Truncated || truncated.TotalBytes <= resultCap || strings.Contains(sent, lastLine) {
		t.Errorf("tool message = %s; want truncated:true, total_bytes over the cap and the tail cut", sent)
	}
}

// queueReadThenAnswer scripts the model: read notes.md, then answer.
func queueReadThenAnswer(fake *fakeOpenRouter) {
	fake.queue(
		streamedToolCalls(scriptedToolCall{id: "call_read", name: "read_file", arguments: `{"path":"notes.md"}`}),
		streamedText("Read it."),
	)
}

func TestEveryRequestMarksTheSystemPromptAndTheLastMessageForPromptCaching(t *testing.T) {
	fake := newFakeOpenRouter(t)
	queueReadThenAnswer(fake)
	session := newSession(t, fake, workspaceWith(t, "notes.md", draftNotes), historymemory.New())

	if reason := endReason(runToEnd(t, session, "Read the notes.")); reason != agent.EndReasonStop {
		t.Fatalf("end reason = %q; want stop", reason)
	}
	requests := fake.chatRequests()
	if len(requests) != 2 {
		t.Fatalf("chat requests = %d; want 2", len(requests))
	}
	for number, request := range requests {
		var marked []string
		for _, message := range request.Messages {
			if message.cacheBreakpoint() {
				marked = append(marked, message.Role)
			}
		}
		last := request.Messages[len(request.Messages)-1]
		if len(marked) != 2 || request.Messages[0].Role != "system" || !request.Messages[0].cacheBreakpoint() || !last.cacheBreakpoint() {
			t.Errorf("request %d: breakpoints on %v; want exactly the system prompt and the last message (%s)", number+1, marked, last.Role)
		}
	}
}

func TestDisablePromptCachingSendsNoBreakpoints(t *testing.T) {
	fake := newFakeOpenRouter(t)
	queueReadThenAnswer(fake)
	session := newSession(t, fake, workspaceWith(t, "notes.md", draftNotes), historymemory.New(),
		func(options *agent.SessionOptions) { options.DisablePromptCaching = true })

	if reason := endReason(runToEnd(t, session, "Read the notes.")); reason != agent.EndReasonStop {
		t.Fatalf("end reason = %q; want stop", reason)
	}
	for number, request := range fake.chatRequests() {
		for _, message := range request.Messages {
			if message.cacheBreakpoint() {
				t.Errorf("request %d: %s message carries cache_control; want none", number+1, message.Role)
			}
		}
	}
}

func TestAToolCallOverItsTimeoutReachesTheModelAsTimedOutAndTheRunContinues(t *testing.T) {
	fake := newFakeOpenRouter(t)
	fake.queue(
		streamedToolCalls(scriptedToolCall{id: "call_wait", name: "wait_for_build", arguments: `{}`}),
		streamedText("The build is still running."),
	)
	session := newSession(t, fake, vfsmemory.New(), historymemory.New(), func(options *agent.SessionOptions) {
		options.ToolCallTimeout = 50 * time.Millisecond
		registerBlockingTool(t, options.Tools)
	})

	events := runToEnd(t, session, "Wait for the build.")

	if reason := endReason(events); reason != agent.EndReasonStop {
		t.Fatalf("end reason = %q; want stop, a timed-out tool does not end the run", reason)
	}
	if result := toolResultEvent(t, events, "call_wait"); result.OK || !strings.Contains(result.Error, "timed out") {
		t.Errorf("EventToolResult = %+v; want a failure that says timed out", result)
	}
	if sent, ok := fake.chatRequests()[1].toolResult("call_wait"); !ok || !strings.Contains(sent, "timed out") {
		t.Errorf("tool message sent to the model = %q (found %v); want the timed-out error", sent, ok)
	}
	if text := runText(events); text != "The build is still running." {
		t.Errorf("run text = %q; want the answer after the timeout", text)
	}
}

// registerBlockingTool adds wait_for_build, which returns only when its
// context ends.
func registerBlockingTool(t *testing.T, registry *tools.Registry) {
	t.Helper()
	err := registry.Register(llm.ToolDefinition{
		Name:        "wait_for_build",
		Description: "Wait until the build finishes.",
		Parameters:  tools.Object(map[string]any{}),
	}, func(ctx context.Context, _ json.RawMessage, _ tools.Environment) (tools.Result, error) {
		<-ctx.Done()
		return tools.Result{}, ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
}
