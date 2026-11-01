package llm

import (
	"context"
	"encoding/json"
)

// Request is one model call, the same for Client.Stream and Client.Complete.
// Zero and nil fields leave the provider's default.
type Request struct {
	// Model is the provider's model id ("anthropic/claude-sonnet-4.6"). Empty
	// leaves the client's default model, where the client has one.
	Model string
	// Messages is the conversation in order, system prompt first.
	Messages []Message
	// Tools are the tools the model may call; calls come back as
	// EventToolCall or Response.ToolCalls.
	Tools []ToolDefinition
	// MaximumTokens caps the completion tokens. 0 leaves the provider default.
	MaximumTokens int
	// Temperature is the sampling temperature. Nil leaves the provider
	// default; new(float32(0)) asks for deterministic decoding.
	Temperature *float32
	// Thinking configures provider-side reasoning. Nil leaves the provider
	// default.
	Thinking *ThinkingConfiguration
	// StopSequences end the generation when the model emits one of them.
	StopSequences []string

	// Modalities lists the output modalities the caller wants the model to
	// emit. For image-capable models, set ["image", "text"]; otherwise the
	// model defaults to text-only and image generation silently fails or
	// returns a provider error. Empty leaves the provider default.
	Modalities []string

	// ImageConfiguration tunes image-output generation (aspect ratio, resolution).
	// Honored only by image-capable models. Nil leaves provider defaults
	// (typically 1:1 / 1K).
	ImageConfiguration *ImageConfiguration
	// ResponseFormat asks for JSON matching a schema (structured output).
	// Nil leaves free-form text.
	ResponseFormat *JSONSchema
}

// JSONSchema constrains a response to JSON matching Schema. Providers map it
// onto their structured-output field (OpenRouter `response_format`, the Codex
// Responses API `text.format`).
type JSONSchema struct {
	// Name identifies the schema, [a-zA-Z0-9_-] only.
	Name string
	// Schema is a JSON Schema object.
	Schema json.RawMessage
	// Strict asks the provider to enforce the schema exactly. Strict schemas
	// need additionalProperties:false and every property listed as required.
	Strict bool
}

// ImageConfiguration configures image-output generation. Forwarded as the
// `image_config` field on the OpenRouter chat-completions request. Non-image
// models ignore it.
//
// AspectRatio values supported across image models: "1:1" (default), "2:3",
// "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9". The extended
// ratios "1:4", "4:1", "1:8", "8:1" are honored only by
// google/gemini-3.1-flash-image-preview.
//
// ImageSize values: "1K" (default), "2K", "4K". "0.5K" is honored only by
// google/gemini-3.1-flash-image-preview.
//
// Empty fields are omitted from the wire body, letting the provider default
// apply per-field.
type ImageConfiguration struct {
	AspectRatio string
	ImageSize   string
}

// EventKind identifies the kind of a streaming event.
type EventKind string

// The kinds of Event, each naming the Event field that carries its payload.
const (
	// EventTextDelta carries the next piece of the answer in Text.
	EventTextDelta EventKind = "text_delta"
	// EventToolCall carries one complete tool call in Tool.
	EventToolCall EventKind = "tool_call"
	// EventThinking carries the next piece of the model's reasoning in Text.
	EventThinking EventKind = "thinking"
	// EventImage carries one generated image in Image.
	EventImage EventKind = "image"
	// EventUsage carries the call's accounting in Usage.
	EventUsage EventKind = "usage"
	// EventError carries the failure that ended the generation in Err,
	// wrapping one of the Err* sentinels of this package.
	EventError EventKind = "error"
	// EventEnd closes the generation; FinishReason is set when the provider
	// reported one.
	EventEnd EventKind = "end"
)

// Event is one streaming event emitted by a Client. Only the field its Kind
// names is set. The channel closes after EventEnd.
type Event struct {
	// Kind says which of the other fields is set.
	Kind EventKind
	// Text is the delta of EventTextDelta and EventThinking.
	Text string
	// Tool is the complete call of EventToolCall.
	Tool *ToolCall
	// Image is the image of EventImage.
	Image *ImagePart
	// Usage is the accounting of EventUsage.
	Usage *Usage
	// Err is the failure of EventError.
	Err error

	// FinishReason carries the provider's reported finish_reason on the chunk
	// that closes a generation. Set on EventEnd (and on the EventError that
	// stands in for finish_reason="content_filter"). Common values: "stop",
	// "tool_calls", "length", "content_filter", "error". Empty when the
	// provider didn't surface one.
	FinishReason string
}

// Response is the fully-assembled result of a non-streaming completion.
//
// Returned by Client.Complete in one shot: no event channel, no SSE framing.
// Use this for image generation and any other workload where progressive token
// delivery has no UX value: the SSE path imposes a per-line buffer cap that
// can't accommodate the multi-MB single-chunk responses image-capable models
// emit.
type Response struct {
	// Text is the whole answer, without the model's reasoning.
	Text string
	// ToolCalls are the complete tool calls, in the order the model made them.
	ToolCalls []ToolCall
	// Images are the generated images.
	Images []ImagePart
	// Usage is nil when the provider reported none.
	Usage *Usage
	// FinishReason mirrors the provider's finish_reason ("stop", "tool_calls",
	// "length", "content_filter", ...), empty when it reported none.
	FinishReason string
}

// Client is a model provider: llm/openrouter and llm/codex implement it,
// llmtest.StubClient fakes it in tests. Optional capabilities (ImageGenerator,
// SpeechGenerator, VideoGenerator, Decider) are separate interfaces callers
// type-assert on.
type Client interface {
	// Stream sends a request and returns its events as they arrive. An error
	// before the first byte (authentication, invalid request, exhausted
	// retries) is returned directly; later failures arrive as EventError. The
	// channel ends with EventEnd and is closed by the client; it is closed
	// early when ctx ends, so the caller stops reading on ctx.Done() or on
	// close and needs no other cleanup.
	Stream(ctx context.Context, req Request) (<-chan Event, error)

	// Complete sends a non-streaming completion request and returns the full
	// response in one shot. Implementations should map provider errors onto the
	// same Err* sentinels Stream uses (ErrAuthentication, ErrRateLimited, ErrServerError,
	// ErrInvalidRequest, ErrContentFiltered).
	//
	// Complete is the right choice for image generation: image-capable models
	// emit the entire base64 payload in a single chunk that frequently exceeds
	// the SSE per-line scanner cap, deterministically failing Stream-based
	// callers. Complete reads the response as a single JSON body and is
	// immune to that failure mode.
	Complete(ctx context.Context, req Request) (Response, error)
}
