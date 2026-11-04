// Package oteladapter traces the model calls of an llm.ObservedClient with OpenTelemetry: one
// client span per call, after the GenAI semantic conventions, in the caller's trace. Spans
// implements llm.Observer; put it into llm.ObservedClientOptions.Observers. The package
// depends on the OpenTelemetry API only; the service builds the SDK provider.
//
// No prompt, completion or tool argument reaches a span: they carry user input and personal
// data, and spans go to a backend shared wider than the conversation store.
package oteladapter

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/govengers/aikido/llm"
)

// ScopeName is the instrumentation scope of the spans.
const ScopeName = "github.com/govengers/aikido/llm/oteladapter"

// The GenAI semantic-convention attributes the spans carry.
const (
	AttributeOperationName attribute.Key = "gen_ai.operation.name"
	AttributeProviderName  attribute.Key = "gen_ai.provider.name"
	AttributeRequestModel  attribute.Key = "gen_ai.request.model"
	AttributeFinishReasons attribute.Key = "gen_ai.response.finish_reasons"
	AttributeInputTokens   attribute.Key = "gen_ai.usage.input_tokens"  //nolint:gosec // G101: an attribute name, not a credential
	AttributeOutputTokens  attribute.Key = "gen_ai.usage.output_tokens" //nolint:gosec // G101: an attribute name, not a credential
	AttributeOperation     attribute.Key = "aikido.llm.operation"       // llm.OperationStream or llm.OperationComplete
	OperationChat                        = "chat"
)

// Options configures Spans.
type Options struct {
	// TracerProvider starts the spans; required. Pass the provider main builds, or
	// otel.GetTracerProvider() when main installed it globally.
	TracerProvider trace.TracerProvider
}

// Spans traces model calls; it implements llm.Observer and is safe for concurrent use.
type Spans struct {
	tracer trace.Tracer
}

var _ llm.Observer = (*Spans)(nil)

// New returns the observer.
func New(options *Options) (*Spans, error) {
	if options == nil || options.TracerProvider == nil {
		return nil, errors.New("oteladapter: Options.TracerProvider is required")
	}
	return &Spans{tracer: options.TracerProvider.Tracer(ScopeName)}, nil
}

// CallStarted starts the call's span, named "chat <model>", and returns the context that
// carries it, so the provider's HTTP instrumentation nests below it.
func (s *Spans) CallStarted(ctx context.Context, call *llm.Call) context.Context {
	name := OperationChat
	if call.Model != "" {
		name += " " + call.Model
	}
	ctx, _ = s.tracer.Start(ctx, name, trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
		AttributeOperationName.String(OperationChat),
		AttributeProviderName.String(call.Client),
		AttributeRequestModel.String(call.Model),
		AttributeOperation.String(call.Operation),
	))
	return ctx
}

// CallFinished records the usage, the finish reason and the error on the span and ends it.
func (s *Spans) CallFinished(ctx context.Context, _ *llm.Call, outcome *llm.CallOutcome) {
	span := trace.SpanFromContext(ctx)
	if outcome.Usage != nil {
		span.SetAttributes(AttributeInputTokens.Int(outcome.Usage.PromptTokens), AttributeOutputTokens.Int(outcome.Usage.CompletionTokens))
	}
	if outcome.FinishReason != "" {
		span.SetAttributes(AttributeFinishReasons.StringSlice([]string{outcome.FinishReason}))
	}
	if outcome.Err != nil {
		span.RecordError(outcome.Err)
		span.SetStatus(codes.Error, outcome.Err.Error())
	}
	span.End()
}
