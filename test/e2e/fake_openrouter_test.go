//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fake serves the endpoints under the same API root as OpenRouter.
const (
	apiRoot            = "/api/v1"
	chatCompletionPath = apiRoot + "/chat/completions"
	imagesPath         = apiRoot + "/images"
)

// scriptedUsage is the usage block every scripted chat answer carries.
const (
	scriptedPromptTokens     = 12
	scriptedCompletionTokens = 7
	scriptedCost             = 0.0012
)

// fakeOpenRouter is an httptest server speaking OpenRouter's wire format. A
// test queues the answers in the order the requests will arrive; the fake
// records every request, so the test can assert what aikido sent. When the
// test ends it fails the test for queued answers nobody asked for and for
// requests nobody scripted.
type fakeOpenRouter struct {
	t       *testing.T
	server  *httptest.Server
	closing chan struct{} // closed at cleanup, releases stalled streams

	mutex      sync.Mutex
	replies    []scriptedReply
	requests   []recordedRequest
	unscripted []string
}

// scriptedReply is one answer of the fake: a streamed SSE answer when
// streamChunks is set, else status, header and body as they are.
type scriptedReply struct {
	path         string
	status       int
	header       map[string]string
	body         string
	streamChunks []string // the data payloads of a streamed answer, flushed one by one
	stall        bool     // after the chunks no [DONE]: hold the stream open until the client leaves
}

// recordedRequest is one request the fake received.
type recordedRequest struct {
	path          string
	authorization string
	receivedAt    time.Time
	body          []byte
}

func newFakeOpenRouter(t *testing.T) *fakeOpenRouter {
	t.Helper()
	fake := &fakeOpenRouter{t: t, closing: make(chan struct{})}
	fake.server = httptest.NewServer(fake)
	t.Cleanup(func() {
		close(fake.closing)
		fake.server.Close()
		fake.verifyScriptUsed()
	})
	return fake
}

// baseURL is the API root to configure as openrouter.Options.BaseURL or
// OPENROUTER_BASE_URL.
func (fake *fakeOpenRouter) baseURL() string {
	return fake.server.URL + apiRoot
}

func (fake *fakeOpenRouter) queue(replies ...scriptedReply) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.replies = append(fake.replies, replies...)
}

// received returns the requests so far, oldest first.
func (fake *fakeOpenRouter) received() []recordedRequest {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	return slices.Clone(fake.requests)
}

// chatRequests decodes every chat-completions request so far, oldest first.
func (fake *fakeOpenRouter) chatRequests() []chatRequestBody {
	fake.t.Helper()
	var decoded []chatRequestBody
	for _, request := range fake.received() {
		if request.path != chatCompletionPath {
			continue
		}
		var body chatRequestBody
		if err := json.Unmarshal(request.body, &body); err != nil {
			fake.t.Fatalf("decode chat request: %v\n%s", err, request.body)
		}
		decoded = append(decoded, body)
	}
	return decoded
}

func (fake *fakeOpenRouter) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := io.ReadAll(request.Body)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	reply, ok := fake.next(request, body)
	if !ok {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":{"code":400,"message":"fake openrouter: unscripted request"}}`)
		return
	}
	if reply.streamChunks != nil {
		fake.stream(writer, request, reply)
		return
	}
	for key, value := range reply.header {
		writer.Header().Set(key, value)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(reply.status)
	_, _ = io.WriteString(writer, reply.body)
}

// next records the request and takes the next scripted reply, if it was
// scripted for this path.
func (fake *fakeOpenRouter) next(request *http.Request, body []byte) (scriptedReply, bool) {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	fake.requests = append(fake.requests, recordedRequest{
		path:          request.URL.Path,
		authorization: request.Header.Get("Authorization"),
		receivedAt:    time.Now(),
		body:          body,
	})
	if len(fake.replies) == 0 || fake.replies[0].path != request.URL.Path {
		fake.unscripted = append(fake.unscripted, request.Method+" "+request.URL.Path)
		return scriptedReply{}, false
	}
	reply := fake.replies[0]
	fake.replies = fake.replies[1:]
	return reply, true
}

// stream writes the chunks as server-sent events, starting with the
// keepalive comment OpenRouter sends while the model warms up.
func (fake *fakeOpenRouter) stream(writer http.ResponseWriter, request *http.Request, reply scriptedReply) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		http.Error(writer, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(writer, ": OPENROUTER PROCESSING\n\n")
	for _, chunk := range reply.streamChunks {
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", chunk)
		flusher.Flush()
	}
	if reply.stall {
		select {
		case <-request.Context().Done():
		case <-fake.closing:
		}
		return
	}
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	flusher.Flush()
}

func (fake *fakeOpenRouter) verifyScriptUsed() {
	fake.mutex.Lock()
	defer fake.mutex.Unlock()
	if len(fake.unscripted) > 0 {
		fake.t.Errorf("fake openrouter got unscripted requests: %v", fake.unscripted)
	}
	if len(fake.replies) > 0 {
		fake.t.Errorf("fake openrouter: %d scripted replies were never requested", len(fake.replies))
	}
}

// scriptedToolCall is one tool call a scripted model answer makes.
type scriptedToolCall struct {
	id        string
	name      string
	arguments string // a JSON object as text
}

// streamedText answers a chat completion with text split into word deltas,
// finish_reason stop and a trailing usage chunk with cost.
func streamedText(text string) scriptedReply {
	chunks := []string{chunk(map[string]any{"role": "assistant", "content": ""}, nil)}
	for _, word := range strings.SplitAfter(text, " ") {
		chunks = append(chunks, chunk(map[string]any{"content": word}, nil))
	}
	chunks = append(chunks, chunk(map[string]any{}, "stop"), usageChunk())
	return scriptedReply{path: chatCompletionPath, streamChunks: chunks}
}

// streamedToolCalls answers a chat completion with tool calls the way
// providers stream them: every call's id and name first, then each call's
// arguments in two fragments, interleaved across the calls, then
// finish_reason tool_calls with the usage on the same chunk.
func streamedToolCalls(calls ...scriptedToolCall) scriptedReply {
	chunks := []string{chunk(map[string]any{"role": "assistant", "content": ""}, nil)}
	for index, call := range calls {
		chunks = append(chunks, toolCallChunk(index, map[string]any{
			"id": call.id, "type": "function",
			"function": map[string]any{"name": call.name, "arguments": ""},
		}))
	}
	for half := range 2 {
		for index, call := range calls {
			middle := len(call.arguments) / 2
			fragment := call.arguments[:middle]
			if half == 1 {
				fragment = call.arguments[middle:]
			}
			chunks = append(chunks, toolCallChunk(index, map[string]any{
				"function": map[string]any{"arguments": fragment},
			}))
		}
	}
	finish := map[string]any{
		"id":      "gen-e2e",
		"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
		"usage":   usageBlock(),
	}
	chunks = append(chunks, marshal(finish))
	return scriptedReply{path: chatCompletionPath, streamChunks: chunks}
}

// stalledText streams the first words of an answer and then holds the
// stream open without finishing it, until the client goes away.
func stalledText(text string) scriptedReply {
	return scriptedReply{
		path: chatCompletionPath,
		streamChunks: []string{
			chunk(map[string]any{"role": "assistant", "content": ""}, nil),
			chunk(map[string]any{"content": text}, nil),
		},
		stall: true,
	}
}

// completedWithImage answers a non-streaming chat completion with text and
// one image as a data URL, the way the Gemini image models do.
func completedWithImage(text string, image []byte) scriptedReply {
	body := map[string]any{
		"id":     "gen-e2e-image",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": text,
				"images": []any{map[string]any{
					"type":      "image_url",
					"image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(image)},
				}},
			},
			"finish_reason": "stop",
		}},
		"usage": usageBlock(),
	}
	return scriptedReply{path: chatCompletionPath, status: http.StatusOK, body: marshal(body)}
}

// generatedImage answers the images endpoint with one base64 image.
func generatedImage(image []byte) scriptedReply {
	body := map[string]any{
		"created": time.Now().Unix(),
		"data": []any{map[string]any{
			"b64_json":   base64.StdEncoding.EncodeToString(image),
			"media_type": "image/png",
		}},
		"usage": usageBlock(),
	}
	return scriptedReply{path: imagesPath, status: http.StatusOK, body: marshal(body)}
}

// failedChat answers a chat completion with an HTTP error status and
// OpenRouter's error envelope; retryAfter, when set, becomes the Retry-After
// header.
func failedChat(status int, retryAfter, message string) scriptedReply {
	reply := scriptedReply{
		path:   chatCompletionPath,
		status: status,
		body:   marshal(map[string]any{"error": map[string]any{"code": status, "message": message}}),
	}
	if retryAfter != "" {
		reply.header = map[string]string{"Retry-After": retryAfter}
	}
	return reply
}

func chunk(delta map[string]any, finishReason any) string {
	return marshal(map[string]any{
		"id":      "gen-e2e",
		"object":  "chat.completion.chunk",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
	})
}

func toolCallChunk(index int, fragment map[string]any) string {
	fragment["index"] = index
	return chunk(map[string]any{"tool_calls": []any{fragment}}, nil)
}

func usageChunk() string {
	return marshal(map[string]any{"id": "gen-e2e", "choices": []any{}, "usage": usageBlock()})
}

func usageBlock() map[string]any {
	return map[string]any{
		"prompt_tokens":     scriptedPromptTokens,
		"completion_tokens": scriptedCompletionTokens,
		"total_tokens":      scriptedPromptTokens + scriptedCompletionTokens,
		"cost":              scriptedCost,
	}
}

// marshal encodes the fake's own literals, which cannot fail to encode.
func marshal(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// chatRequestBody is the part of a chat-completions request the tests check.
type chatRequestBody struct {
	Model      string        `json:"model"`
	Stream     bool          `json:"stream"`
	Modalities []string      `json:"modalities"`
	Messages   []wireMessage `json:"messages"`
	Tools      []wireTool    `json:"tools"`
}

type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []wireToolCall  `json:"tool_calls"`
}

// contentPart is one part of a message's content array.
type contentPart struct {
	Text         string `json:"text"`
	CacheControl *struct {
		Type string `json:"type"`
	} `json:"cache_control"`
}

type wireToolCall struct {
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type wireTool struct {
	Function struct {
		Name string `json:"name"`
	} `json:"function"`
}

// text is the message content as text: a plain string, or the text parts
// joined.
func (message wireMessage) text() string {
	var plain string
	if json.Unmarshal(message.Content, &plain) == nil {
		return plain
	}
	var parts []contentPart
	_ = json.Unmarshal(message.Content, &parts) // not parts either: no text
	var joined strings.Builder
	for _, part := range parts {
		joined.WriteString(part.Text)
	}
	return joined.String()
}

// cacheBreakpoint reports whether the message is a prompt-cache breakpoint:
// a content part carrying cache_control of type ephemeral.
func (message wireMessage) cacheBreakpoint() bool {
	var parts []contentPart
	if json.Unmarshal(message.Content, &parts) != nil {
		return false // plain string content has no part to carry it
	}
	return slices.ContainsFunc(parts, func(part contentPart) bool {
		return part.CacheControl != nil && part.CacheControl.Type == "ephemeral"
	})
}

func (body chatRequestBody) toolNames() []string {
	names := make([]string, 0, len(body.Tools))
	for _, tool := range body.Tools {
		names = append(names, tool.Function.Name)
	}
	return names
}

// toolResult returns the content of the tool message answering callID.
func (body chatRequestBody) toolResult(callID string) (string, bool) {
	for _, message := range body.Messages {
		if message.Role == "tool" && message.ToolCallID == callID {
			return message.text(), true
		}
	}
	return "", false
}

// lastUserText is the content of the last user message.
func (body chatRequestBody) lastUserText() string {
	for _, message := range slices.Backward(body.Messages) {
		if message.Role == "user" {
			return message.text()
		}
	}
	return ""
}
