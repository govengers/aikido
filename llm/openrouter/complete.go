package openrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/govengers/aikido/llm"
)

// Complete sends a non-streaming chat-completions request and returns the
// fully-assembled response in one shot.
//
// Use this for image generation. Image-capable models (gemini-flash-image-preview,
// gpt-image-1, etc.) emit the entire base64 PNG/JPEG payload in a single SSE
// chunk. A 1024×1024 PNG is typically 0.8–2 MB raw, 1.07–2.7 MiB base64,
// which exceeds the SSE scanner's per-line cap and trips ErrServerError on
// every retry. Complete reads the body as one JSON document and is immune to
// that failure mode.
//
// Retry behaviour mirrors Stream: 429/5xx at request-start are retried per
// Options.MaximumAttempts; auth and bad-request errors short-circuit.
// Content-filter aborts surface as ErrContentFiltered (either via a top-level
// error envelope on the JSON body or via finish_reason="content_filter"), with
// the partial Response (usage, finish reason) still returned. Network-layer
// errors are mapped onto ErrServerError so CompleteWithRetry can apply the
// same retry policy as CollectWithRetry; a cancelled ctx surfaces as the
// context error.
func (c *Client) Complete(ctx context.Context, request llm.Request) (llm.Response, error) {
	start := time.Now()
	body, err := c.buildBody(ctx, request, false)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: build request: %w", err)
	}

	raw, err := c.postJSON(ctx, "/chat/completions", body)
	if err != nil {
		return llm.Response{}, err
	}
	response, err := parseCompleteResponse(raw)
	if response.Usage != nil {
		response.Usage.SetTiming(start, time.Time{}, time.Now())
	}
	return response, err
}

// parseCompleteResponse maps a non-streaming chat-completions JSON body onto
// llm.Response.
//
// The wire shape mirrors streamChunk except that the assistant payload lives
// on choices[i].message instead of choices[i].delta. Both shapes are folded
// in: some providers (and gateway shims) populate `delta` even on
// non-streaming responses.
func parseCompleteResponse(body []byte) (llm.Response, error) {
	var chunk streamChunk
	if err := json.Unmarshal(body, &chunk); err != nil {
		return llm.Response{}, fmt.Errorf("openrouter: decode response: %w", llm.ErrServerError)
	}

	// The usage block rides along when the provider priced the failed call,
	// and a priced failure still cost money: return it with the error rather
	// than dropping it.
	if chunk.Error != nil {
		return llm.Response{Usage: chunkUsage(&chunk)}, fmt.Errorf("openrouter: %s: %w", chunk.Error.message(), chunk.Error.cause())
	}

	var response llm.Response
	assembler := newToolCallAssembler()
	for _, choice := range chunk.Choices {
		absorbDelta(&response, assembler, choice.Delta)
		if choice.Message != nil {
			absorbDelta(&response, assembler, *choice.Message)
		}
		if choice.FinishReason != nil {
			response.FinishReason = *choice.FinishReason
		}
	}
	response.ToolCalls = append(response.ToolCalls, assembler.flush()...)
	response.Usage = chunkUsage(&chunk)

	// finish_reason="content_filter" without a top-level error envelope:
	// surface as ErrContentFiltered so the same caller branch fires for both
	// streaming and non-streaming.
	if response.FinishReason == finishReasonContentFilter {
		return response, fmt.Errorf("openrouter finish_reason=content_filter: %w", llm.ErrContentFiltered)
	}
	return response, nil
}

// absorbDelta folds one streamDelta payload (delta or message) into the
// response. It mirrors streamDecoder.emitPayload, writing to a Response
// rather than emitting events. llm.Response has no thinking field, so
// reasoning is dropped.
func absorbDelta(response *llm.Response, assembler *toolCallAssembler, payload streamDelta) {
	text, images := decodeDeltaContent(payload.Content)
	response.Text += text
	response.Images = append(response.Images, images...)
	for _, part := range payload.Images {
		if image, ok := decodeAPIImagePart(part); ok {
			response.Images = append(response.Images, image)
		}
	}
	for _, fragment := range payload.ToolCalls {
		assembler.feed(fragment)
	}
}
