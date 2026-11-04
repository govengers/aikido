package oteladapter_test

import (
	"context"
	"fmt"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/llm/oteladapter"
)

func TestNewRequiresATracerProvider(t *testing.T) {
	if _, err := oteladapter.New(nil); err == nil {
		t.Error("nil options: want an error")
	}
	if _, err := oteladapter.New(&oteladapter.Options{}); err == nil {
		t.Error("no provider: want an error")
	}
}

func newClient(t *testing.T, stub llm.Client) (*llm.ObservedClient, *tracetest.SpanRecorder, trace.Tracer) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	spans, err := oteladapter.New(&oteladapter.Options{TracerProvider: provider})
	if err != nil {
		t.Fatal(err)
	}
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: stub, Name: "openrouter", Observers: []llm.Observer{spans}})
	if err != nil {
		t.Fatal(err)
	}
	return observed, recorder, provider.Tracer("test")
}

func attributes(span sdktrace.ReadOnlySpan) map[string]string {
	values := map[string]string{}
	for _, recorded := range span.Attributes() {
		values[string(recorded.Key)] = recorded.Value.String()
	}
	return values
}

func TestACallIsAClientSpanInTheCallersTrace(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "the secret answer"},
		{Kind: llm.EventUsage, Usage: &llm.Usage{PromptTokens: 120, CompletionTokens: 30}},
		{Kind: llm.EventEnd, FinishReason: "stop"},
	}})
	observed, recorder, tracer := newClient(t, stub)
	ctx, request := tracer.Start(t.Context(), "POST /api/v1/chats")

	if _, err := llm.Collect(ctx, observed, llm.Request{Model: "anthropic/claude-sonnet-4.6", Messages: []llm.Message{{Role: llm.RoleUser, Content: "a secret question"}}}); err != nil {
		t.Fatal(err)
	}
	request.End()

	spans := recorder.Ended()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want the call and the request", len(spans))
	}
	call := spans[0]
	if call.Name() != "chat anthropic/claude-sonnet-4.6" || call.SpanKind() != trace.SpanKindClient {
		t.Errorf("span %q of kind %v", call.Name(), call.SpanKind())
	}
	if call.Parent().SpanID() != request.SpanContext().SpanID() {
		t.Error("the call's span is not a child of the request's")
	}
	want := map[string]string{
		"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openrouter", "gen_ai.request.model": "anthropic/claude-sonnet-4.6",
		"aikido.llm.operation": "stream", "gen_ai.usage.input_tokens": "120", "gen_ai.usage.output_tokens": "30",
		"gen_ai.response.finish_reasons": `["stop"]`,
	}
	if got := attributes(call); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("attributes = %v, want %v (no prompt, no completion)", got, want)
	}
}

func TestAFailedCallIsAnError(t *testing.T) {
	stub := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventError, Err: llm.ErrRateLimited}}})
	observed, recorder, _ := newClient(t, stub)

	if _, err := observed.Complete(t.Context(), llm.Request{}); err == nil {
		t.Fatal("want the error")
	}

	spans := recorder.Ended()
	if len(spans) != 1 || spans[0].Status().Code.String() != "Error" || len(spans[0].Events()) != 1 || spans[0].Name() != "chat" {
		t.Errorf("spans = %v", spans)
	}
}

func TestTheProviderSeesTheCallsSpan(t *testing.T) {
	var seen trace.SpanContext
	observed, recorder, _ := newClient(t, spanReadingClient{seen: &seen})

	if _, err := observed.Complete(t.Context(), llm.Request{Model: "openai/gpt-5.4"}); err != nil {
		t.Fatal(err)
	}

	if spans := recorder.Ended(); len(spans) != 1 || !spans[0].SpanContext().Equal(seen) {
		t.Error("the wrapped client did not get the span in its context")
	}
}

// spanReadingClient records the span its Complete runs in.
type spanReadingClient struct {
	llm.Client
	seen *trace.SpanContext
}

func (c spanReadingClient) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	*c.seen = trace.SpanContextFromContext(ctx)
	return llm.Response{}, nil
}

func ExampleNew() {
	spans, err := oteladapter.New(&oteladapter.Options{TracerProvider: noop.NewTracerProvider()}) // in a service: the provider of govengers/telemetry/tracing
	if err != nil {
		fmt.Println(err)
		return
	}
	observed, err := llm.NewObservedClient(&llm.ObservedClientOptions{Client: llmtest.NewStubClient(), Name: "openrouter", Observers: []llm.Observer{spans}})
	fmt.Println(observed != nil, err)
	// Output: true <nil>
}
