package agent

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
)

// testTimeout bounds waits that only expire when the code under test is broken.
const testTimeout = 5 * time.Second

func newTestSession(t *testing.T, client llm.Client, registry *tools.Registry, history History, configure ...func(*SessionOptions)) *Session {
	t.Helper()
	if history == nil {
		history = memory.New()
	}
	options := &SessionOptions{
		ID:           "s-test",
		Client:       client,
		Tools:        registry,
		History:      history,
		Locker:       NewLocalLocker(),
		Model:        "test/model",
		SystemPrompt: "you are a test",
		MaximumTurns: 5,
	}
	for _, apply := range configure {
		apply(options)
	}
	session, err := NewSession(options)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	return session
}

func collectEvents(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var collected []Event
	for event := range events {
		collected = append(collected, event)
	}
	return collected
}

// runToEnd runs userText on session and returns every event of the run.
func runToEnd(t *testing.T, session *Session, userText string) []Event {
	t.Helper()
	events, err := session.Run(context.Background(), userText)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return collectEvents(t, events)
}

// endReasonOf returns the EndReason of the run's EventEnd and fails the test
// when the run has no EventEnd or it is not the last event.
func endReasonOf(t *testing.T, events []Event) EndReason {
	t.Helper()
	if len(events) == 0 || events[len(events)-1].Kind != EventEnd {
		t.Fatalf("run did not end with EventEnd: %+v", events)
	}
	return events[len(events)-1].EndReason
}

// errorOf returns the error of the run's EventError, nil if there is none.
func errorOf(events []Event) error {
	for _, event := range events {
		if event.Kind == EventError {
			return event.Err
		}
	}
	return nil
}

func mustRegister(t *testing.T, registry *tools.Registry, name string, handler tools.Handler) {
	t.Helper()
	if err := registry.Register(llm.ToolDefinition{Name: name}, handler); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

func storedMessages(t *testing.T, history History, sessionID string) []llm.Message {
	t.Helper()
	messages, err := history.Read(context.Background(), sessionID)
	if err != nil {
		t.Fatalf("History.Read: %v", err)
	}
	return messages
}

func textTurn(text string) llmtest.TurnScript {
	return llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventTextDelta, Text: text}, {Kind: llm.EventEnd}}}
}

func toolCallTurn(callID, name, arguments string) llmtest.TurnScript {
	return llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: callID, Name: name, Arguments: arguments}},
		{Kind: llm.EventEnd},
	}}
}

func TestRun_HappyPath_TextOnly(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "Hello "},
		{Kind: llm.EventTextDelta, Text: "world"},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 5, CompletionTokens: 3}},
		{Kind: llm.EventEnd},
	}})
	history := memory.New()
	session := newTestSession(t, stub, nil, history)

	events := runToEnd(t, session, "say hi")

	var text strings.Builder
	var sawUsage bool
	for _, event := range events {
		switch event.Kind {
		case EventText:
			text.WriteString(event.Text)
		case EventUsage:
			sawUsage = true
		default:
		}
	}
	if text.String() != "Hello world" {
		t.Errorf("text = %q; want %q", text.String(), "Hello world")
	}
	if !sawUsage {
		t.Error("missing EventUsage")
	}
	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop", reason)
	}

	// History flushed once: user + assistant.
	stored := storedMessages(t, history, "s-test")
	if len(stored) != 2 {
		t.Fatalf("len(stored) = %d; want 2 (user+assistant)", len(stored))
	}
	if stored[0].Role != llm.RoleUser || stored[0].Content != "say hi" {
		t.Errorf("stored[0] = %+v", stored[0])
	}
	if stored[1].Role != llm.RoleAssistant || stored[1].Content != "Hello world" {
		t.Errorf("stored[1] = %+v", stored[1])
	}
}

func TestRun_ToolCallThenEnd(t *testing.T) {
	registry := tools.NewRegistry()
	mustRegister(t, registry, "echo", func(_ context.Context, arguments json.RawMessage, _ tools.Environment) (tools.Result, error) {
		var parsed map[string]any
		if err := json.Unmarshal(arguments, &parsed); err != nil {
			return tools.Result{}, err
		}
		return tools.Result{Content: map[string]any{"echoed": parsed["msg"]}}, nil
	})
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "thinking..."},
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "echo", Arguments: `{"msg":"hi"}`}},
			{Kind: llm.EventEnd},
		}},
		textTurn("done"),
	)
	history := memory.New()
	session := newTestSession(t, stub, registry, history)

	events := runToEnd(t, session, "do it")

	var sawToolCall, sawToolResult bool
	for _, event := range events {
		switch event.Kind {
		case EventToolCall:
			sawToolCall = true
			if event.ToolCall == nil || event.ToolCall.Name != "echo" {
				t.Errorf("tool call wrong: %+v", event.ToolCall)
			}
		case EventToolResult:
			sawToolResult = true
			if !event.ToolResult.OK {
				t.Errorf("tool result not OK: %+v", event.ToolResult)
			}
		default:
		}
	}
	if !sawToolCall || !sawToolResult {
		t.Errorf("tool events: call=%v result=%v", sawToolCall, sawToolResult)
	}
	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop", reason)
	}

	// One History.Append (variadic): user + assistant 1 + tool + assistant 2.
	stored := storedMessages(t, history, "s-test")
	if len(stored) != 4 {
		t.Errorf("stored len = %d; want 4 (user + asst1 + tool + asst2). messages=%+v", len(stored), stored)
	}
	if want := `{"echoed":"hi"}`; stored[2].Role != llm.RoleTool || stored[2].Content != want {
		t.Errorf("tool message = %+v; want content %s", stored[2], want)
	}
}

func TestRun_ToolErrorContinues(t *testing.T) {
	registry := tools.NewRegistry()
	sentinel := errors.New("tool exploded")
	mustRegister(t, registry, "boom", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{}, sentinel
	})
	stub := llmtest.NewStubClient(toolCallTurn("c1", "boom", ""), textTurn("i recovered"))
	session := newTestSession(t, stub, registry, nil)

	events := runToEnd(t, session, "go")

	var sawFailedResult bool
	for _, event := range events {
		if event.Kind == EventToolResult && event.ToolResult != nil && !event.ToolResult.OK {
			sawFailedResult = true
			if !strings.Contains(event.ToolResult.Error, "exploded") {
				t.Errorf("tool error = %q", event.ToolResult.Error)
			}
		}
	}
	if !sawFailedResult {
		t.Error("expected EventToolResult with OK=false")
	}
	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop (loop continues despite tool error)", reason)
	}
}

func TestRun_UnknownToolWithoutRegistryContinues(t *testing.T) {
	stub := llmtest.NewStubClient(toolCallTurn("c1", "ghost", "{}"), textTurn("ok"))
	session := newTestSession(t, stub, nil, nil)

	events := runToEnd(t, session, "go")

	var toolResult *ToolResult
	for _, event := range events {
		if event.Kind == EventToolResult {
			toolResult = event.ToolResult
		}
	}
	if toolResult == nil || toolResult.OK || !strings.Contains(toolResult.Error, "ghost") {
		t.Errorf("tool result = %+v; want a failed result naming the unknown tool", toolResult)
	}
	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop", reason)
	}
}

func TestRun_MidStreamErrorSkipsHistoryAppend(t *testing.T) {
	providerError := errors.New("provider blew up")
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "partial"},
		{Kind: llm.EventError, Err: providerError},
		{Kind: llm.EventEnd},
	}})
	history := memory.New()
	session := newTestSession(t, stub, nil, history)

	events := runToEnd(t, session, "go")

	if err := errorOf(events); !errors.Is(err, providerError) {
		t.Errorf("EventError = %v; want the provider error", err)
	}
	if reason := endReasonOf(t, events); reason != EndReasonError {
		t.Errorf("end reason = %q; want error", reason)
	}
	if stored := storedMessages(t, history, "s-test"); len(stored) != 0 {
		t.Errorf("History.Append should NOT have been called on mid-stream error; stored=%+v", stored)
	}
}

func TestRun_MaximumTurnsHitsCap(t *testing.T) {
	// Always emit a tool call so the loop never reaches stop.
	registry := tools.NewRegistry()
	mustRegister(t, registry, "loop", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{Content: "ok"}, nil
	})
	turns := make([]llmtest.TurnScript, 10)
	for index := range turns {
		turns[index] = toolCallTurn("c", "loop", "")
	}
	stub := llmtest.NewStubClient(turns...)
	history := memory.New()
	session := newTestSession(t, stub, registry, history, func(options *SessionOptions) { options.MaximumTurns = 3 })

	events := runToEnd(t, session, "go")

	if reason := endReasonOf(t, events); reason != EndReasonMaximumTurns {
		t.Errorf("end reason = %q; want max_turns", reason)
	}
	// History flushed: user + 3 turns of (assistant + tool) = 1 + 6 = 7 messages.
	if stored := storedMessages(t, history, "s-test"); len(stored) != 7 {
		t.Errorf("stored len = %d; want 7", len(stored))
	}
}

func TestRun_RunTimeout(t *testing.T) {
	session := newTestSession(t, blockingClient{}, nil, nil, func(options *SessionOptions) {
		options.RunTimeout = 80 * time.Millisecond
		options.LLMCallTimeout = 10 * time.Second // make sure the outer timeout fires first
	})
	if reason := endReasonOf(t, runToEnd(t, session, "go")); reason != EndReasonTimeout {
		t.Errorf("end reason = %q; want timeout", reason)
	}
}

func TestRun_LLMCallTimeout(t *testing.T) {
	session := newTestSession(t, blockingClient{}, nil, nil, func(options *SessionOptions) {
		options.RunTimeout = 5 * time.Second
		options.LLMCallTimeout = 80 * time.Millisecond
	})
	if reason := endReasonOf(t, runToEnd(t, session, "go")); reason != EndReasonTimeout {
		t.Errorf("end reason = %q; want timeout", reason)
	}
}

func TestRun_Cancellation(t *testing.T) {
	streaming := make(chan struct{})
	session := newTestSession(t, blockingClient{streaming: streaming}, nil, nil, func(options *SessionOptions) {
		options.RunTimeout = 5 * time.Second
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events, _ := session.Run(ctx, "go")
	<-streaming
	cancel()

	if reason := endReasonOf(t, collectEvents(t, events)); reason != EndReasonCancelled {
		t.Errorf("end reason = %q; want cancelled", reason)
	}
}

func TestRun_StrictHistoryReadError(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("hi"))
	sentinel := errors.New("read boom")
	session := newTestSession(t, stub, nil, errorHistory{readError: sentinel})

	events := runToEnd(t, session, "go")

	if err := errorOf(events); !errors.Is(err, sentinel) {
		t.Errorf("EventError = %v; want the History error", err)
	}
	if reason := endReasonOf(t, events); reason != EndReasonError {
		t.Errorf("end reason = %q; want error", reason)
	}
}

func TestRun_StrictHistoryAppendError(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("hi"))
	sentinel := errors.New("append boom")
	session := newTestSession(t, stub, nil, errorHistory{appendError: sentinel})

	events := runToEnd(t, session, "go")

	if err := errorOf(events); !errors.Is(err, sentinel) {
		t.Errorf("EventError = %v; want the History error", err)
	}
	if reason := endReasonOf(t, events); reason != EndReasonError {
		t.Errorf("end reason = %q; want error", reason)
	}
}

func TestRun_StrictLockerError(t *testing.T) {
	sentinel := errors.New("lock boom")
	session := newTestSession(t, llmtest.NewStubClient(), nil, nil, func(options *SessionOptions) {
		options.Locker = errorLocker{err: sentinel}
	})

	events := runToEnd(t, session, "go")

	if err := errorOf(events); !errors.Is(err, sentinel) {
		t.Errorf("EventError = %v; want the Locker error", err)
	}
	if reason := endReasonOf(t, events); reason != EndReasonError {
		t.Errorf("end reason = %q; want error", reason)
	}
}

func TestRun_StreamDialError(t *testing.T) {
	sentinel := errors.New("dial fail")
	history := memory.New()
	session := newTestSession(t, dialErrorClient{err: sentinel}, nil, history)

	events := runToEnd(t, session, "go")

	if err := errorOf(events); !errors.Is(err, sentinel) {
		t.Errorf("EventError = %v; want the dial error", err)
	}
	if reason := endReasonOf(t, events); reason != EndReasonError {
		t.Errorf("end reason = %q; want error", reason)
	}
	if stored := storedMessages(t, history, "s-test"); len(stored) != 0 {
		t.Error("History should not be appended on dial error")
	}
}

// A panicking tool handler is a tool error the model sees; the run goes on.
func TestRun_ToolPanicIsAToolError(t *testing.T) {
	registry := tools.NewRegistry()
	mustRegister(t, registry, "explode", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		panic("tool bug")
	})
	stub := llmtest.NewStubClient(toolCallTurn("c1", "explode", "{}"), textTurn("after the panic"))
	history := memory.New()
	session := newTestSession(t, stub, registry, history)

	events := runToEnd(t, session, "go")

	if reason := endReasonOf(t, events); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop", reason)
	}
	stored := storedMessages(t, history, "s-test")
	if len(stored) != 4 || !strings.Contains(stored[2].Content, "tool panicked: explode: tool bug") {
		t.Errorf("stored = %+v; want the panic as the tool message", stored)
	}
}

// panickingClient panics in Stream, a stand-in for any bug inside the run.
type panickingClient struct{ dialErrorClient }

func (panickingClient) Stream(context.Context, llm.Request) (<-chan llm.Event, error) {
	panic("client bug")
}

// Regression: a panic inside the run used to crash the caller's process,
// because the run goroutine did not recover (CON-001).
func TestRun_PanicEndsRunWithError(t *testing.T) {
	history := memory.New()
	session := newTestSession(t, panickingClient{}, nil, history)

	events := runToEnd(t, session, "go")

	if err := errorOf(events); err == nil || !strings.Contains(err.Error(), "client bug") {
		t.Errorf("EventError = %v; want the recovered panic", err)
	}
	if reason := endReasonOf(t, events); reason != EndReasonError {
		t.Errorf("end reason = %q; want error", reason)
	}
	if stored := storedMessages(t, history, "s-test"); len(stored) != 0 {
		t.Errorf("History must not be updated after a panic; stored=%+v", stored)
	}
	// The panic released the session lock: the next run gets it and fails the same way.
	if reason := endReasonOf(t, runToEnd(t, session, "again")); reason != EndReasonError {
		t.Errorf("next run end reason = %q; want error", reason)
	}
}

// Regression: a caller that cancelled and stopped reading used to leave the
// run goroutine blocked on a full channel forever, holding the session lock
// (CON-005).
func TestRun_AbandonedRunReleasesTheLock(t *testing.T) {
	floodEvents := make([]llm.Event, 0, 2*eventBufferSize)
	for range 2 * eventBufferSize {
		floodEvents = append(floodEvents, llm.Event{Kind: llm.EventTextDelta, Text: "x"})
	}
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: floodEvents}, textTurn("second"))
	session := newTestSession(t, stub, nil, nil)

	abandonedContext, cancel := context.WithCancel(context.Background())
	abandoned, _ := session.Run(abandonedContext, "flood")
	for len(abandoned) < cap(abandoned) {
		runtime.Gosched() // wait until the run is blocked on the full channel
	}
	cancel()

	ctx, cancelSecond := context.WithTimeout(context.Background(), testTimeout)
	defer cancelSecond()
	second, _ := session.Run(ctx, "next")
	if reason := endReasonOf(t, collectEvents(t, second)); reason != EndReasonStop {
		t.Errorf("run after an abandoned run ended with %q; want stop", reason)
	}
	collectEvents(t, abandoned)
}

func TestRun_LocalLockerSerializesSameIDAcrossSessions(t *testing.T) {
	locker := NewLocalLocker()
	history := memory.New()
	newSharedSession := func(client llm.Client) *Session {
		return newTestSession(t, client, nil, history, func(options *SessionOptions) {
			options.ID = "shared"
			options.Locker = locker
			options.MaximumTurns = 1
		})
	}
	firstStreaming := make(chan struct{}, 1)
	releaseFirst := make(chan struct{})
	first := newSharedSession(gatedClient{streaming: firstStreaming, release: releaseFirst})
	second := newSharedSession(llmtest.NewStubClient(textTurn("x")))

	firstEvents, _ := first.Run(context.Background(), "first")
	<-firstStreaming // first holds the lock now
	secondEvents, _ := second.Run(context.Background(), "second")
	close(releaseFirst)

	if reason := endReasonOf(t, collectEvents(t, firstEvents)); reason != EndReasonStop {
		t.Errorf("first run ended with %q", reason)
	}
	if reason := endReasonOf(t, collectEvents(t, secondEvents)); reason != EndReasonStop {
		t.Errorf("second run ended with %q", reason)
	}
	// Serialized: the second run read History only after the first appended.
	stored := storedMessages(t, history, "shared")
	if len(stored) != 4 || stored[0].Content != "first" || stored[2].Content != "second" {
		t.Errorf("stored = %+v; want first's user+assistant, then second's", stored)
	}
}

func TestRun_LocalLockerRunsDifferentIDsInParallel(t *testing.T) {
	locker := NewLocalLocker()
	streaming := make(chan struct{}, 2)
	release := make(chan struct{})
	client := gatedClient{streaming: streaming, release: release}
	sessions := make([]*Session, 0, 2)
	for _, id := range []string{"a", "b"} {
		sessions = append(sessions, newTestSession(t, client, nil, nil, func(options *SessionOptions) {
			options.ID = id
			options.Locker = locker
		}))
	}

	var runs sync.WaitGroup
	collected := make([][]Event, len(sessions))
	for index, session := range sessions {
		events, _ := session.Run(context.Background(), "go")
		runs.Go(func() {
			collected[index] = collectEvents(t, events)
		})
	}
	for range sessions {
		select {
		case <-streaming:
		case <-time.After(testTimeout):
			t.Fatal("both sessions should stream at the same time")
		}
	}
	close(release)
	runs.Wait()
	for index, events := range collected {
		if reason := endReasonOf(t, events); reason != EndReasonStop {
			t.Errorf("session %d ended with %q", index, reason)
		}
	}
}

func TestNewLocalSession_AutoSuppliesDefaults(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("hi"))
	session, err := NewLocalSession(&SessionOptions{ID: "x", Client: stub, Model: "m"})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	if session.options.History == nil {
		t.Error("History should be auto-supplied")
	}
	if session.options.Locker == nil {
		t.Error("Locker should be auto-supplied")
	}
	if reason := endReasonOf(t, runToEnd(t, session, "say hi")); reason != EndReasonStop {
		t.Errorf("end reason = %q; want stop", reason)
	}
}

func TestNewLocalSession_RespectsProvided(t *testing.T) {
	customHistory := memory.New()
	customLocker := NewLocalLocker()
	stub := llmtest.NewStubClient()
	session, err := NewLocalSession(&SessionOptions{
		ID: "x", Client: stub, Model: "m",
		History: customHistory, Locker: customLocker,
	})
	if err != nil {
		t.Fatalf("NewLocalSession: %v", err)
	}
	if session.options.History != customHistory {
		t.Error("custom History was overwritten")
	}
	if session.options.Locker != customLocker {
		t.Error("custom Locker was overwritten")
	}
}

func TestNewLocalSession_NilOptions(t *testing.T) {
	if _, err := NewLocalSession(nil); err == nil {
		t.Error("expected an error for nil options")
	}
}

func TestRunWithOptions_SystemSuffixIsPerRun(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("ok"), textTurn("ok"))
	history := memory.New()
	session := newTestSession(t, stub, nil, history)

	events, _ := session.RunWithOptions(context.Background(), "one", &RunOptions{SystemSuffix: "Answer briefly."})
	collectEvents(t, events)
	runToEnd(t, session, "two")

	requests := stub.Requests()
	if got := requests[0].Messages[0].Content; got != "you are a test\n\nAnswer briefly." {
		t.Errorf("first system = %q", got)
	}
	if got := requests[1].Messages[0].Content; got != "you are a test" {
		t.Errorf("second system = %q", got)
	}
	for _, message := range storedMessages(t, history, "s-test") {
		if message.Role == llm.RoleSystem || strings.Contains(message.Content, "Answer briefly") {
			t.Errorf("suffix leaked into history: %+v", message)
		}
	}
}

func TestSessionOptions_ThinkingReachesEveryCall(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("ok"))
	session := newTestSession(t, stub, nil, nil, func(options *SessionOptions) {
		options.Thinking = llm.ThinkingByEffort(llm.ThinkingEffortLow)
	})
	runToEnd(t, session, "hi")
	if got := stub.Requests()[0].Thinking.Effort(); got != llm.ThinkingEffortLow {
		t.Fatalf("effort = %q, want low", got)
	}
}

func TestRunOptions_ThinkingOverridesSession(t *testing.T) {
	stub := llmtest.NewStubClient(textTurn("ok"), textTurn("ok"))
	session := newTestSession(t, stub, nil, nil, func(options *SessionOptions) {
		options.Thinking = llm.ThinkingByEffort(llm.ThinkingEffortLow)
	})
	events, _ := session.RunWithOptions(context.Background(), "hard one", &RunOptions{Thinking: llm.ThinkingByEffort(llm.ThinkingEffortHigh)})
	collectEvents(t, events)
	runToEnd(t, session, "easy one")

	requests := stub.Requests()
	if got := requests[0].Thinking.Effort(); got != llm.ThinkingEffortHigh {
		t.Errorf("run with override: effort = %q, want high", got)
	}
	if got := requests[1].Thinking.Effort(); got != llm.ThinkingEffortLow {
		t.Errorf("next run: effort = %q, want the session's low", got)
	}
}

func TestRun_ForwardsThinkingAndImages(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventThinking, Text: "pondering"},
		{Kind: llm.EventImage, Image: &llm.ImagePart{URL: "https://example.com/a.png"}},
		{Kind: llm.EventImage},
		{Kind: llm.EventToolCall},
		{Kind: llm.EventEnd},
	}})
	history := memory.New()
	session := newTestSession(t, stub, nil, history)

	events := runToEnd(t, session, "draw")

	var thinking string
	var images int
	for _, event := range events {
		switch event.Kind {
		case EventThinking:
			thinking += event.Text
		case EventImage:
			images++
		default:
		}
	}
	if thinking != "pondering" || images != 1 {
		t.Errorf("thinking = %q, images = %d; want pondering and 1 image", thinking, images)
	}
	stored := storedMessages(t, history, "s-test")
	if len(stored) != 2 || len(stored[1].Images) != 1 || len(stored[1].ToolCalls) != 0 {
		t.Errorf("stored = %+v; want the image on the assistant message and no tool calls", stored)
	}
}

func TestSystemPrompt(t *testing.T) {
	testCases := []struct {
		name, prompt, suffix, want string
	}{
		{"neither", "", "", ""},
		{"prompt only", "be kind", "", "be kind"},
		{"suffix only", "", "be brief", "be brief"},
		{"both joined by a blank line", "be kind", "be brief", "be kind\n\nbe brief"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := systemPrompt(testCase.prompt, testCase.suffix); got != testCase.want {
				t.Errorf("systemPrompt(%q, %q) = %q; want %q", testCase.prompt, testCase.suffix, got, testCase.want)
			}
		})
	}
}

func TestResolveEndReason(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancelExpired()
	contexts := map[string]context.Context{
		"cancelled": cancelled,
		"expired":   expired,
		"live":      context.Background(),
		"nil":       nil,
	}

	testCases := []struct {
		name             string
		userContext      string
		runContext       string
		callContextError error
		want             EndReason
	}{
		{"caller cancelled wins", "cancelled", "expired", context.DeadlineExceeded, EndReasonCancelled},
		{"caller deadline", "expired", "expired", nil, EndReasonTimeout},
		{"run timeout", "live", "expired", nil, EndReasonTimeout},
		{"call timeout", "live", "live", context.DeadlineExceeded, EndReasonTimeout},
		{"call cancelled", "live", "live", context.Canceled, EndReasonCancelled},
		{"plain failure", "live", "live", nil, EndReasonError},
		{"nil contexts", "nil", "nil", nil, EndReasonError},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := resolveEndReason(contexts[testCase.userContext], contexts[testCase.runContext], testCase.callContextError)
			if got != testCase.want {
				t.Errorf("resolveEndReason = %q; want %q", got, testCase.want)
			}
		})
	}
}

func TestToolResultJSON(t *testing.T) {
	testCases := []struct {
		name       string
		toolResult ToolResult
		want       string
	}{
		{"content", ToolResult{OK: true, Content: map[string]any{"a": 1}}, `{"a":1}`},
		{"nil content", ToolResult{OK: true}, "null"},
		{"failure", ToolResult{Error: `bad "path"`}, `{"error":"bad \"path\""}`},
		{"unencodable content", ToolResult{OK: true, Content: make(chan int)}, `{"error":"content marshal failed: json: unsupported type: chan int"}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := toolResultJSON(testCase.toolResult); got != testCase.want {
				t.Errorf("toolResultJSON = %s; want %s", got, testCase.want)
			}
		})
	}
}

func TestEventSink_DropsOnlyWhenAbandonedAndFull(t *testing.T) {
	events := make(chan Event, 1)
	abandoned := make(chan struct{})
	sink := eventSink{events: events, abandoned: abandoned}
	close(abandoned)

	sink.send(Event{Kind: EventText, Text: "fits the buffer"})
	sink.send(Event{Kind: EventText, Text: "dropped"})

	if got := (<-events).Text; got != "fits the buffer" {
		t.Errorf("first event = %q; want the buffered one", got)
	}
	if len(events) != 0 {
		t.Error("the second event should have been dropped")
	}
}

// Regression: with ctx done and the provider's channel closed at once, the
// select could take the close and accept a cut reply after LLMCallTimeout.
func TestReceiveReply_StreamClosedAfterContextEndIsAFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink := eventSink{events: make(chan Event, 2*eventBufferSize), abandoned: ctx.Done()}
	for range 100 {
		providerEvents := make(chan llm.Event, 1)
		providerEvents <- llm.Event{Kind: llm.EventTextDelta, Text: "cut"}
		close(providerEvents)
		if _, err := receiveReply(ctx, providerEvents, sink); !errors.Is(err, context.Canceled) {
			t.Fatalf("receiveReply = %v; want context.Canceled", err)
		}
	}
}

// observingLocker grants the lock once the test passed it the run's event
// channel; unlock reports how many events the run had queued by then.
type observingLocker struct {
	runEvents      chan (<-chan Event)
	queuedAtUnlock chan int
}

func (l observingLocker) Lock(context.Context, string) (func(), error) {
	events := <-l.runEvents
	return func() { l.queuedAtUnlock <- len(events) }, nil
}

func TestRun_ReleasesTheLockAfterEventEnd(t *testing.T) {
	locker := observingLocker{runEvents: make(chan (<-chan Event), 1), queuedAtUnlock: make(chan int, 1)}
	session := newTestSession(t, llmtest.NewStubClient(textTurn("hi")), nil, nil, func(options *SessionOptions) {
		options.Locker = locker
	})
	events, _ := session.Run(context.Background(), "go")
	locker.runEvents <- events

	queued := <-locker.queuedAtUnlock // nothing is read before the unlock
	if all := collectEvents(t, events); queued != len(all) {
		t.Errorf("unlock saw %d of the run's %d events queued; want EventEnd sent before the unlock", queued, len(all))
	}
}

// Regression: the run read the published ToolCall and ToolResult again for
// the history, so a caller changing them raced with the run (go test -race).
func TestRun_CallerMayChangePublishedToolEvents(t *testing.T) {
	registry := tools.NewRegistry()
	mustRegister(t, registry, "ping", func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
		return tools.Result{Content: "pong"}, nil
	})
	history := memory.New()
	session := newTestSession(t, llmtest.NewStubClient(toolCallTurn("c1", "ping", "{}"), textTurn("done")), registry, history)
	events, _ := session.Run(context.Background(), "go")
	for event := range events {
		switch event.Kind {
		case EventToolCall:
			event.ToolCall.ID = "changed"
		case EventToolResult:
			event.ToolResult.ModelContent = "changed"
		default:
		}
	}
	stored := storedMessages(t, history, "s-test")
	if len(stored) != 4 || stored[2].ToolCallID != "c1" || stored[2].Content != `"pong"` {
		t.Errorf("stored = %+v; want the tool message as the model saw it", stored)
	}
}

func TestRun_FailedCallStillReportsItsUsage(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "partial"},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 7}},
		{Kind: llm.EventError, Err: errors.New("content filter")},
		{Kind: llm.EventEnd},
	}})
	events := runToEnd(t, newTestSession(t, stub, nil, nil), "go")

	var kinds []EventKind
	for _, event := range events {
		if event.Kind == EventUsage && event.Usage.PromptTokens != 7 {
			t.Errorf("usage = %+v; want the provider's", event.Usage)
		}
		if event.Kind != EventText {
			kinds = append(kinds, event.Kind)
		}
	}
	if want := []EventKind{EventUsage, EventError, EventEnd}; !slices.Equal(kinds, want) {
		t.Errorf("events = %v; want %v", kinds, want)
	}
}
