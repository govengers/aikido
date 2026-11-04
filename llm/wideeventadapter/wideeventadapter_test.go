package wideeventadapter_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	wideevent "github.com/govengers/wide-event-logger"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/wideeventadapter"
)

func fields(we *wideevent.Event) map[string]any {
	values := map[string]any{}
	for _, field := range we.Fields() {
		values[field.Key] = field.Value
	}
	return values
}

func TestCallsAreSummedOnTheEvent(t *testing.T) {
	observer := wideeventadapter.New()
	we := wideevent.Begin(wideevent.EmitterFunc(func(*wideevent.Event) {}), "chat")
	ctx := observer.CallStarted(wideevent.NewContext(t.Context(), we), &llm.Call{})
	call := &llm.Call{Client: "openrouter", Operation: llm.OperationStream, Model: "anthropic/claude-sonnet-4.6"}

	observer.CallFinished(ctx, call, &llm.CallOutcome{Duration: 2 * time.Second, FinishReason: "tool_calls", Usage: &llm.Usage{
		PromptTokens: 1000, CompletionTokens: 100, CacheReadTokens: 800, CostUSD: 0.01,
	}})
	observer.CallFinished(ctx, call, &llm.CallOutcome{Duration: time.Second, FinishReason: "stop", Usage: &llm.Usage{
		PromptTokens: 1200, CompletionTokens: 50, CostUSD: 0.005,
	}})
	observer.CallFinished(ctx, call, &llm.CallOutcome{Duration: time.Second, Err: llm.ErrRateLimited})

	got := fields(we)
	want := map[string]any{
		wideeventadapter.KeyCalls: 3, wideeventadapter.KeyFailures: 1, wideeventadapter.KeyDuration: 4 * time.Second,
		wideeventadapter.KeyPromptTokens: 2200, wideeventadapter.KeyCompletionTokens: 150, wideeventadapter.KeyCacheReadTokens: 800,
		wideeventadapter.KeyClient: "openrouter", wideeventadapter.KeyModel: "anthropic/claude-sonnet-4.6",
		wideeventadapter.KeyFinishReason: "stop", wideeventadapter.KeyError: llm.ErrorClassRateLimited,
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
	if cost, _ := got[wideeventadapter.KeyCostUSD].(float64); cost < 0.0149 || cost > 0.0151 {
		t.Errorf("cost = %v, want 0.015", cost)
	}
}

func TestAContextWithoutAnEventRecordsNothing(t *testing.T) {
	observer := wideeventadapter.New()

	observer.CallFinished(context.Background(), &llm.Call{}, &llm.CallOutcome{}) // must not panic
}

func TestConcurrentCallsAreAllCounted(t *testing.T) {
	observer := wideeventadapter.New()
	we := wideevent.Begin(wideevent.EmitterFunc(func(*wideevent.Event) {}), "chat")
	ctx := wideevent.NewContext(t.Context(), we)
	done := make(chan struct{})
	for range 20 {
		go func() {
			defer func() { done <- struct{}{} }()
			observer.CallFinished(ctx, &llm.Call{}, &llm.CallOutcome{Usage: &llm.Usage{PromptTokens: 1}})
		}()
	}
	for range 20 {
		<-done
	}

	if got := fields(we); got[wideeventadapter.KeyCalls] != 20 || got[wideeventadapter.KeyPromptTokens] != 20 {
		t.Errorf("calls = %v, prompt tokens = %v, want 20 each", got[wideeventadapter.KeyCalls], got[wideeventadapter.KeyPromptTokens])
	}
}

func ExampleNew() {
	observer := wideeventadapter.New()
	we := wideevent.Begin(wideevent.EmitterFunc(func(*wideevent.Event) {}), "chat") // in a service: the request's event
	ctx := wideevent.NewContext(context.Background(), we)
	observer.CallFinished(ctx, &llm.Call{Client: "openrouter", Model: "openai/gpt-5.4"}, &llm.CallOutcome{Usage: &llm.Usage{PromptTokens: 42}})
	fmt.Println(fields(we)[wideeventadapter.KeyPromptTokens])
	// Output: 42
}
