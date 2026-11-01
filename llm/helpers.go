package llm

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/govengers/aikido/retry"
)

// IsTransientServerError reports whether err is a transient upstream-provider
// failure (typically a 5xx, mid-stream RST, or other server-side flake) that
// is worth retrying. Authentication errors, invalid-request errors, and rate
// limits are NOT classified as transient: authentication and bad-request
// errors won't fix themselves with a retry, and rate limits should be honored
// explicitly (typically via a Retry-After-aware policy at the caller).
//
// Use as the ShouldRetry predicate when retrying llm.Collect:
//
//	llm.CollectWithRetry(ctx, client, request, retry.Policy{
//	    MaximumAttempts: 5,
//	    BaseDelay:       2 * time.Second,
//	    MaximumDelay:    30 * time.Second,
//	    Multiplier:      2.0,
//	    Jitter:          0.2,
//	    ShouldRetry:     llm.IsTransientServerError,
//	})
func IsTransientServerError(err error) bool {
	return errors.Is(err, ErrServerError)
}

// IsContentFilter reports whether err is a content-policy abort surfaced by the
// provider (finish_reason="content_filter", an explicit error envelope with a
// matching code/message, or any error wrapping ErrContentFiltered). Use this
// to short-circuit retry, since the same prompt will trip the classifier again
// on the next attempt, and to render a "try different wording" message rather
// than a generic "try again later".
//
// Note: providers sometimes mid-stream RST without sending a structured
// content-filter signal. Those failures surface as ErrServerError and are
// indistinguishable from genuine transient flake at the wire level. Callers
// that want stronger detection can heuristically classify "all retries
// failed identically" as a content-filter signal at the call-site.
func IsContentFilter(err error) bool {
	return errors.Is(err, ErrContentFiltered)
}

// DefaultStreamingRetryPolicy returns a sensible retry policy for image
// generation and other streaming LLM operations: 5 attempts, 2s base, 30s cap,
// 2x multiplier, 20% jitter, retrying only transient provider errors.
//
// Tuned for image-gen preview models (e.g. Gemini flash-image-preview) which
// drop streams under upstream load at ~20% in observed traces. With 5 attempts
// at this rate the effective failure rate is ~0.03%.
func DefaultStreamingRetryPolicy() retry.Policy {
	return retry.Policy{
		MaximumAttempts: 5,
		BaseDelay:       2 * time.Second,
		MaximumDelay:    30 * time.Second,
		Multiplier:      2.0,
		Jitter:          0.2,
		ShouldRetry:     IsTransientServerError,
	}
}

// CollectWithRetry wraps Collect with retry.Do using the supplied policy.
// On retry, the entire stream is restarted from scratch (no resume) and
// Collect's accumulated state is discarded between attempts. The returned
// Response is from the last attempt, also when that attempt failed.
//
// Provider-side cost is paid per attempt: a streaming model that aborts after
// emitting partial tokens still bills for those tokens. Tune MaximumAttempts
// with cost in mind.
//
// If policy.ShouldRetry is nil, IsTransientServerError is used. If
// policy.MaximumAttempts is < 1, it's clamped to 1 (no retry).
func CollectWithRetry(ctx context.Context, client Client, request Request, policy retry.Policy) (Response, error) {
	if policy.ShouldRetry == nil {
		policy.ShouldRetry = IsTransientServerError
	}
	var response Response
	err := retry.Do(ctx, policy, func(int) error {
		var attemptError error
		response, attemptError = Collect(ctx, client, request)
		return attemptError
	})
	return response, err
}

// CompleteWithRetry wraps Client.Complete with retry.Do using the supplied
// policy. Each attempt sends a fresh non-streaming request; the returned
// Response is from the last attempt, also when that attempt failed.
//
// Use this for image generation: a single oversized base64 payload exceeds the
// SSE scanner cap and trips ErrServerError on every Stream attempt. Complete
// reads the body as one JSON document and avoids the cap entirely; the retry
// wrapper is here for the standard 429/5xx-at-request-start flake.
//
// If policy.ShouldRetry is nil, IsTransientServerError is used.
func CompleteWithRetry(ctx context.Context, client Client, request Request, policy retry.Policy) (Response, error) {
	if policy.ShouldRetry == nil {
		policy.ShouldRetry = IsTransientServerError
	}
	var response Response
	err := retry.Do(ctx, policy, func(int) error {
		var attemptError error
		response, attemptError = client.Complete(ctx, request)
		return attemptError
	})
	return response, err
}

// Collect drains a stream into one Response, for callers that want the whole
// answer but need Stream's behavior (or a client without a separate Complete).
//
// The Response holds the text accumulated from EventTextDelta, all complete
// tool calls, all images surfaced by the provider, the final Usage if the
// provider emitted one and the FinishReason of EventEnd. Thinking text is not
// included in the text.
//
// On a stream error Collect keeps reading until the channel closes, so the
// usage a provider reports after the error (the failed call was billed) and
// the FinishReason still land in the Response; it returns the first error.
// On a cancelled ctx it returns what it gathered so far with that first
// error, else with the context error, also when the channel closed at the
// same time; it does not wait for the producer after ctx ends.
func Collect(ctx context.Context, client Client, request Request) (Response, error) {
	events, err := client.Stream(ctx, request)
	if err != nil {
		return Response{}, err
	}
	var (
		response    Response
		text        strings.Builder
		streamError error
	)
	for {
		select {
		case <-ctx.Done():
			response.Text = text.String()
			if streamError != nil {
				return response, streamError
			}
			return response, ctx.Err()
		case event, open := <-events:
			if !open {
				response.Text = text.String()
				if streamError == nil {
					// A producer stops early when ctx ends: a close after
					// that is a cut-off answer, not a success.
					streamError = ctx.Err()
				}
				return response, streamError
			}
			if event.Kind == EventError && streamError == nil {
				streamError = event.Err
			}
			collectEvent(&response, &text, event)
		}
	}
}

func collectEvent(response *Response, text *strings.Builder, event Event) {
	switch event.Kind {
	case EventTextDelta:
		text.WriteString(event.Text)
	case EventToolCall:
		if event.Tool != nil {
			response.ToolCalls = append(response.ToolCalls, *event.Tool)
		}
	case EventImage:
		if event.Image != nil {
			response.Images = append(response.Images, *event.Image)
		}
	case EventUsage:
		response.Usage = event.Usage
	case EventEnd:
		response.FinishReason = event.FinishReason
	case EventThinking, EventError:
	}
}
