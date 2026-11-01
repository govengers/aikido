package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

func TestNewClient_RequiresAPIKey(t *testing.T) {
	t.Parallel()
	if _, err := NewClient(&Options{APIKey: ""}); err == nil {
		t.Error("NewClient with empty APIKey should error")
	}
	if _, err := NewClient(nil); err == nil {
		t.Error("NewClient(nil) should error")
	}
	if _, err := NewClient(&Options{APIKey: "k"}); err != nil {
		t.Errorf("NewClient(valid): unexpected err %v", err)
	}
}

// TestNewClient_DefaultClientBoundsResponseHeaders pins RES-001: without an
// HTTPClient a hung provider fails after the header timeout instead of
// blocking forever, while the overall Timeout stays off for long streams.
func TestNewClient_DefaultClientBoundsResponseHeaders(t *testing.T) {
	t.Parallel()
	client, err := NewClient(&Options{APIKey: "k"})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if client.httpClient.Timeout != 0 {
		t.Errorf("Timeout = %v, want 0 (streams outlive any fixed cap)", client.httpClient.Timeout)
	}
	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T, want *http.Transport", client.httpClient.Transport)
	}
	if transport.ResponseHeaderTimeout != defaultResponseHeaderTimeout {
		t.Errorf("ResponseHeaderTimeout = %v, want %v", transport.ResponseHeaderTimeout, defaultResponseHeaderTimeout)
	}
}

func TestStream_SimpleText(t *testing.T) {
	t.Parallel()
	events := streamBody(t, loadFixture(t, "simple_text.sse"))
	summary := summarize(t, events)

	if len(summary.errors) != 0 {
		t.Errorf("unexpected EventError: %v", summary.errors)
	}
	if len(summary.usages) != 1 {
		t.Fatalf("usage events = %d, want 1", len(summary.usages))
	}
	usage := summary.usages[0]
	if usage.PromptTokens != 18 || usage.CompletionTokens != 4 {
		t.Errorf("usage tokens = %+v", usage)
	}
	if usage.CostUSD == 0 {
		t.Error("usage.CostUSD == 0; want non-zero")
	}
	if summary.text != "Hello, world." {
		t.Errorf("text = %q, want %q", summary.text, "Hello, world.")
	}
	assertEventOrder(t, events)
}

func TestStream_SingleTool(t *testing.T) {
	t.Parallel()
	events := streamBody(t, loadFixture(t, "single_tool.sse"))
	summary := summarize(t, events)

	if summary.text != "Let me check that file." {
		t.Errorf("text = %q", summary.text)
	}
	if len(summary.toolCalls) != 1 {
		t.Fatalf("calls = %d, want 1", len(summary.toolCalls))
	}
	call := summary.toolCalls[0]
	if call.ID != "call_abc" || call.Name != "read_file" {
		t.Errorf("call = %+v", call)
	}
	if call.Arguments != `{"path":"README.md"}` {
		t.Errorf("Arguments = %q, want %q", call.Arguments, `{"path":"README.md"}`)
	}
	assertEventOrder(t, events)
}

func TestStream_MultiToolInterleaved(t *testing.T) {
	t.Parallel()
	events := streamBody(t, loadFixture(t, "multi_tool_interleaved.sse"))
	calls := summarize(t, events).toolCalls

	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2; events=%+v", len(calls), events)
	}
	// In index order: call_a (read_file) first, call_b (list_files) second.
	if calls[0].ID != "call_a" || calls[0].Name != "read_file" || calls[0].Arguments != `{"path":"a.md"}` {
		t.Errorf("calls[0] = %+v", calls[0])
	}
	if calls[1].ID != "call_b" || calls[1].Name != "list_files" || calls[1].Arguments != `{}` {
		t.Errorf("calls[1] = %+v", calls[1])
	}
	if !json.Valid([]byte(calls[0].Arguments)) || !json.Valid([]byte(calls[1].Arguments)) {
		t.Error("tool-call Arguments not valid JSON")
	}
	assertEventOrder(t, events)
}

func TestStream_MidStreamError(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "mid_stream_error.sse")
	var hits atomic.Int32
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		hits.Add(1)
		sseHandler(body)(writer, request)
	})
	stream, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "openai/gpt-4o"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, stream, 2*time.Second)
	summary := summarize(t, events)

	if summary.text != "Working on it..." {
		t.Errorf("text = %q", summary.text)
	}
	if len(summary.errors) != 1 || !errors.Is(summary.errors[0], llm.ErrServerError) {
		t.Errorf("errors = %v, want one matching ErrServerError", summary.errors)
	}
	assertEventOrder(t, events)
	if got := hits.Load(); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on mid-stream error)", got)
	}
}

func TestStream_ContentFilter(t *testing.T) {
	t.Parallel()
	for _, fixture := range []string{"content_filter_finish_reason.sse", "content_filter_error_envelope.sse"} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			summary := summarize(t, streamBody(t, loadFixture(t, fixture)))
			if len(summary.errors) != 1 || !errors.Is(summary.errors[0], llm.ErrContentFiltered) {
				t.Fatalf("errors = %v, want one matching ErrContentFiltered", summary.errors)
			}
			if errors.Is(summary.errors[0], llm.ErrServerError) {
				t.Error("err should NOT match ErrServerError when content-filtered")
			}
			if summary.end == nil || summary.end.FinishReason != finishReasonContentFilter {
				t.Errorf("EventEnd = %+v, want FinishReason %q", summary.end, finishReasonContentFilter)
			}
		})
	}
}

func TestStream_429ThenSuccess(t *testing.T) {
	t.Parallel()
	successBody := loadFixture(t, "success_after_429.sse")
	var (
		hits  atomic.Int32
		first atomic.Int64
		gap   atomic.Int64
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if hits.Add(1) == 1 {
			first.Store(time.Now().UnixNano())
			writer.Header().Set("Content-Type", "application/json")
			writer.Header().Set("Retry-After", "1")
			writer.Header().Set("X-RateLimit-Reset", "1741305600000")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":{"code":429,"message":"Rate limit exceeded","metadata":{}}}`))
			return
		}
		gap.Store(time.Now().UnixNano() - first.Load())
		sseHandler(successBody)(writer, request)
	})
	stream, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	summary := summarize(t, drain(t, stream, 5*time.Second))

	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2 (one 429, one success)", got)
	}
	if waited := time.Duration(gap.Load()); waited < 800*time.Millisecond {
		t.Errorf("gap between requests = %v, want >= ~1s (Retry-After)", waited)
	}
	if len(summary.errors) != 0 {
		t.Errorf("unexpected EventError on retry path: %v", summary.errors)
	}
	if summary.text != "Hello, world." {
		t.Errorf("text = %q", summary.text)
	}
}

func TestStream_Thinking(t *testing.T) {
	t.Parallel()
	summary := summarize(t, streamBody(t, loadFixture(t, "thinking.sse")))
	if !strings.Contains(summary.thinking, "Let me think") {
		t.Errorf("thinking text = %q, want it to contain %q", summary.thinking, "Let me think")
	}
	if summary.text != "42." {
		t.Errorf("content text = %q, want %q", summary.text, "42.")
	}
}

func TestStream_StatusErrors(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{name: "401", status: http.StatusUnauthorized, body: `{"error":{"code":401,"message":"invalid key"}}`, want: llm.ErrAuthentication},
		{name: "400", status: http.StatusBadRequest, body: `{"error":{"code":400,"message":"bad input"}}`, want: llm.ErrInvalidRequest},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			})
			_, err := newTestClient(t, server).Stream(context.Background(), llm.Request{Model: "x"})
			if !errors.Is(err, testCase.want) {
				t.Errorf("err = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestStream_Cancellation(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := newTestClient(t, server).Stream(ctx, llm.Request{Model: "x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	// The producer must exit and close the channel.
	cancel()
	_ = drain(t, stream, 2*time.Second)
}

// TestStream_CancelledMidStreamEmitsNoError pins that cancelling a running
// stream ends it without an EventError: the body read fails because of the
// cancellation, not because the provider dropped the connection.
func TestStream_CancelledMidStreamEmitsNoError(t *testing.T) {
	t.Parallel()
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := newTestClient(t, server).Stream(ctx, llm.Request{Model: "x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if first := <-stream; first.Kind != llm.EventTextDelta {
		t.Fatalf("first event = %s, want text_delta", first.Kind)
	}
	cancel()
	for event := range stream {
		if event.Kind == llm.EventError {
			t.Errorf("EventError after cancellation: %v", event.Err)
		}
	}
}

// TestComplete_CancelledContextIsNotServerError pins RES-002: the caller's
// cancellation is a context error, not a transient ErrServerError that a
// retry wrapper would retry.
func TestComplete_CancelledContextIsNotServerError(t *testing.T) {
	t.Parallel()
	requestArrived := make(chan struct{})
	var hits atomic.Int32
	server := newTestServer(t, func(_ http.ResponseWriter, request *http.Request) {
		// Only a fully read body lets the server notice the client hanging up.
		_, _ = io.Copy(io.Discard, request.Body)
		hits.Add(1)
		close(requestArrived)
		<-request.Context().Done()
	})
	// One attempt: with retry the backoff wait would answer the cancellation
	// itself and hide the mapping of the failed round trip.
	client, err := NewClient(&Options{APIKey: "sk-test", BaseURL: server.URL, MaximumAttempts: 1})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-requestArrived
		cancel()
	}()
	_, err = client.Complete(ctx, llm.Request{Model: "any"})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(context.Canceled)", err)
	}
	if errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v matches ErrServerError; a cancelled call is not a server error", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry after cancellation)", got)
	}
}

// TestStream_RequestBody checks headers and the request body on the wire.
func TestStream_RequestBody(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "simple_text.sse")
	var (
		captured map[string]any
		header   http.Header
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(rawBody, &captured)
		header = request.Header.Clone()
		sseHandler(body)(writer, request)
	})
	client, err := NewClient(&Options{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		HTTPReferer:   "https://example.com",
		XTitle:        "aikido-test",
		ProviderOrder: []string{"anthropic"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	stream, err := client.Stream(context.Background(), llm.Request{
		Model:         "anthropic/claude-sonnet-4.6", // passed through: the caller chooses the canonical form
		MaximumTokens: 1024,
		Temperature:   new(float32(0.7)),
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "you are helpful"},
			{Role: llm.RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = drain(t, stream, 2*time.Second)

	assertHeaders(t, header, map[string]string{
		"Authorization": "Bearer sk-test",
		"Accept":        "text/event-stream",
		"HTTP-Referer":  "https://example.com",
		"X-Title":       "aikido-test",
	})
	assertChatBody(t, captured)
	// Single-entry order locks to one provider: allow_fallbacks=false.
	assertProviderBlock(t, captured, 1, false)
}

func assertHeaders(t *testing.T, header http.Header, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got := header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

func assertChatBody(t *testing.T, captured map[string]any) {
	t.Helper()
	if captured == nil {
		t.Fatal("no request body captured")
	}
	if got := captured["model"]; got != "anthropic/claude-sonnet-4.6" {
		t.Errorf("model = %v, want %q (verbatim, no normalization)", got, "anthropic/claude-sonnet-4.6")
	}
	if captured["stream"] != true {
		t.Errorf("stream = %v, want true", captured["stream"])
	}
	if got := captured["max_tokens"]; got != 1024.0 {
		t.Errorf("max_tokens = %v, want 1024", got)
	}
	// float32 0.7 comes back as a float64 with representation noise.
	if temperature, ok := captured["temperature"].(float64); !ok || temperature < 0.69 || temperature > 0.71 {
		t.Errorf("temperature = %v, want ~0.7", captured["temperature"])
	}
	if messages, ok := captured["messages"].([]any); !ok || len(messages) != 2 {
		t.Errorf("messages malformed: %v", captured["messages"])
	}
}

func assertProviderBlock(t *testing.T, captured map[string]any, wantOrderLength int, wantFallbacks bool) {
	t.Helper()
	provider, ok := captured["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider field missing or wrong shape: %v", captured["provider"])
	}
	if order, ok := provider["order"].([]any); !ok || len(order) != wantOrderLength || order[0] != "anthropic" {
		t.Errorf("provider.order = %v", provider["order"])
	}
	if allowFallbacks, ok := provider["allow_fallbacks"].(bool); !ok || allowFallbacks != wantFallbacks {
		t.Errorf("provider.allow_fallbacks = %v, want %v", provider["allow_fallbacks"], wantFallbacks)
	}
}

// TestStream_ProviderOrderMultiEntry covers the multi-entry case:
// listing more than one provider implies a fallback chain.
func TestStream_ProviderOrderMultiEntry(t *testing.T) {
	t.Parallel()
	body := loadFixture(t, "simple_text.sse")
	var captured map[string]any
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(rawBody, &captured)
		sseHandler(body)(writer, request)
	})
	client, err := NewClient(&Options{
		APIKey:        "sk-test",
		BaseURL:       server.URL,
		ProviderOrder: []string{"anthropic", "openai"},
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	stream, err := client.Stream(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	_ = drain(t, stream, 2*time.Second)

	assertProviderBlock(t, captured, 2, true)
}

// TestStream_IgnoresCommentLines: comment-only chunks
// ("`: OPENROUTER PROCESSING`") do not turn into events.
func TestStream_IgnoresCommentLines(t *testing.T) {
	t.Parallel()
	body := []byte(`: OPENROUTER PROCESSING

data: {"id":"x","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":null}]}

: another comment

data: {"id":"x","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}

data: [DONE]

`)
	if text := summarize(t, streamBody(t, body)).text; text != "hi" {
		t.Errorf("text = %q, want %q", text, "hi")
	}
}

// TestStream_ImageStreamingDelta covers the streaming `delta.images[]` shape
// that image-capable models emit when streaming generated images. The base64
// data URI is decoded into bytes; the remote URL is passed through verbatim.
func TestStream_ImageStreamingDelta(t *testing.T) {
	t.Parallel()
	events := streamBody(t, loadFixture(t, "image_streaming_delta.sse"))
	summary := summarize(t, events)

	if summary.text != "Here is the requested image:" {
		t.Errorf("text = %q", summary.text)
	}
	if len(summary.images) != 2 {
		t.Fatalf("images = %d, want 2", len(summary.images))
	}
	decoded, remote := summary.images[0], summary.images[1]
	if decoded.ContentType != "image/png" || len(decoded.Data) == 0 || decoded.URL != "" {
		t.Errorf("images[0] = %+v, want the data URI decoded into bytes", decoded)
	}
	if remote.URL != "https://cdn.example.com/img/abc123.png" || len(remote.Data) != 0 {
		t.Errorf("images[1] = %+v, want the URL passed through", remote)
	}
	assertEventOrder(t, events)
}

// TestStream_ImageMessageNonStream covers the case where the provider returns
// the entire assistant message in a single chunk with `choices[0].message`
// (rather than `delta`) populated.
func TestStream_ImageMessageNonStream(t *testing.T) {
	t.Parallel()
	images := summarize(t, streamBody(t, loadFixture(t, "image_message_nonstream.sse"))).images
	if len(images) != 1 {
		t.Fatalf("images = %d, want 1", len(images))
	}
	if images[0].ContentType != "image/jpeg" || len(images[0].Data) == 0 {
		t.Errorf("image = %+v, want decoded JPEG bytes", images[0])
	}
}

// TestStream_ImageInlineContent covers the typed-parts-array content shape
// where the image lives inside `message.content` as an `image_url` part
// alongside one or more `text` parts.
func TestStream_ImageInlineContent(t *testing.T) {
	t.Parallel()
	summary := summarize(t, streamBody(t, loadFixture(t, "image_inline_content.sse")))
	if summary.text != "Generated." {
		t.Errorf("text = %q", summary.text)
	}
	if len(summary.images) != 1 {
		t.Fatalf("images = %d, want 1", len(summary.images))
	}
	if summary.images[0].ContentType != "image/webp" || len(summary.images[0].Data) == 0 {
		t.Errorf("images[0] = %+v", summary.images[0])
	}
}
