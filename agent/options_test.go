package agent

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
)

// toolMessage returns the content of the first tool message the run stored.
func toolMessage(t *testing.T, history History) string {
	t.Helper()
	for _, message := range storedMessages(t, history, "s-test") {
		if message.Role == llm.RoleTool {
			return message.Content
		}
	}
	t.Fatal("no tool message stored")
	return ""
}

// toolResultEvent returns the run's first EventToolResult.
func toolResultEvent(t *testing.T, events []Event) *ToolResult {
	t.Helper()
	for _, event := range events {
		if event.Kind == EventToolResult {
			return event.ToolResult
		}
	}
	t.Fatal("no EventToolResult")
	return nil
}

// runOneTool runs a session whose model calls the tool "work" once, then
// stops, and returns the run's events and its History.
func runOneTool(t *testing.T, handler tools.Handler, configure func(*SessionOptions)) ([]Event, History) {
	t.Helper()
	registry := tools.NewRegistry()
	mustRegister(t, registry, "work", handler)
	stub := llmtest.NewStubClient(toolCallTurn("c1", "work", "{}"), textTurn("done"))
	history := memory.NewHistory()
	session := newTestSession(t, stub, registry, history, configure)
	events := runToEnd(t, session, "go")
	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Fatalf("end reason = %q; want stop", reason)
	}
	return events, history
}

func TestSessionOptions_ToolCallTimeout(t *testing.T) {
	var remaining time.Duration
	recordDeadline := func(ctx context.Context, _ json.RawMessage, _ tools.Environment) (tools.Result, error) {
		deadline, _ := ctx.Deadline()
		remaining = time.Until(deadline)
		return tools.Result{Content: "ok"}, nil
	}

	t.Run("default is 2 minutes", func(t *testing.T) {
		runOneTool(t, recordDeadline, func(*SessionOptions) {})
		if remaining <= time.Minute || remaining > 2*time.Minute {
			t.Errorf("deadline in %v; want about 2 minutes", remaining)
		}
	})
	t.Run("negative disables the cap", func(t *testing.T) {
		runOneTool(t, recordDeadline, func(options *SessionOptions) { options.ToolCallTimeout = -1 })
		if remaining <= 5*time.Minute {
			t.Errorf("deadline in %v; want only the 10 minute RunTimeout", remaining)
		}
	})
	t.Run("expired call is a tool error and the run continues", func(t *testing.T) {
		waitForDeadline := func(ctx context.Context, _ json.RawMessage, _ tools.Environment) (tools.Result, error) {
			<-ctx.Done()
			return tools.Result{}, ctx.Err()
		}
		events, history := runOneTool(t, waitForDeadline, func(options *SessionOptions) { options.ToolCallTimeout = 20 * time.Millisecond })
		if result := toolResultEvent(t, events); result.OK || !strings.Contains(result.Error, "tool work timed out after 20ms") {
			t.Errorf("tool result = %+v; want the timeout", result)
		}
		if content := toolMessage(t, history); !strings.Contains(content, "timed out") {
			t.Errorf("tool message = %s; want the timeout", content)
		}
	})
}

func TestSessionOptions_MaximumToolResultBytes(t *testing.T) {
	large := strings.Repeat("a", 70_000)
	returnLarge := func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{Content: large}, nil
	}
	testCases := []struct {
		name          string
		maximumBytes  int
		wantTruncated bool
		wantAtMost    int
	}{
		{"default is 64 KiB", 0, true, 64 << 10},
		{"explicit cap", 200, true, 200},
		{"negative disables the cap", -1, false, len(large) + 2},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			events, history := runOneTool(t, returnLarge, func(options *SessionOptions) {
				options.MaximumToolResultBytes = testCase.maximumBytes
			})
			content := toolMessage(t, history)
			if len(content) > testCase.wantAtMost || !json.Valid([]byte(content)) {
				t.Errorf("tool message is %d bytes (valid JSON %v); want valid JSON of at most %d", len(content), json.Valid([]byte(content)), testCase.wantAtMost)
			}
			if truncated := strings.Contains(content, `"truncated":true`); truncated != testCase.wantTruncated {
				t.Errorf("truncated = %v; want %v", truncated, testCase.wantTruncated)
			}
			if result := toolResultEvent(t, events); result.Content != large {
				t.Error("EventToolResult must carry the full content")
			}
		})
	}
}

func TestBoundedJSON(t *testing.T) {
	testCases := []struct {
		name         string
		encoded      string
		maximumBytes int
		wantSame     bool // the result fits and is returned unchanged
		wantEmpty    bool // the cap leaves no room for content
	}{
		{"fits", `"short"`, 100, true, false},
		{"exact fit", `"12345"`, 7, true, false},
		{"no cap", strings.Repeat("x", 500), -1, true, false},
		{"cut", `"` + strings.Repeat("x", 500) + `"`, 200, false, false},
		{"quotes escape larger", strings.Repeat(`"`, 500), 200, false, false},
		{"multibyte boundary", strings.Repeat("ä", 300), 201, false, false},
		{"cap below the notice", strings.Repeat("x", 500), 10, false, true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := boundedJSON(testCase.encoded, testCase.maximumBytes)
			if testCase.wantSame {
				if got != testCase.encoded {
					t.Errorf("boundedJSON changed a fitting result: %s", got)
				}
				return
			}
			checkTruncationNotice(t, got, testCase.encoded, testCase.maximumBytes, testCase.wantEmpty)
		})
	}
}

// checkTruncationNotice checks that got is the notice for encoded under
// maximumBytes, holding the longest prefix that fits (none when wantEmpty).
func checkTruncationNotice(t *testing.T, got, encoded string, maximumBytes int, wantEmpty bool) {
	t.Helper()
	var notice truncatedResult
	if err := json.Unmarshal([]byte(got), &notice); err != nil {
		t.Fatalf("notice is not valid JSON: %v: %s", err, got)
	}
	if !notice.Truncated || notice.TotalBytes != len(encoded) || !strings.HasPrefix(encoded, notice.Content) {
		t.Errorf("notice = %+v; want truncated, total %d and a prefix of the result", notice, len(encoded))
	}
	if wantEmpty {
		if notice.Content != "" {
			t.Errorf("content = %q; want empty", notice.Content)
		}
		return
	}
	// One more character would add at most 6 escaped bytes and 1 digit.
	if len(got) > maximumBytes || len(got) <= maximumBytes-7 {
		t.Errorf("notice is %d bytes; want the longest prefix within %d", len(got), maximumBytes)
	}
}

// cacheMarks returns, per message, whether it carries a cache breakpoint.
func cacheMarks(messages []llm.Message) []bool {
	marks := make([]bool, len(messages))
	for index, message := range messages {
		marks[index] = message.Cache != nil
	}
	return marks
}

func TestSessionOptions_PromptCaching(t *testing.T) {
	run := func(t *testing.T, configure func(*SessionOptions)) ([]llm.Request, History) {
		t.Helper()
		registry := tools.NewRegistry()
		mustRegister(t, registry, "work", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
			return tools.Result{Content: "ok"}, nil
		})
		stub := llmtest.NewStubClient(toolCallTurn("c1", "work", "{}"), textTurn("done"))
		history := memory.NewHistory()
		runToEnd(t, newTestSession(t, stub, registry, history, configure), "go")
		return stub.Requests(), history
	}

	t.Run("default marks the system prompt and the last message", func(t *testing.T) {
		requests, history := run(t, func(*SessionOptions) {})
		// system, user | system, user, assistant tool call, tool result
		if got := cacheMarks(requests[0].Messages); !slices.Equal(got, []bool{true, true}) {
			t.Errorf("first request marks = %v", got)
		}
		if got := cacheMarks(requests[1].Messages); !slices.Equal(got, []bool{true, false, false, true}) {
			t.Errorf("second request marks = %v", got)
		}
		if ttl := requests[0].Messages[0].Cache.TTL; ttl != "" {
			t.Errorf("TTL = %q; want the default 5 minutes", ttl)
		}
		for _, message := range storedMessages(t, history, "s-test") {
			if message.Cache != nil {
				t.Errorf("breakpoint leaked into History: %+v", message)
			}
		}
	})
	t.Run("DisablePromptCaching sends none", func(t *testing.T) {
		requests, _ := run(t, func(options *SessionOptions) { options.DisablePromptCaching = true })
		for _, request := range requests {
			if got := cacheMarks(request.Messages); !slices.Equal(got, make([]bool, len(request.Messages))) {
				t.Errorf("marks = %v; want none", got)
			}
		}
	})
	t.Run("caller breakpoints win", func(t *testing.T) {
		stub := llmtest.NewStubClient(textTurn("ok"))
		session := newTestSession(t, stub, nil, nil)
		events, _ := session.RunWithMessages(context.Background(), []llm.Message{
			{Role: llm.RoleUser, Content: "long document", Cache: &llm.CacheBreakpoint{TTL: llm.CacheTTLOneHour}},
			{Role: llm.RoleAssistant, Content: "read it"},
			{Role: llm.RoleUser, Content: "summarize"},
		})
		collectEvents(t, events)
		messages := stub.Requests()[0].Messages
		if got := cacheMarks(messages); !slices.Equal(got, []bool{false, true, false, false}) {
			t.Errorf("marks = %v; want only the caller's", got)
		}
	})
}

func TestWithCacheBreakpoints(t *testing.T) {
	system := llm.Message{Role: llm.RoleSystem, Content: "rules"}
	user := llm.Message{Role: llm.RoleUser, Content: "hi"}
	toolCalls := llm.Message{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "c"}}}
	image := llm.Message{Role: llm.RoleUser, Images: []llm.ImagePart{{URL: "https://example.com/a.png"}}}
	testCases := []struct {
		name     string
		messages []llm.Message
		want     []bool
	}{
		{"empty", nil, []bool{}},
		{"system only", []llm.Message{system}, []bool{true}},
		{"no system", []llm.Message{user, user}, []bool{false, true}},
		{"tool-call tail is skipped", []llm.Message{system, user, toolCalls}, []bool{true, false, false}},
		{"image tail", []llm.Message{system, image}, []bool{true, true}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := withCacheBreakpoints(testCase.messages)
			if marks := cacheMarks(got); !slices.Equal(marks, testCase.want) {
				t.Errorf("marks = %v; want %v", marks, testCase.want)
			}
			if slices.Contains(cacheMarks(testCase.messages), true) {
				t.Error("withCacheBreakpoints modified its input")
			}
		})
	}
}
