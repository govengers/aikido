package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/govengers/aikido/llm"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	source := newTokenSource(t, &TokenSourceOptions{Tokens: Tokens{AccessToken: "at", RefreshToken: "rt", AccountID: "acct", ExpiresAt: time.Now().Add(time.Hour)}})
	client, err := NewClient(&Options{Tokens: source, Originator: "casa-mapa", BaseURL: testServer.URL})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func sse(events ...string) string {
	var builder strings.Builder
	for _, event := range events {
		var probe struct{ Type string }
		_ = json.Unmarshal([]byte(event), &probe) // test fixtures are valid JSON
		builder.WriteString("event: " + probe.Type + "\ndata: " + event + "\n\n")
	}
	return builder.String()
}

func TestComplete_RequestShapeAndText(t *testing.T) {
	t.Parallel()
	client := newClient(t, func(writer http.ResponseWriter, request *http.Request) {
		for header, want := range map[string]string{"Authorization": "Bearer at", "ChatGPT-Account-ID": "acct", "originator": "casa-mapa", "Accept": "text/event-stream", "User-Agent": "casa-mapa/aikido"} {
			if got := request.Header.Get(header); got != want {
				t.Errorf("header %s = %q, want %q", header, got, want)
			}
		}
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body) // a broken body fails the checks below
		if request.URL.Path != "/responses" || body["stream"] != true || body["store"] != false || body["instructions"] != "be brief\n\nno markdown" {
			t.Errorf("path %s body %v", request.URL.Path, body)
		}
		if _, found := body["max_output_tokens"]; found {
			t.Error("max_output_tokens must not be sent")
		}
		input := body["input"].([]any)
		content := input[0].(map[string]any)["content"].([]any)
		if image := content[1].(map[string]any); image["type"] != "input_image" || !strings.HasPrefix(image["image_url"].(string), "data:image/png;base64,") {
			t.Errorf("image part = %v", image)
		}
		if reply := input[1].(map[string]any)["content"].([]any)[0].(map[string]any); reply["type"] != "output_text" {
			t.Errorf("assistant part = %v", reply)
		}
		if format := body["text"].(map[string]any)["format"].(map[string]any); format["type"] != "json_schema" || format["name"] != "meal" || format["strict"] != true {
			t.Errorf("text.format = %v", format)
		}
		_, _ = io.WriteString(writer, sse(
			`{"type":"response.output_text.delta","delta":"{\"kcal\":"}`,
			`{"type":"response.output_text.delta","delta":"640}"}`,
			`{"type":"response.output_text.done","text":"{\"kcal\":640}"}`,
			`{"type":"response.completed","response":{"output":[],"usage":{"input_tokens":900,"output_tokens":12,"input_tokens_details":{"cached_tokens":800}}}}`,
		))
	})
	response, err := client.Complete(context.Background(), llm.Request{
		Model: "gpt-5.5",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "be brief"},
			{Role: llm.RoleUser, Content: "what is this?", Images: []llm.ImagePart{{Data: []byte("\x89PNG\r\n\x1a\n0000"), ContentType: "image/png"}}},
			{Role: llm.RoleAssistant, Content: "a meal"},
			{Role: llm.RoleSystem, Content: "no markdown"},
		},
		ResponseFormat: &llm.JSONSchema{Name: "meal", Strict: true, Schema: json.RawMessage(`{"type":"object"}`)},
		MaximumTokens:  500,
	})
	if err != nil || response.Text != `{"kcal":640}` || response.Usage == nil || response.Usage.PromptTokens != 900 || response.Usage.CacheReadTokens != 800 || response.FinishReason != "stop" {
		t.Fatalf("Complete = %+v, %v", response, err)
	}
}

func TestStream_DoneWithoutDeltasAndFailures(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		body      string
		text      string
		finish    string
		wantError error
	}{
		"done only":   {sse(`{"type":"response.output_text.done","text":"hi"}`, `{"type":"response.completed","response":{}}`), "hi", "stop", nil},
		"incomplete":  {sse(`{"type":"response.output_text.delta","delta":"pa"}`, `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`), "pa", "max_output_tokens", nil},
		"failed":      {sse(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`), "", "error", llm.ErrServerError},
		"cut off":     {sse(`{"type":"response.output_text.delta","delta":"par"}`), "par", "error", llm.ErrServerError},
		"error event": {sse(`{"type":"error","code":"content_policy","message":"no"}`), "", "content_filter", llm.ErrContentFiltered},
		"garbage":     {"data: {nope\n\n" + sse(`{"type":"response.completed"}`), "", "stop", nil},
	} {
		client := newClient(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, testCase.body) })
		response, err := client.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
		errorMatches := (testCase.wantError == nil && err == nil) || (testCase.wantError != nil && errors.Is(err, testCase.wantError))
		if response.Text != testCase.text || response.FinishReason != testCase.finish || !errorMatches {
			t.Errorf("%s: text %q finish %q err %v", name, response.Text, response.FinishReason, err)
		}
	}
}

func TestStream_RejectsUnsupportedRequestsAndMapsStatus(t *testing.T) {
	t.Parallel()
	client := newClient(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusBadRequest) })
	user := func(message llm.Message) llm.Request {
		message.Role = llm.RoleUser
		return llm.Request{Messages: []llm.Message{message}}
	}
	for name, testCase := range map[string]struct {
		request     llm.Request
		wantMessage string
	}{
		"tools":      {llm.Request{Tools: []llm.ToolDefinition{{Name: "x"}}}, "tools and thinking"},
		"thinking":   {llm.Request{Thinking: &llm.ThinkingConfiguration{}}, "tools and thinking"},
		"audio":      {user(llm.Message{Audio: []llm.AudioPart{{Data: []byte{1}, Format: "wav"}}}), "audio input is not supported"},
		"tool calls": {user(llm.Message{ToolCalls: []llm.ToolCall{{ID: "c1"}}}), "tool calls are not supported"},
		"tool role":  {llm.Request{Messages: []llm.Message{{Role: llm.RoleTool, Content: "x"}}}, "tool messages are not supported"},
		"status 400": {llm.Request{Model: "m"}, "status 400"},
	} {
		if _, err := client.Stream(context.Background(), testCase.request); !errors.Is(err, llm.ErrInvalidRequest) || !strings.Contains(err.Error(), testCase.wantMessage) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestModels_SkipsHidden(t *testing.T) {
	t.Parallel()
	client := newClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/models" || request.URL.Query().Get("client_version") == "" || request.Header.Get("ChatGPT-Account-ID") != "acct" {
			t.Errorf("models request %s", request.URL)
		}
		_, _ = io.WriteString(writer, `{"models":[{"slug":"gpt-5.5","visibility":"list"},{"slug":"gpt-reserve","visibility":"hide"},{"slug":"gpt-6-astra"}]}`)
	})
	ids, err := client.Models(context.Background())
	if err != nil || strings.Join(ids, ",") != "gpt-5.5,gpt-6-astra" {
		t.Fatalf("Models = %v, %v", ids, err)
	}
}

func TestModels_Failures(t *testing.T) {
	t.Parallel()
	unauthorized := newClient(t, func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusUnauthorized) })
	if _, err := unauthorized.Models(context.Background()); !errors.Is(err, llm.ErrAuthentication) {
		t.Errorf("401: %v", err)
	}
	garbage := newClient(t, func(writer http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(writer, "nope") })
	if _, err := garbage.Models(context.Background()); err == nil {
		t.Error("undecodable models list: no error")
	}
}

// Regression: a stream line over the cap ended the stream as if the body had
// just stopped, hiding the cause.
func TestStream_OversizedLineNamesTheCap(t *testing.T) {
	t.Parallel()
	client := newClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "data: "+strings.Repeat("x", maximumStreamLineBytes)+"\n\n")
	})
	_, err := client.Complete(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x"}}})
	if !errors.Is(err, llm.ErrServerError) || !strings.Contains(err.Error(), bufio.ErrTooLong.Error()) {
		t.Fatalf("err = %v, want a server error naming the line cap", err)
	}
}

// Regression: the models catalog was read without a limit.
func TestModels_RejectsAnOversizedCatalog(t *testing.T) {
	t.Parallel()
	client := newClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"models":[{"slug":"gpt-5.5"}],"padding":"`+strings.Repeat("x", maximumModelsResponseBytes)+`"}`)
	})
	if _, err := client.Models(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v, want an over-cap error", err)
	}
}

func TestReadAtMost(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		body    string
		wantErr bool
	}{
		{"", false},
		{"1234", false},
		{"12345", true},
	} {
		raw, err := readAtMost(strings.NewReader(testCase.body), 4)
		if (err != nil) != testCase.wantErr || (err == nil && string(raw) != testCase.body) {
			t.Errorf("readAtMost(%q, 4) = %q, %v", testCase.body, raw, err)
		}
	}
}

// Regression: a call that fails because its ctx ended reported
// llm.ErrServerError, which callers and retry policies take as transient.
func TestModels_CancelledCallReportsTheContextError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := newClient(t, func(_ http.ResponseWriter, request *http.Request) {
		cancel()
		<-request.Context().Done()
	})
	_, err := client.Models(ctx)
	if !errors.Is(err, context.Canceled) || errors.Is(err, llm.ErrServerError) {
		t.Errorf("err = %v, want context.Canceled and no server error", err)
	}
}

func TestNewClient_Validates(t *testing.T) {
	t.Parallel()
	source := newTokenSource(t, &TokenSourceOptions{Tokens: Tokens{RefreshToken: "rt"}})
	for name, options := range map[string]*Options{
		"nil":           nil,
		"no tokens":     {Originator: "casa-mapa"},
		"no originator": {Tokens: source},
	} {
		if _, err := NewClient(options); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) { panic("transport bug") }

func TestProcessStream_RecoversAPanicAndStillEnds(t *testing.T) {
	t.Parallel()
	events := make(chan llm.Event, streamBufferSize)
	processStream(context.Background(), panickingReader{}, events, time.Now())
	close(events)
	var kinds []llm.EventKind
	var failure error
	for event := range events {
		kinds = append(kinds, event.Kind)
		if event.Kind == llm.EventError {
			failure = event.Err
		}
	}
	if len(kinds) != 2 || kinds[1] != llm.EventEnd || !errors.Is(failure, llm.ErrServerError) {
		t.Errorf("events = %v, error %v", kinds, failure)
	}
}

func TestStreamFailure(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name         string
		event        string
		wantFinish   string
		wantSentinel error
		wantMessage  string
	}{
		{"top-level code", `{"type":"error","code":"rate","message":"slow down"}`, "error", llm.ErrServerError, "rate slow down"},
		{"response error wins", `{"type":"response.failed","code":"x","response":{"error":{"code":"server_error","message":"boom"}}}`, "error", llm.ErrServerError, "server_error boom"},
		{"content policy", `{"type":"error","code":"content_policy_violation"}`, "content_filter", llm.ErrContentFiltered, "content_policy_violation"},
		{"safety", `{"type":"response.failed","response":{"error":{"code":"safety_block"}}}`, "content_filter", llm.ErrContentFiltered, "safety_block"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			event, ok := parseStreamLine("data: " + testCase.event)
			if !ok {
				t.Fatal("fixture does not parse")
			}
			finish, err := streamFailure(event)
			if finish != testCase.wantFinish || !errors.Is(err, testCase.wantSentinel) || !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Errorf("streamFailure = %q, %v", finish, err)
			}
		})
	}
}

func TestParseStreamLine(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		line     string
		wantType string
		wantOK   bool
	}{
		{`data: {"type":"response.completed"}`, "response.completed", true},
		{`data:{"type":"error"}`, "error", true},
		{`event: response.completed`, "", false},
		{`data: [DONE]`, "", false},
		{``, "", false},
	} {
		event, ok := parseStreamLine(testCase.line)
		if ok != testCase.wantOK || event.Type != testCase.wantType {
			t.Errorf("parseStreamLine(%q) = %q, %v", testCase.line, event.Type, ok)
		}
	}
}

func TestImageURL(t *testing.T) {
	t.Parallel()
	png := []byte("\x89PNG\r\n\x1a\n0000")
	for _, testCase := range []struct {
		name    string
		image   llm.ImagePart
		want    string
		wantErr error
	}{
		{"url wins", llm.ImagePart{URL: "https://example.com/a.png", Data: png}, "https://example.com/a.png", nil},
		{"data with type", llm.ImagePart{Data: []byte("x"), ContentType: "image/webp"}, "data:image/webp;base64,eA==", nil},
		{"data sniffed", llm.ImagePart{Data: png}, "data:image/png;base64,iVBORw0KGgowMDAw", nil},
		{"empty", llm.ImagePart{}, "", llm.ErrInvalidRequest},
	} {
		got, err := imageURL(testCase.image)
		if got != testCase.want || !errors.Is(err, testCase.wantErr) {
			t.Errorf("%s: imageURL = %q, %v; want %q, %v", testCase.name, got, err, testCase.want, testCase.wantErr)
		}
	}
	empty := llm.Request{Model: "m", Messages: []llm.Message{{Role: llm.RoleUser, Content: "x", Images: []llm.ImagePart{{}}}}}
	if _, err := buildBody(empty); !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("buildBody with an empty image: err = %v, want llm.ErrInvalidRequest", err)
	}
}

func TestStatusSentinel(t *testing.T) {
	t.Parallel()
	for status, want := range map[int]error{
		http.StatusUnauthorized:        llm.ErrAuthentication,
		http.StatusForbidden:           llm.ErrAuthentication,
		http.StatusPaymentRequired:     llm.ErrAuthentication,
		http.StatusTooManyRequests:     llm.ErrRateLimited,
		http.StatusRequestTimeout:      llm.ErrServerError,
		http.StatusInternalServerError: llm.ErrServerError,
		http.StatusBadGateway:          llm.ErrServerError,
		http.StatusBadRequest:          llm.ErrInvalidRequest,
		http.StatusNotFound:            llm.ErrInvalidRequest,
	} {
		if got := statusSentinel(status); !errors.Is(got, want) {
			t.Errorf("statusSentinel(%d) = %v, want %v", status, got, want)
		}
	}
}
