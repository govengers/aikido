package openrouter

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/retry"
)

func TestComplete_SimpleText(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"id": "gen-test",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "Hello, world."},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 18, "completion_tokens": 4, "total_tokens": 22, "cost": 0.00012}
	}`)
	server := newTestServer(t, jsonHandler(body))
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "Hello, world." {
		t.Errorf("text = %q, want %q", response.Text, "Hello, world.")
	}
	if response.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want %q", response.FinishReason, "stop")
	}
	if response.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if response.Usage.PromptTokens != 18 || response.Usage.CompletionTokens != 4 {
		t.Errorf("usage = %+v", response.Usage)
	}
	if response.Usage.CostUSD == 0 {
		t.Error("CostUSD == 0; want non-zero")
	}
}

// TestComplete_LargeImage proves Complete handles a single response carrying
// well over 1 MiB of base64 image data — exactly the case that breaks Stream
// because of the SSE per-line scanner cap. Generates ~3 MiB of base64 payload
// (≈2.25 MB raw "PNG"), wraps it in the OpenRouter image-output envelope, and
// asserts the bytes round-trip end-to-end.
func TestComplete_LargeImage(t *testing.T) {
	t.Parallel()

	// 2.25 MB random-ish PNG-shaped bytes (signature + filler). Base64 of this
	// alone is ~3 MB — comfortably above the 1 MiB SSE line cap that breaks
	// Stream-based image-gen against real Gemini responses.
	const rawSize = 2_250_000
	rawData := make([]byte, rawSize)
	// Plausible PNG signature so a downstream sniffer wouldn't reject it.
	copy(rawData, []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	// Fill the rest with non-zero bytes so base64 doesn't compress to nothing.
	for index := 8; index < rawSize; index++ {
		rawData[index] = byte(index % 251)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawData)

	envelope := map[string]any{
		"id":     "gen-image-test",
		"object": "chat.completion",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "Here is your image.",
				"images": []map[string]any{{
					"type":      "image_url",
					"image_url": map[string]any{"url": dataURI},
				}},
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     18,
			"completion_tokens": 1290,
			"total_tokens":      1308,
			"cost":              0.00038,
		},
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	// Sanity check: envelope must comfortably exceed the 1 MiB SSE per-line cap
	// to prove the non-streaming path handles what Stream cannot.
	if len(body) < 1500*1024 {
		t.Fatalf("envelope size = %d, want > 1.5 MiB to exercise the SSE cap", len(body))
	}

	server := newTestServer(t, jsonHandler(body))
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "Here is your image." {
		t.Errorf("text = %q", response.Text)
	}
	if len(response.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(response.Images))
	}
	got := response.Images[0]
	if got.ContentType != "image/png" {
		t.Errorf("contentType = %q, want image/png", got.ContentType)
	}
	if !bytes.Equal(got.Data, rawData) {
		t.Errorf("image bytes round-trip failed: got %d bytes, want %d", len(got.Data), len(rawData))
	}
	if response.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", response.FinishReason)
	}
}

// TestComplete_LargeImage_ProvesStreamingBreaks pins the failure mode the
// non-streaming path was added to fix: feeding the same large image envelope
// through the SSE scanner deterministically returns ErrServerError. If aikido
// ever bumps the per-line buffer to handle arbitrary sizes, this test will
// flag the change so the contract is reconsidered.
func TestComplete_LargeImage_ProvesStreamingBreaks(t *testing.T) {
	t.Parallel()
	// 2.25 MB raw → ~3 MB base64 — same setup as TestComplete_LargeImage.
	const rawSize = 2_250_000
	rawData := make([]byte, rawSize)
	for index := range rawData {
		rawData[index] = byte(index % 251)
	}
	dataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawData)
	envelope := map[string]any{
		"id":     "gen-image-test",
		"object": "chat.completion.chunk",
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"role": "assistant",
				"images": []map[string]any{{
					"type":      "image_url",
					"image_url": map[string]any{"url": dataURI},
				}},
			},
			"finish_reason": "stop",
		}},
	}
	chunkJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	// SSE wrapping: one `data: <json>\n\n` line then [DONE].
	sseBody := []byte("data: ")
	sseBody = append(sseBody, chunkJSON...)
	sseBody = append(sseBody, []byte("\n\ndata: [DONE]\n\n")...)

	server := newTestServer(t, sseHandler(sseBody))
	client := newTestClient(t, server)

	stream, err := client.Stream(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	events := drain(t, stream, 5*time.Second)

	var sawServerError bool
	for _, event := range events {
		if event.Kind == llm.EventError && errors.Is(event.Err, llm.ErrServerError) {
			sawServerError = true
		}
	}
	if !sawServerError {
		t.Errorf("expected SSE path to fail with ErrServerError on >1 MiB line, but it succeeded; events=%d", len(events))
	}
}

func TestComplete_ToolCalls(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"id": "gen-tool",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{\"path\":\"a.md\"}"}}
				]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 30, "completion_tokens": 12, "total_tokens": 42}
	}`)
	server := newTestServer(t, jsonHandler(body))
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{Model: "anthropic/claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(response.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %d, want 1", len(response.ToolCalls))
	}
	call := response.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "read_file" {
		t.Errorf("tool_call = %+v", call)
	}
	if call.Arguments != `{"path":"a.md"}` {
		t.Errorf("tool_call.Arguments = %q", call.Arguments)
	}
	if !json.Valid([]byte(call.Arguments)) {
		t.Error("tool_call.Arguments not valid JSON")
	}
	if response.FinishReason != "tool_calls" {
		t.Errorf("finish_reason = %q", response.FinishReason)
	}
}

func TestComplete_ContentFilterFinishReason(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"id": "gen-cf",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": ""},
			"finish_reason": "content_filter"
		}]
	}`)
	server := newTestServer(t, jsonHandler(body))
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err == nil {
		t.Fatal("expected error for finish_reason=content_filter")
	}
	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want errors.Is(ErrContentFiltered)", err)
	}
	if errors.Is(err, llm.ErrServerError) {
		t.Errorf("err should NOT match ErrServerError when content-filtered")
	}
	// Response is preserved so callers can log usage even on filtered turns.
	if response.FinishReason != "content_filter" {
		t.Errorf("response.FinishReason = %q", response.FinishReason)
	}
}

func TestComplete_ContentFilterErrorEnvelope(t *testing.T) {
	t.Parallel()
	body := []byte(`{
		"error": {
			"code": "content_policy_violation",
			"message": "Content was blocked by safety policy"
		}
	}`)
	server := newTestServer(t, jsonHandler(body))
	client := newTestClient(t, server)

	_, err := client.Complete(context.Background(), llm.Request{Model: "google/gemini-3.1-flash-image-preview"})
	if err == nil {
		t.Fatal("expected error for content-filter envelope")
	}
	if !errors.Is(err, llm.ErrContentFiltered) {
		t.Errorf("err = %v, want errors.Is(ErrContentFiltered)", err)
	}
}

func TestComplete_5xxRetryThenSuccess(t *testing.T) {
	t.Parallel()
	successBody := []byte(`{
		"id": "gen-after-503",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "ok"},
			"finish_reason": "stop"
		}]
	}`)
	var hits int32
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(successBody)
	})
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" {
		t.Errorf("text = %q", response.Text)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("hits = %d, want 2 (one 503 + one success)", got)
	}
}

func TestComplete_4xxNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&hits, 1)
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	client := newTestClient(t, server)

	_, err := client.Complete(context.Background(), llm.Request{Model: "any"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !errors.Is(err, llm.ErrAuthentication) {
		t.Errorf("err = %v, want ErrAuth", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (no retry on 401)", got)
	}
}

func TestComplete_RequestBodyHasStreamFalse(t *testing.T) {
	t.Parallel()
	var captured []byte
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		captured, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`))
	})
	client := newTestClient(t, server)

	if _, err := client.Complete(context.Background(), llm.Request{
		Model:    "any",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// stream:true must not be on the wire when calling Complete; omitempty on
	// the false case means the field is absent entirely.
	if bytes.Contains(captured, []byte(`"stream":true`)) {
		t.Errorf("body contains stream:true; want absent. body=%s", captured)
	}
}

func TestComplete_ImageConfigOnWire(t *testing.T) {
	t.Parallel()
	var captured []byte
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		captured, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	})
	client := newTestClient(t, server)

	if _, err := client.Complete(context.Background(), llm.Request{
		Model:              "any",
		Messages:           []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
		Modalities:         []string{"image", "text"},
		ImageConfiguration: &llm.ImageConfiguration{AspectRatio: "16:9", ImageSize: "2K"},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !bytes.Contains(captured, []byte(`"image_config":{"aspect_ratio":"16:9","image_size":"2K"}`)) {
		t.Errorf("image_config not on wire. body=%s", captured)
	}

	// Nil ImageConfig must omit the field entirely.
	captured = nil
	if _, err := client.Complete(context.Background(), llm.Request{
		Model:    "any",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if bytes.Contains(captured, []byte(`image_config`)) {
		t.Errorf("image_config leaked into body when unset. body=%s", captured)
	}
}

func TestComplete_TypedPartsImageInContent(t *testing.T) {
	t.Parallel()
	// Some image-capable models emit the image as a typed-parts content array
	// instead of (or in addition to) the dedicated images[] field.
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkAAIAAAoAAggA9GkAAAAASUVORK5CYII="
	body := fmt.Appendf(nil, `{
		"id": "gen-typed-parts",
		"object": "chat.completion",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": [
					{"type": "text", "text": "Done."},
					{"type": "image_url", "image_url": {"url": "data:image/png;base64,%s"}}
				]
			},
			"finish_reason": "stop"
		}]
	}`, png)
	server := newTestServer(t, jsonHandler(body))
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "Done." {
		t.Errorf("text = %q, want %q", response.Text, "Done.")
	}
	if len(response.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(response.Images))
	}
	if response.Images[0].ContentType != "image/png" {
		t.Errorf("contentType = %q", response.Images[0].ContentType)
	}
	if len(response.Images[0].Data) == 0 {
		t.Error("image data is empty; want decoded base64 bytes")
	}
}

func TestComplete_NetworkError(t *testing.T) {
	t.Parallel()
	// Server that closes the connection without responding.
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Fatal("ResponseWriter does not support Hijacker")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()
	})
	client := newTestClient(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := client.Complete(ctx, llm.Request{Model: "any"})
	if err == nil {
		t.Fatal("expected network error")
	}
	if !errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v, want errors.Is(ErrServerError)", err)
	}
}

// TestMalformedSuccessBodyIsNotTransient pins RES-002 for a 2xx that does not
// decode: ErrInvalidResponse, not ErrServerError, so neither the client nor
// CompleteWithRetry sends (and bills) the request again.
func TestMalformedSuccessBodyIsNotTransient(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name string
		call func(client *Client) error
	}{
		{name: "complete", call: func(client *Client) error {
			_, err := llm.CompleteWithRetry(context.Background(), client, llm.Request{Model: "m"}, retry.Policy{MaximumAttempts: 3})
			return err
		}},
		{name: "images", call: func(client *Client) error {
			_, err := client.GenerateImage(context.Background(), llm.ImageRequest{Model: "m", Prompt: "p"})
			return err
		}},
		{name: "decisions", call: func(client *Client) error {
			_, err := client.Decide(context.Background(), llm.DecisionRequest{Model: "m", State: "s", Questions: map[string]llm.DecisionQuestion{"q": llm.NoulQuestion("q?")}})
			return err
		}},
		{name: "models", call: func(client *Client) error {
			_, err := client.Models(context.Background())
			return err
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				_, _ = writer.Write([]byte(`{"this is not valid json`))
			})
			err := testCase.call(newTestClient(t, server))
			if !errors.Is(err, llm.ErrInvalidResponse) || llm.IsTransientServerError(err) {
				t.Errorf("err = %v, want ErrInvalidResponse and not transient", err)
			}
			if got := requests.Load(); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
		})
	}
}

func TestComplete_WireContract(t *testing.T) {
	t.Parallel()
	var (
		paths   []string
		headers []http.Header
		bodies  [][]byte
		hits    int32
	)
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		paths = append(paths, request.URL.Path)
		headers = append(headers, request.Header.Clone())
		bodies = append(bodies, rawBody)
		if atomic.AddInt32(&hits, 1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"message":"upstream down"}}`))
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`))
	})

	client, err := NewClient(&Options{
		APIKey:      "sk-test",
		BaseURL:     server.URL,
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido CLI",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Complete(context.Background(), llm.Request{
		Model:    "any",
		Messages: []llm.Message{{Role: llm.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("attempts = %d, want 2", len(paths))
	}
	for attempt, path := range paths {
		if path != "/chat/completions" {
			t.Errorf("attempt %d path = %q, want /chat/completions", attempt, path)
		}
	}
	for attempt, header := range headers {
		if got := header.Get("Accept"); got != "application/json" {
			t.Errorf("attempt %d Accept = %q", attempt, got)
		}
		if got := header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("attempt %d Authorization = %q", attempt, got)
		}
		if got := header.Get("HTTP-Referer"); got != "https://github.com/govengers/aikido" {
			t.Errorf("attempt %d HTTP-Referer = %q", attempt, got)
		}
		if got := header.Get("X-Title"); got != "aikido CLI" {
			t.Errorf("attempt %d X-Title = %q", attempt, got)
		}
	}
	if string(bodies[0]) != string(bodies[1]) {
		t.Errorf("retry sent a different body:\n%s\n%s", bodies[0], bodies[1])
	}
}

func TestComplete_ErrorEnvelopeNoRetry(t *testing.T) {
	t.Parallel()
	var hits int32
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		atomic.AddInt32(&hits, 1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"error":{"code":"server_error","message":"provider blew up"}}`))
	})
	client := newTestClient(t, server)

	_, err := client.Complete(context.Background(), llm.Request{Model: "any"})
	if !errors.Is(err, llm.ErrServerError) {
		t.Fatalf("err = %v, want ErrServerError", err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("hits = %d, want 1 (a 200 envelope is parsed outside the retry loop)", got)
	}
}

// trackedBody fails mid-read when failAt is reached and records its Close.
type trackedBody struct {
	data     []byte
	position int
	failAt   int
	closed   bool
}

func (b *trackedBody) Read(buffer []byte) (int, error) {
	if b.failAt >= 0 && b.position >= b.failAt {
		return 0, errors.New("connection reset mid-body")
	}
	if b.position >= len(b.data) {
		return 0, io.EOF
	}
	count := copy(buffer, b.data[b.position:])
	if b.failAt >= 0 && b.position+count > b.failAt {
		count = b.failAt - b.position
	}
	b.position += count
	return count, nil
}

func (b *trackedBody) Close() error {
	b.closed = true
	return nil
}

// recordingTransport serves canned responses in order and keeps the request
// bytes so the caller can assert body identity across retries.
type recordingTransport struct {
	bodies   [][]byte
	attempts int
	respond  func(attempt int) *http.Response
}

func (rt *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	rawBody, _ := io.ReadAll(request.Body)
	rt.bodies = append(rt.bodies, rawBody)
	rt.attempts++
	return rt.respond(rt.attempts), nil
}

// TestPostJSON_ReadFailureRetriesAndClosesBody pins the read-failure branch:
// a body that dies mid-read is an ErrServerError (so it retries), the retry
// resends identical bytes, and every response body is closed.
func TestPostJSON_ReadFailureRetriesAndClosesBody(t *testing.T) {
	t.Parallel()
	first := &trackedBody{data: []byte(`{"id":"x","choices":[]}`), failAt: 4}
	second := &trackedBody{data: []byte(`{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"ok"}}]}`), failAt: -1}
	// Snapshotted when attempt two starts, so the assertion is "closed before
	// the retry" and not merely "closed eventually".
	var firstClosedAtRetry bool
	transport := &recordingTransport{respond: func(attempt int) *http.Response {
		body := io.ReadCloser(second)
		if attempt == 1 {
			body = first
		} else {
			firstClosedAtRetry = first.closed
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       body,
		}
	}}
	client, err := NewClient(&Options{APIKey: "sk-test", BaseURL: "https://example.invalid/api/v1", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	response, err := client.Complete(context.Background(), llm.Request{Model: "any"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if response.Text != "ok" {
		t.Errorf("text = %q, want ok", response.Text)
	}
	if transport.attempts != 2 {
		t.Fatalf("attempts = %d, want 2", transport.attempts)
	}
	if string(transport.bodies[0]) != string(transport.bodies[1]) {
		t.Errorf("retry sent a different body:\n%s\n%s", transport.bodies[0], transport.bodies[1])
	}
	if !firstClosedAtRetry {
		t.Error("first response body was still open when attempt two started")
	}
	if !second.closed {
		t.Error("last response body was not closed")
	}
}

// TestComplete_OversizedBodyIsNotRead pins the response cap: a declared
// length above maximumResponseBytes is refused before reading, as
// ErrInvalidResponse, and not retried.
func TestComplete_OversizedBodyIsNotRead(t *testing.T) {
	t.Parallel()
	transport := &recordingTransport{respond: func(int) *http.Response {
		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: maximumResponseBytes + 1,
			Body:          io.NopCloser(bytes.NewReader([]byte(`{}`))),
		}
	}}
	client, err := NewClient(&Options{APIKey: "sk-test", BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	_, err = client.Complete(context.Background(), llm.Request{Model: "any"})
	if !errors.Is(err, llm.ErrInvalidResponse) {
		t.Errorf("err = %v, want ErrInvalidResponse", err)
	}
	if transport.attempts != 1 {
		t.Errorf("attempts = %d, want 1", transport.attempts)
	}
}

// TestReadBounded pins the cap on bodies without a declared length.
func TestReadBounded(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "below the cap", body: "1234567"},
		{name: "at the cap", body: "12345678"},
		{name: "above the cap", body: "123456789", wantErr: llm.ErrInvalidResponse},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := &http.Response{ContentLength: -1, Body: io.NopCloser(strings.NewReader(testCase.body))}
			raw, err := readBounded(context.Background(), response, 8)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("err = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantErr == nil && string(raw) != testCase.body {
				t.Errorf("raw = %q, want %q", raw, testCase.body)
			}
		})
	}
}

// TestComplete_ChatImageFixture replays a recorded Gemini image response and
// asserts the chat path still carries modalities, image_config and reference
// images on the wire.
func TestComplete_ChatImageFixture(t *testing.T) {
	t.Parallel()
	var captured []byte
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		captured, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(loadFixture(t, "chat_image_gemini_nonstream.json"))
	})
	client := newTestClient(t, server)

	response, err := client.Complete(context.Background(), llm.Request{
		Model: "google/gemini-3.1-flash-image-preview",
		Messages: []llm.Message{{
			Role:    llm.RoleUser,
			Content: "a red cube on white",
			Images:  []llm.ImagePart{{URL: "data:image/jpeg;base64,QUJD", ContentType: "image/jpeg"}},
		}},
		Modalities:         []string{"image", "text"},
		ImageConfiguration: &llm.ImageConfiguration{AspectRatio: "16:9", ImageSize: "2K"},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(response.Images) != 1 || len(response.Images[0].Data) == 0 {
		t.Fatalf("images = %+v, want one inline image", response.Images)
	}
	if !isPNG(response.Images[0].Data) {
		t.Errorf("decoded bytes are not a PNG: %x", response.Images[0].Data[:8])
	}
	if response.Text != "Here is the image." {
		t.Errorf("text = %q", response.Text)
	}

	var body struct {
		Modalities  []string `json:"modalities"`
		ImageConfig *struct {
			AspectRatio string `json:"aspect_ratio"`
			ImageSize   string `json:"image_size"`
		} `json:"image_config"`
		Messages []struct {
			Content []struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				ImageURL *struct {
					URL string `json:"url"`
				} `json:"image_url"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(captured, &body); err != nil {
		t.Fatalf("decode request: %v (%s)", err, captured)
	}
	if len(body.Modalities) != 2 || body.Modalities[0] != "image" {
		t.Errorf("modalities = %v", body.Modalities)
	}
	if body.ImageConfig == nil || body.ImageConfig.AspectRatio != "16:9" || body.ImageConfig.ImageSize != "2K" {
		t.Errorf("image_config = %+v", body.ImageConfig)
	}
	if len(body.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(body.Messages))
	}
	parts := body.Messages[0].Content
	if len(parts) != 2 || parts[1].ImageURL == nil || parts[1].ImageURL.URL != "data:image/jpeg;base64,QUJD" {
		t.Errorf("content parts = %+v", parts)
	}
}
