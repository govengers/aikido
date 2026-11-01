package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

// TestUsageAccounting_RequestedOnBothPaths pins the wire contract that makes
// exact cost accounting possible at all: without "usage":{"include":true} the
// provider reports tokens but never a price.
func TestUsageAccounting_RequestedOnBothPaths(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name string
		call func(t *testing.T, client *Client)
		body []byte
	}{
		{
			name: "stream",
			call: func(t *testing.T, client *Client) {
				stream, err := client.Stream(context.Background(), llm.Request{Model: "any"})
				if err != nil {
					t.Fatalf("Stream: %v", err)
				}
				_ = drain(t, stream, 2*time.Second)
			},
		},
		{
			name: "complete",
			call: func(t *testing.T, client *Client) {
				if _, err := client.Complete(context.Background(), llm.Request{Model: "any"}); err != nil {
					t.Fatalf("Complete: %v", err)
				}
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var captured []byte
			server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
				captured, _ = io.ReadAll(request.Body)
				writer.WriteHeader(http.StatusOK)
				_, _ = writer.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
			})

			testCase.call(t, newTestClient(t, server))

			if !bytes.Contains(captured, []byte(`"usage":{"include":true}`)) {
				t.Errorf("request body missing usage accounting: %s", captured)
			}
			var decoded map[string]any
			if err := json.Unmarshal(captured, &decoded); err != nil {
				t.Fatalf("body is not JSON: %v", err)
			}
			usage, ok := decoded["usage"].(map[string]any)
			if !ok || usage["include"] != true {
				t.Errorf("usage block = %v, want {include:true}", decoded["usage"])
			}
		})
	}
}

// TestComplete_ErrorEnvelopeKeepsUsage: a failed turn the provider priced was
// still billed, so the price has to reach the caller with the error.
func TestComplete_ErrorEnvelopeKeepsUsage(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"error":{"code":"server_error","message":"provider blew up"},` +
			`"usage":{"prompt_tokens":12,"completion_tokens":0,"total_tokens":12,"cost":0.00042}}`))
	})

	response, err := newTestClient(t, server).Complete(context.Background(), llm.Request{Model: "any"})
	if !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("err = %v, want ErrServerError", err)
	}
	if response.Usage == nil {
		t.Fatal("usage dropped on the error path")
	}
	if response.Usage.CostUSD != 0.00042 {
		t.Errorf("CostUSD = %v, want 0.00042", response.Usage.CostUSD)
	}
	if response.Usage.PromptTokens != 12 {
		t.Errorf("PromptTokens = %d, want 12", response.Usage.PromptTokens)
	}
}

func TestComplete_CostIsMapped(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],` +
			`"usage":{"prompt_tokens":18,"completion_tokens":4,"total_tokens":22,"cost":0.000066}}`))
	})

	response, err := newTestClient(t, server).Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Usage == nil || response.Usage.CostUSD != 0.000066 {
		t.Fatalf("usage = %+v, want CostUSD 0.000066", response.Usage)
	}
}

// TestStream_ErrorEnvelopeEmitsUsageBeforeError: the price of the failed turn
// arrives on the same chunk as the error envelope.
func TestStream_ErrorEnvelopeEmitsUsageBeforeError(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"partial"}}]}

data: {"id":"g","error":{"code":"server_error","message":"boom"},"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10,"cost":0.00011}}

data: [DONE]
`)
	server := newTestServer(t, sseHandler(body))

	stream, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, stream, 2*time.Second)

	kinds := eventKinds(events)
	usageAt, errorAt := slices.Index(kinds, llm.EventUsage), slices.Index(kinds, llm.EventError)
	if usageAt < 0 {
		t.Fatalf("no usage event; kinds = %v", kinds)
	}
	if errorAt < 0 {
		t.Fatalf("no error event; kinds = %v", kinds)
	}
	if usageAt > errorAt {
		t.Errorf("usage arrived after the error; kinds = %v", kinds)
	}
	if got := events[usageAt].Usage.CostUSD; got != 0.00011 {
		t.Errorf("CostUSD = %v, want 0.00011", got)
	}
	if last := events[len(events)-1].Kind; last != llm.EventEnd {
		t.Errorf("last event = %v, want EventEnd", last)
	}
}

// TestStream_TrailingUsageAfterErrorIsStillEmitted: the scanner used to stop
// on the error envelope, which threw away a price the provider sends one chunk
// later.
func TestStream_TrailingUsageAfterErrorIsStillEmitted(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"partial"}}]}

data: {"id":"g","error":{"code":"server_error","message":"boom"}}

data: {"id":"g","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":1,"total_tokens":10,"cost":0.00013}}

data: [DONE]
`)
	server := newTestServer(t, sseHandler(body))

	stream, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, stream, 2*time.Second)
	kinds := eventKinds(events)

	usageAt := slices.Index(kinds, llm.EventUsage)
	if usageAt < 0 {
		t.Fatalf("trailing usage dropped; kinds = %v", kinds)
	}
	if got := events[usageAt].Usage.CostUSD; got != 0.00013 {
		t.Errorf("CostUSD = %v, want 0.00013", got)
	}
	if errorAt := slices.Index(kinds, llm.EventError); errorAt < 0 || errorAt > usageAt {
		t.Errorf("expected the error before the trailing usage; kinds = %v", kinds)
	}
	if last := events[len(events)-1].Kind; last != llm.EventEnd {
		t.Errorf("last event = %v, want EventEnd", last)
	}
}

// TestStream_TrailingUsageAfterContentFilter covers the second abort shape: a
// finish_reason rather than an error envelope.
func TestStream_TrailingUsageAfterContentFilter(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"draw"}}]}

data: {"id":"g","choices":[{"index":0,"delta":{},"finish_reason":"content_filter"}]}

data: {"id":"g","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":0,"total_tokens":7,"cost":0.00002}}

data: [DONE]
`)
	server := newTestServer(t, sseHandler(body))

	stream, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, stream, 2*time.Second)
	kinds := eventKinds(events)

	usageAt := slices.Index(kinds, llm.EventUsage)
	if usageAt < 0 {
		t.Fatalf("usage after a content filter dropped; kinds = %v", kinds)
	}
	if got := events[usageAt].Usage.CostUSD; got != 0.00002 {
		t.Errorf("CostUSD = %v, want 0.00002", got)
	}
	if err := events[slices.Index(kinds, llm.EventError)].Err; !errors.Is(err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want ErrContentFiltered", err)
	}
}

// TestStream_UsageIsEmittedOnce guards against a provider that starts sending
// cumulative usage snapshots: only the first one is surfaced.
func TestStream_UsageIsEmittedOnce(t *testing.T) {
	t.Parallel()
	body := []byte(`data: {"id":"g","choices":[{"index":0,"delta":{"content":"hi"}}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4,"cost":0.00001}}

data: {"id":"g","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5,"cost":0.00002}}

data: [DONE]
`)
	server := newTestServer(t, sseHandler(body))

	stream, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, stream, 2*time.Second)

	var usages int
	for _, event := range events {
		if event.Kind == llm.EventUsage {
			usages++
		}
	}
	if usages != 1 {
		t.Errorf("usage events = %d, want 1", usages)
	}
}
