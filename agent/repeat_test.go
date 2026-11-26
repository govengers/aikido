package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
)

// repeatingTurns scripts count replies that each call name with the same
// arguments, then a text answer.
func repeatingTurns(count int, name, arguments, answer string) []llmtest.TurnScript {
	turns := make([]llmtest.TurnScript, 0, count+1)
	for range count {
		turns = append(turns, toolCallTurn("c", name, arguments))
	}
	return append(turns, textTurn(answer))
}

func countingRegistry(t *testing.T, ran *int, result func(int) string) *tools.Registry {
	t.Helper()
	registry := tools.NewRegistry()
	mustRegister(t, registry, "read", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		*ran++
		return tools.Result{Content: result(*ran)}, nil
	})
	return registry
}

func toolResults(events []Event) []ToolResult {
	var results []ToolResult
	for _, event := range events {
		if event.Kind == EventToolResult {
			results = append(results, *event.ToolResult)
		}
	}
	return results
}

func TestRun_RepeatedToolCallIsRefusedAfterTheLimit(t *testing.T) {
	ran := 0
	registry := countingRegistry(t, &ran, func(int) string { return "same" })
	// Key order and spacing differ: still the same call.
	stub := llmtest.NewStubClient(
		toolCallTurn("c1", "read", `{"path":"a","limit":5}`),
		toolCallTurn("c2", "read", `{"limit": 5, "path": "a"}`),
		toolCallTurn("c3", "read", `{"path":"a","limit":5}`),
		toolCallTurn("c4", "read", `{"path":"a","limit":5}`),
		textTurn("done"),
	)
	session := newTestSession(t, stub, registry, nil, func(options *SessionOptions) { options.MaximumTurns = 10 })

	events := runToEnd(t, session, "go")

	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Fatalf("end reason = %q; want stop", reason)
	}
	if ran != 3 {
		t.Errorf("tool ran %d times; want 3 (the 4th identical call is refused)", ran)
	}
	results := toolResults(events)
	if len(results) != 4 || results[3].OK || !strings.Contains(results[3].Error, ErrRepeatedToolCall.Error()) {
		t.Fatalf("4th result = %+v; want a repeated-call refusal", results[len(results)-1])
	}
}

func TestRun_RepeatedCallWithChangingResultIsNotRefused(t *testing.T) {
	ran := 0
	registry := countingRegistry(t, &ran, func(n int) string { return strings.Repeat("x", n) })
	session := newTestSession(t, llmtest.NewStubClient(repeatingTurns(6, "read", `{}`, "done")...), registry, nil,
		func(options *SessionOptions) { options.MaximumTurns = 10 })

	events := runToEnd(t, session, "poll")

	if ran != 6 {
		t.Errorf("tool ran %d times; want 6: a changing result is progress", ran)
	}
	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop", reason)
	}
}

func TestRun_AModelThatKeepsRepeatingGetsTheWrapUpAndEnds(t *testing.T) {
	ran := 0
	registry := countingRegistry(t, &ran, func(int) string { return "same" })
	// 3 runs + 3 refusals, then the wrap-up call answers.
	stub := llmtest.NewStubClient(append(repeatingTurns(6, "read", `{"path":"a"}`, "")[:6], textTurn("what I found"))...)
	history := memory.New()
	session := newTestSession(t, stub, registry, history, func(options *SessionOptions) { options.MaximumTurns = 30 })

	events := runToEnd(t, session, "go")

	if reason := endReasonOf(t, events); reason != EndReasonRepeatedToolCalls {
		t.Fatalf("end reason = %q; want repeated_tool_calls", reason)
	}
	if ran != 3 {
		t.Errorf("tool ran %d times; want 3", ran)
	}
	stored := storedMessages(t, history, "s-test")
	if last := stored[len(stored)-1]; last.Role != llm.RoleAssistant || last.Content != "what I found" {
		t.Errorf("last stored message = %+v; want the wrap-up answer", last)
	}
}

func TestRun_RepeatedToolCallLimitNegativeTurnsTheGuardOff(t *testing.T) {
	ran := 0
	registry := countingRegistry(t, &ran, func(int) string { return "same" })
	session := newTestSession(t, llmtest.NewStubClient(repeatingTurns(6, "read", `{}`, "done")...), registry, nil,
		func(options *SessionOptions) { options.MaximumTurns = 10; options.RepeatedToolCallLimit = -1 })

	runToEnd(t, session, "go")

	if ran != 6 {
		t.Errorf("tool ran %d times; want 6 without a limit", ran)
	}
}

func TestRun_WrapUpAnswersWithoutToolsAtMaximumTurns(t *testing.T) {
	ran := 0
	registry := countingRegistry(t, &ran, func(n int) string { return strings.Repeat("y", n) })
	stub := llmtest.NewStubClient(
		toolCallTurn("c1", "read", `{"n":1}`),
		toolCallTurn("c2", "read", `{"n":2}`),
		// The wrap-up reply still asks for a tool: the call is dropped, the text kept.
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "partial answer"},
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c3", Name: "read", Arguments: `{}`}},
			{Kind: llm.EventEnd},
		}},
	)
	history := memory.New()
	session := newTestSession(t, stub, registry, history, func(options *SessionOptions) {
		options.MaximumTurns = 2
		options.WrapUpPrompt = "Out of %d calls. Answer."
	})

	events := runToEnd(t, session, "go")

	if reason := endReasonOf(t, events); reason != EndReasonMaximumTurns {
		t.Fatalf("end reason = %q; want max_turns", reason)
	}
	if ran != 2 {
		t.Errorf("tool ran %d times; want 2: the wrap-up runs no tool", ran)
	}
	requests := stub.Requests()
	last := requests[len(requests)-1].Messages
	if prompt := last[len(last)-1]; prompt.Role != llm.RoleUser || prompt.Content != "Out of 2 calls. Answer." {
		t.Errorf("wrap-up request ends with %+v; want the wrap-up prompt", prompt)
	}
	stored := storedMessages(t, history, "s-test")
	if answer := stored[len(stored)-1]; answer.Content != "partial answer" || len(answer.ToolCalls) != 0 {
		t.Errorf("stored answer = %+v; want the text without tool calls", answer)
	}
}

func TestRun_AnEmptyWrapUpLeavesNoUnansweredPrompt(t *testing.T) {
	registry := countingRegistry(t, new(int), func(n int) string { return strings.Repeat("z", n) })
	stub := llmtest.NewStubClient(toolCallTurn("c1", "read", `{}`), toolCallTurn("c2", "read", `{"again":true}`))
	history := memory.New()
	session := newTestSession(t, stub, registry, history, func(options *SessionOptions) { options.MaximumTurns = 1 })

	runToEnd(t, session, "go")

	stored := storedMessages(t, history, "s-test")
	if last := stored[len(stored)-1]; last.Role != llm.RoleTool {
		t.Errorf("last stored message = %+v; want the tool result, not the wrap-up prompt", last)
	}
}

func TestRun_DisableWrapUpEndsAtTheLimit(t *testing.T) {
	registry := countingRegistry(t, new(int), func(n int) string { return strings.Repeat("z", n) })
	stub := llmtest.NewStubClient(toolCallTurn("c1", "read", `{}`), textTurn("never asked"))
	session := newTestSession(t, stub, registry, nil, func(options *SessionOptions) { options.MaximumTurns = 1; options.DisableWrapUp = true })

	runToEnd(t, session, "go")

	if calls := len(stub.Requests()); calls != 1 {
		t.Errorf("model calls = %d; want 1 without wrap-up", calls)
	}
}

func TestRun_AnAlternatingCycleIsRefused(t *testing.T) {
	ran := map[string]int{}
	registry := tools.NewRegistry()
	mustRegister(t, registry, "read", func(_ context.Context, arguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		ran[string(arguments)]++
		return tools.Result{Text: "same for " + string(arguments)}, nil
	})
	turns := []llmtest.TurnScript{}
	for range 4 {
		turns = append(turns, toolCallTurn("a", "read", `{"path":"a"}`), toolCallTurn("b", "read", `{"path":"b"}`))
	}
	session := newTestSession(t, llmtest.NewStubClient(append(turns, textTurn("done"))...), registry, nil,
		func(options *SessionOptions) { options.MaximumTurns = 20 })

	events := runToEnd(t, session, "go")

	// a, b, a, b, a, b ran: the 7th (a) and 8th (b) calls are refused as members of the cycle.
	if ran[`{"path":"a"}`] != 3 || ran[`{"path":"b"}`] != 3 {
		t.Errorf("ran = %v; want each 3 times", ran)
	}
	results := toolResults(events)
	for _, refused := range results[6:8] {
		if refused.OK || !strings.Contains(refused.Error, "repeated the same 2 calls") {
			t.Errorf("result = %+v; want a cycle refusal", refused)
		}
	}
}

func TestRun_EditThenTestWithTheSameOutputIsNoCycle(t *testing.T) {
	ran := 0
	registry := tools.NewRegistry()
	mustRegister(t, registry, "edit", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{Text: "edited"}, nil
	})
	mustRegister(t, registry, "test", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		ran++
		return tools.Result{Text: "FAIL"}, nil
	})
	turns := []llmtest.TurnScript{}
	for attempt := range 5 {
		turns = append(turns, toolCallTurn("e", "edit", `{"try":`+string(rune('0'+attempt))+`}`), toolCallTurn("t", "test", `{}`))
	}
	session := newTestSession(t, llmtest.NewStubClient(append(turns, textTurn("done"))...), registry, nil,
		func(options *SessionOptions) { options.MaximumTurns = 20 })

	runToEnd(t, session, "fix it")

	if ran != 5 {
		t.Errorf("test ran %d times; want 5: each edit differs, so there is no cycle", ran)
	}
}

func TestRepeatGuardRefusalWrapsTheSentinel(t *testing.T) {
	guard := newRepeatGuard(1)
	call := llm.ToolCall{Name: "read", Arguments: `not json`}
	guard.observe(call, "r")
	if err := guard.refusal(call); !errors.Is(err, ErrRepeatedToolCall) {
		t.Errorf("refusal = %v; want ErrRepeatedToolCall", err)
	}
}
