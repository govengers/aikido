package llm_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

type contextKey struct{}

// recordingObserver records the calls and outcomes it sees and marks the call's context.
type recordingObserver struct {
	name     string
	order    *[]string
	mutex    sync.Mutex
	calls    []llm.Call
	outcomes []llm.CallOutcome
	contexts []any // the mark CallFinished found in its context
}

func (o *recordingObserver) CallStarted(ctx context.Context, call *llm.Call) context.Context {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	*o.order = append(*o.order, "start "+o.name)
	o.calls = append(o.calls, *call)
	return context.WithValue(ctx, contextKey{}, o.name)
}

func (o *recordingObserver) CallFinished(ctx context.Context, _ *llm.Call, outcome *llm.CallOutcome) {
	o.mutex.Lock()
	defer o.mutex.Unlock()
	*o.order = append(*o.order, "finish "+o.name)
	o.outcomes = append(o.outcomes, *outcome)
	o.contexts = append(o.contexts, ctx.Value(contextKey{}))
}

func (o *recordingObserver) outcome(t *testing.T) llm.CallOutcome {
	t.Helper()
	o.mutex.Lock()
	defer o.mutex.Unlock()
	if len(o.outcomes) != 1 {
		t.Fatalf("outcomes = %d, want 1", len(o.outcomes))
	}
	return o.outcomes[0]
}

// observedHarness is an ObservedClient with two recording observers that share one log of
// their calls.
type observedHarness struct {
	client        *llm.ObservedClient
	first, second *recordingObserver
	order         *[]string
}

func newObserved(t *testing.T, client llm.Client) observedHarness {
	t.Helper()
	order := &[]string{}
	first, second := &recordingObserver{name: "first", order: order}, &recordingObserver{name: "second", order: order}
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: client, Name: "openrouter", Observers: []llm.Observer{first, second}})
	if err != nil {
		t.Fatal(err)
	}
	return observedHarness{client: observed, first: first, second: second, order: order}
}

func TestNewObservedClientValidatesOptions(t *testing.T) {
	stub := llmtest.NewStubClient()
	observer := &recordingObserver{order: &[]string{}}
	for name, options := range map[string]*llm.ObservedClientOptions{
		"nil options":  nil,
		"no client":    {Name: "openrouter", Observers: []llm.Observer{observer}},
		"no name":      {Client: stub, Observers: []llm.Observer{observer}},
		"no observer":  {Client: stub, Name: "openrouter"},
		"nil observer": {Client: stub, Name: "openrouter", Observers: []llm.Observer{nil}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := llm.NewObservedClient(options); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestStreamReportsUsageAndFinishReasonAndPassesEventsOn(t *testing.T) {
	usage := &llm.Usage{PromptTokens: 120, CompletionTokens: 30, CostUSD: 0.002}
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "hello"},
		{Kind: llm.EventUsage, Usage: usage},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}})
	harness := newObserved(t, stub)
	observed, first, second, order := harness.client, harness.first, harness.second, harness.order

	response, err := llm.Collect(t.Context(), observed, llm.Request{Model: "anthropic/claude-sonnet-4.6"})
	if err != nil {
		t.Fatal(err)
	}

	if response.Text != "hello" {
		t.Errorf("text = %q, want hello", response.Text)
	}
	outcome := second.outcome(t)
	if outcome.Usage != usage || outcome.FinishReason != "stop" || outcome.Err != nil || outcome.Duration <= 0 {
		t.Errorf("outcome = %+v", outcome)
	}
	if call := first.calls[0]; call != (llm.Call{Client: "openrouter", Operation: llm.OperationStream, Model: "anthropic/claude-sonnet-4.6"}) {
		t.Errorf("call = %+v", call)
	}
	if got := *order; len(got) != 4 || got[0] != "start first" || got[1] != "start second" || got[2] != "finish second" || got[3] != "finish first" {
		t.Errorf("order = %v, want started in order and finished in reverse", got)
	}
	if first.contexts[0] != "second" {
		t.Errorf("CallFinished got context %v, want the call's context", first.contexts[0])
	}
}

func TestStreamReportsTheEventError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventError, Err: llm.ErrContentFiltered, FinishReason: "content_filter"},
		{Kind: llm.EventEnd, FinishReason: "content_filter"},
	}})
	harness := newObserved(t, stub)
	observed, first := harness.client, harness.first

	events, err := observed.Stream(t.Context(), llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	// drain the stream
	for range events {
	}

	outcome := first.outcome(t)
	if !errors.Is(outcome.Err, llm.ErrContentFiltered) || outcome.FinishReason != "content_filter" {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestStreamReportsAnErrorBeforeTheFirstByte(t *testing.T) {
	harness := newObserved(t, llmtest.NewStubClient()) // no turn scripted: Stream fails
	observed, first := harness.client, harness.first

	if _, err := observed.Stream(t.Context(), llm.Request{}); err == nil {
		t.Fatal("want an error")
	}

	if first.outcome(t).Err == nil {
		t.Error("the outcome carries no error")
	}
}

func TestStreamReportsACallerThatGaveUp(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	stub := llmtest.NewStubClient(llmtest.TurnScript{Block: block, Events: []llm.Event{{Kind: llm.EventEnd}}})
	harness := newObserved(t, stub)
	observed, first := harness.client, harness.first
	ctx, cancel := context.WithCancel(t.Context())

	events, err := observed.Stream(ctx, llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// drain until the observed channel closes
	for range events {
	}

	if outcome := first.outcome(t); !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("outcome error = %v, want context.Canceled", outcome.Err)
	}
}

func TestStreamStopsForwardingWhenTheCallerStopsReading(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "a"},
		{Kind: llm.EventTextDelta, Text: "b"},
		{Kind: llm.EventEnd},
	}})
	harness := newObserved(t, stub)
	observed, first := harness.client, harness.first
	ctx, cancel := context.WithCancel(t.Context())

	events, err := observed.Stream(ctx, llm.Request{})
	if err != nil {
		t.Fatal(err)
	}
	<-events // the caller reads one event, then gives up
	cancel()
	// the channel closes
	for range events {
	}

	if outcome := first.outcome(t); !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("outcome error = %v, want context.Canceled", outcome.Err)
	}
}

func TestCompleteReportsTheResponse(t *testing.T) {
	usage := &llm.Usage{PromptTokens: 10, CompletionTokens: 5}
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "done"},
		{Kind: llm.EventUsage, Usage: usage},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}})
	harness := newObserved(t, stub)
	observed, first := harness.client, harness.first

	response, err := observed.Complete(t.Context(), llm.Request{Model: "openai/gpt-5.4"})
	if err != nil {
		t.Fatal(err)
	}

	outcome := first.outcome(t)
	if response.Text != "done" || outcome.Usage != usage || outcome.Err != nil {
		t.Errorf("response = %+v, outcome = %+v", response, outcome)
	}
	if first.calls[0].Operation != llm.OperationComplete {
		t.Errorf("operation = %q", first.calls[0].Operation)
	}
}

func TestCompleteReportsItsError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrRateLimited}}})
	harness := newObserved(t, stub)
	observed, first := harness.client, harness.first

	_, err := observed.Complete(t.Context(), llm.Request{})

	if !errors.Is(err, llm.ErrRateLimited) || !errors.Is(first.outcome(t).Err, llm.ErrRateLimited) {
		t.Errorf("err = %v, outcome = %v", err, first.outcome(t).Err)
	}
}
