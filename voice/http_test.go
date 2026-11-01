package voice

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/memory"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/tools"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func newTestHandler(t *testing.T, transcriber, chat llm.Client, maximumBytes int64) (*Handler, func() []llm.Message) {
	t.Helper()
	pipeline := newPipeline(t, &Options{Transcriber: transcriber, Speech: &fakeSpeech{}})
	session, history := newSession(t, chat)
	handler, err := NewHandler(&HandlerOptions{
		Pipeline:          pipeline,
		Session:           func(*http.Request) (Runner, error) { return session, nil },
		MaximumAudioBytes: maximumBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, func() []llm.Message {
		messages, err := history.Read(context.Background(), "voice-test")
		if err != nil {
			t.Errorf("read history: %v", err)
		}
		return messages
	}
}

func newPost(t *testing.T, target, contentType string, body io.Reader) *http.Request {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, target, body)
	request.Header.Set("Content-Type", contentType)
	return request
}

func readLines(t *testing.T, body *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		var line map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("line %q is not JSON: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

func TestHandler_TextInSpeechOut_NDJSON(t *testing.T) {
	t.Parallel()
	handler, _ := newTestHandler(t, nil, llmtest.NewStubClient(replyTurn(0.002, firstAnswer+secondAnswer)), 0)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Hallo","output":"speech"}`)))

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("status %d, type %q: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body)
	}
	lines := readLines(t, recorder.Body)
	var types []string
	for _, line := range lines {
		types = append(types, line["type"].(string))
	}
	if got := strings.Join(types, ","); got != "text,speech,speech,done" {
		t.Errorf("types = %s", got)
	}
	speech := lines[1]
	audio, err := base64.StdEncoding.DecodeString(speech["audio"].(string))
	if err != nil || speech["seq"] != float64(0) || speech["content_type"] != "audio/mpeg" || string(audio) != "audio:"+strings.TrimSpace(firstAnswer) {
		t.Errorf("speech line = %v (%v)", speech, err)
	}
	done := lines[len(lines)-1]
	usage := done["usage"].(map[string]any)
	if done["end_reason"] != "stop" || usage["llm_cost_usd"] != 0.002 || usage["speech_chars"] == float64(0) {
		t.Errorf("done line = %v", done)
	}
	if calls, _ := usage["llm_calls"].([]any); len(calls) != 1 || calls[0].(map[string]any)["cost_usd"] != 0.002 {
		t.Errorf("llm_calls = %v; want one call", usage["llm_calls"])
	}
}

func multipartAudio(t *testing.T, withAudio bool) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	if withAudio {
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="audio"; filename="clip.webm"`)
		header.Set("Content-Type", "audio/webm;codecs=opus")
		part, err := form.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("webm-bytes")); err != nil {
			t.Fatal(err)
		}
	} else if err := form.WriteField("text", "Wo kommt die Dusche hin?"); err != nil {
		t.Fatal(err)
	}
	for field, value := range map[string]string{"output": "text", "language": "de"} {
		if err := form.WriteField(field, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, form.FormDataContentType()
}

func TestHandler_AudioUploads(t *testing.T) {
	t.Parallel()
	for name, build := range map[string]func(t *testing.T) *http.Request{
		"multipart": func(t *testing.T) *http.Request {
			body, contentType := multipartAudio(t, true)
			return newPost(t, "/voice", contentType, body)
		},
		"raw body": func(t *testing.T) *http.Request {
			return newPost(t, "/voice?output=text&language=de", "audio/webm;codecs=opus", strings.NewReader("webm-bytes"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			transcriber := llmtest.NewStubClient(transcriptTurn("Wo kommt die Dusche hin?"))
			handler, _ := newTestHandler(t, transcriber, llmtest.NewStubClient(replyTurn(0, "An die Wand.")), 0)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, build(t))
			lines := readLines(t, recorder.Body)
			if recorder.Code != http.StatusOK || len(lines) < 3 || lines[0]["type"] != "transcript" || lines[0]["text"] != "Wo kommt die Dusche hin?" {
				t.Fatalf("status %d, lines %v", recorder.Code, lines)
			}
			audio := transcriber.Requests()[0].Messages[0].Audio[0]
			if string(audio.Data) != "webm-bytes" || audio.Format != "webm" {
				t.Errorf("transcriber got %+v", audio)
			}
		})
	}
}

func TestHandler_MultipartTextTurn(t *testing.T) {
	t.Parallel()
	handler, history := newTestHandler(t, nil, llmtest.NewStubClient(replyTurn(0, "An die Wand.")), 0)
	body, contentType := multipartAudio(t, false)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newPost(t, "/voice", contentType, body))
	if lines := readLines(t, recorder.Body); recorder.Code != http.StatusOK || lines[len(lines)-1]["end_reason"] != "stop" {
		t.Fatalf("status %d, lines %v", recorder.Code, lines)
	}
	if messages := history(); len(messages) != 2 || messages[0].Content != "Wo kommt die Dusche hin?" {
		t.Errorf("history = %+v", messages)
	}
}

func TestHandler_RejectsBadRequests(t *testing.T) {
	t.Parallel()
	handler, _ := newTestHandler(t, llmtest.NewStubClient(), llmtest.NewStubClient(), 64)
	for name, testCase := range map[string]struct {
		method, contentType, body string
		want                      int
	}{
		"GET":            {http.MethodGet, "", "", http.StatusMethodNotAllowed},
		"too large":      {http.MethodPost, "audio/webm", strings.Repeat("x", 65), http.StatusRequestEntityTooLarge},
		"too large json": {http.MethodPost, "application/json", `{"text":"` + strings.Repeat("x", 65) + `"}`, http.StatusRequestEntityTooLarge},
		"unknown audio":  {http.MethodPost, "audio/midi", "x", http.StatusUnsupportedMediaType},
		"unknown body":   {http.MethodPost, "text/plain", "hi", http.StatusUnsupportedMediaType},
		"broken json":    {http.MethodPost, "application/json", `{"text":`, http.StatusBadRequest},
		"broken form":    {http.MethodPost, "multipart/form-data; boundary=x", "nope", http.StatusBadRequest},
		"empty text":     {http.MethodPost, "application/json", `{"text":""}`, http.StatusBadRequest},
		"unknown output": {http.MethodPost, "application/json", `{"text":"x","output":"video"}`, http.StatusBadRequest},
	} {
		request := httptest.NewRequestWithContext(t.Context(), testCase.method, "/voice", strings.NewReader(testCase.body))
		request.Header.Set("Content-Type", testCase.contentType)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != testCase.want || !strings.Contains(recorder.Body.String(), `"error"`) {
			t.Errorf("%s: %d %s, want %d", name, recorder.Code, recorder.Body, testCase.want)
		}
	}
}

func TestHandler_SessionErrorAnswers400(t *testing.T) {
	t.Parallel()
	handler, err := NewHandler(&HandlerOptions{
		Pipeline: newPipeline(t, &Options{Speech: &fakeSpeech{}}),
		Session:  func(*http.Request) (Runner, error) { return nil, errors.New("unknown conversation") },
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Hallo"}`)))
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "unknown conversation") {
		t.Errorf("%d %s", recorder.Code, recorder.Body)
	}
}

func TestNewHandler_Validates(t *testing.T) {
	t.Parallel()
	pipeline := newPipeline(t, &Options{Speech: &fakeSpeech{}})
	for name, options := range map[string]*HandlerOptions{
		"nil":        nil,
		"no session": {Pipeline: pipeline},
		"no pipeline": {Session: func(*http.Request) (Runner, error) {
			return nil, errors.New("unused")
		}},
	} {
		if _, err := NewHandler(options); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestRunErrorStatus(t *testing.T) {
	t.Parallel()
	if got := runErrorStatus(llm.ErrInvalidRequest); got != http.StatusBadRequest {
		t.Errorf("invalid request: %d", got)
	}
	if got := runErrorStatus(errors.New("boom")); got != http.StatusInternalServerError {
		t.Errorf("other error: %d", got)
	}
}

// A client that disconnects after the first line must not cancel the turn:
// the reply still lands in History.
func TestHandler_ClientDisconnectDoesNotCancelTheTurn(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	chat := &gatedClient{first: "Die Dusche passt an die Fensterwand. ", rest: "Der Rest kommt später.", gate: release}
	handler, history := newTestHandler(t, nil, chat, 0)
	disconnected, served := make(chan struct{}), make(chan struct{})
	testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer close(served)
		watched := request.Context()
		go func() {
			<-watched.Done()
			close(disconnected)
		}()
		handler.ServeHTTP(writer, request)
	}))
	defer testServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, testServer.URL, strings.NewReader(`{"text":"Hallo"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(response.Body).ReadString('\n') // a short read fails the check below
	if !strings.Contains(line, `"type":"text"`) {
		t.Fatalf("first line = %q", line)
	}
	cancel()
	_ = response.Body.Close() // the connection is cancelled already
	<-disconnected
	close(release)
	<-served

	messages := history()
	if want := "Die Dusche passt an die Fensterwand. Der Rest kommt später."; len(messages) != 2 || messages[1].Content != want {
		t.Fatalf("history = %+v, want the full reply", messages)
	}
}

func TestHandler_TimeoutStillWritesDone(t *testing.T) {
	t.Parallel()
	session, _ := newSession(t, &gatedClient{first: "Die Dusche ", gate: make(chan struct{})})
	handler, err := NewHandler(&HandlerOptions{
		Pipeline: newPipeline(t, &Options{Speech: &fakeSpeech{}}),
		Session:  func(*http.Request) (Runner, error) { return session, nil },
		Timeout:  100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Hallo"}`)))
	lines := readLines(t, recorder.Body)
	if last := lines[len(lines)-1]; last["type"] != "done" || last["end_reason"] != "timeout" || last["usage"] == nil {
		t.Fatalf("last line = %v", last)
	}
}

func TestHandler_ToolResultContentAndExtraFields(t *testing.T) {
	t.Parallel()
	registry := tools.NewRegistry()
	if err := registry.Register(llm.ToolDefinition{Name: "highlight", Parameters: tools.Object(map[string]any{})},
		func(context.Context, json.RawMessage, tools.Environment) (tools.Result, error) {
			return tools.Result{Content: map[string]string{"ui": "highlight:shower"}}, nil
		}); err != nil {
		t.Fatal(err)
	}
	chat := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "c1", Name: "highlight", Arguments: `{}`}},
			{Kind: llm.EventEnd},
		}},
		replyTurn(0, "Da ist die Dusche."),
	)
	session, err := agent.NewSession(&agent.SessionOptions{
		ID: "voice-test", Client: chat, History: memory.New(), Locker: agent.NewLocalLocker(),
		Model: "test/model", Tools: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	var route string
	handler, err := NewHandler(&HandlerOptions{
		Pipeline: newPipeline(t, &Options{Speech: &fakeSpeech{}}),
		Session: func(request *http.Request) (Runner, error) {
			var extra struct {
				Route string `json:"route"`
			}
			if err := json.NewDecoder(request.Body).Decode(&extra); err != nil {
				return nil, err
			}
			route = extra.Route
			return session, nil
		},
		ToolResult: func(result agent.ToolResult) any { return result.Content },
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Wo ist die Dusche?","route":"/plan/3"}`)))
	if route != "/plan/3" {
		t.Errorf("route = %q", route)
	}
	for _, line := range readLines(t, recorder.Body) {
		if line["type"] == "tool_result" {
			content, _ := line["tool"].(map[string]any)["content"].(map[string]any)
			if content["ui"] != "highlight:shower" {
				t.Fatalf("tool_result line = %v", line)
			}
			return
		}
	}
	t.Fatal("no tool_result line")
}

func TestToWireUsage_CountsTranscriptionTokens(t *testing.T) {
	t.Parallel()
	wire := toWireUsage(&Usage{
		Transcription: &llm.Usage{PromptTokens: 100, CompletionTokens: 7, CostUSD: 0.0001, Provider: "Google"},
		LLM:           llm.Usage{PromptTokens: 10, CompletionTokens: 5, CostUSD: 0.002},
		CostUSD:       0.0021,
	})
	if wire.PromptTokens != 110 || wire.CompletionTokens != 12 || wire.TranscriptionCostUSD != 0.0001 || wire.Transcription == nil || wire.Transcription.Provider != "Google" {
		t.Errorf("wire usage = %+v", wire)
	}
}

// stalledWriter is a ResponseWriter whose client stopped reading: a Write
// blocks until the write deadline passes or, without a deadline, until the
// test releases it.
type stalledWriter struct {
	header   http.Header
	release  chan struct{}
	mutex    sync.Mutex
	deadline time.Time
	writes   int
}

func (s *stalledWriter) Header() http.Header { return s.header }

func (*stalledWriter) WriteHeader(int) {}

func (*stalledWriter) Flush() {}

func (s *stalledWriter) SetWriteDeadline(deadline time.Time) error {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.deadline = deadline
	return nil
}

func (s *stalledWriter) Write([]byte) (int, error) {
	s.mutex.Lock()
	deadline := s.deadline
	s.writes++
	s.mutex.Unlock()
	if deadline.IsZero() {
		<-s.release
		return 0, errors.New("released without a deadline")
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.release:
	}
	return 0, os.ErrDeadlineExceeded
}

func (s *stalledWriter) writeCount() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return s.writes
}

// A client that stops reading must not block the handler: the write
// deadline ends the stream, and the turn still drains into History.
func TestHandler_StalledClientHitsTheWriteDeadline(t *testing.T) {
	t.Parallel()
	session, history := newSession(t, llmtest.NewStubClient(replyTurn(0, "Erster Satz. ", "Zweiter Satz.")))
	handler, err := NewHandler(&HandlerOptions{
		Pipeline:     newPipeline(t, &Options{Speech: &fakeSpeech{}}),
		Session:      func(*http.Request) (Runner, error) { return session, nil },
		WriteTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	writer := &stalledWriter{header: http.Header{}, release: make(chan struct{})}
	request := newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Hallo","output":"speech"}`))
	served := make(chan struct{})
	go func() {
		defer close(served)
		handler.ServeHTTP(writer, request)
	}()
	select {
	case <-served:
	case <-time.After(5 * time.Second):
		close(writer.release)
		<-served
		t.Fatal("handler blocked on a client that stopped reading")
	}
	if got := writer.writeCount(); got != 1 {
		t.Errorf("writes = %d, want 1: nothing is written after a failed write", got)
	}
	messages, err := history.Read(context.Background(), "voice-test")
	if err != nil || len(messages) != 2 || messages[1].Content != "Erster Satz. Zweiter Satz." {
		t.Fatalf("history = %+v (%v), want the full reply", messages, err)
	}
}

// deadlineRecorder is a ResponseRecorder that records every write deadline.
type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	d.deadlines = append(d.deadlines, deadline)
	return nil
}

// The write deadline covers one line only: left armed while the turn waits
// for its next event (a long tool call), it would reset a healthy HTTP/2
// stream. Every deadline is cleared again after its line.
func TestHandler_ClearsTheWriteDeadlineAfterEachLine(t *testing.T) {
	t.Parallel()
	handler, _ := newTestHandler(t, nil, llmtest.NewStubClient(replyTurn(0, firstAnswer+secondAnswer)), 0)
	writer := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	handler.ServeHTTP(writer, newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Hallo","output":"speech"}`)))

	lines := len(readLines(t, writer.Body))
	if lines == 0 || len(writer.deadlines) != 2*lines {
		t.Fatalf("%d deadlines for %d lines, want one set and one clear per line", len(writer.deadlines), lines)
	}
	for index, deadline := range writer.deadlines {
		if armed := index%2 == 0; deadline.IsZero() == armed {
			t.Errorf("deadline %d = %v, want armed before and cleared after each line", index, deadline)
		}
	}
}

// bareWriter is a ResponseWriter without Flush, deadlines or Unwrap, like a
// logging or gzip middleware wrapper.
type bareWriter struct {
	header http.Header
	body   bytes.Buffer
}

func (b *bareWriter) Header() http.Header { return b.header }

func (*bareWriter) WriteHeader(int) {}

func (b *bareWriter) Write(data []byte) (int, error) { return b.body.Write(data) }

// A writer that cannot flush is not a disconnected client: every line,
// "done" included, is written.
func TestHandler_WriterWithoutFlushGetsEveryLine(t *testing.T) {
	t.Parallel()
	handler, _ := newTestHandler(t, nil, llmtest.NewStubClient(replyTurn(0, firstAnswer+secondAnswer)), 0)
	writer := &bareWriter{header: http.Header{}}
	handler.ServeHTTP(writer, newPost(t, "/voice", "application/json", strings.NewReader(`{"text":"Hallo","output":"speech"}`)))

	var types []string
	for _, line := range readLines(t, &writer.body) {
		types = append(types, line["type"].(string))
	}
	if got := strings.Join(types, ","); got != "text,speech,speech,done" {
		t.Errorf("types = %s, want every line", got)
	}
}

func TestNewHandler_DefaultsTheWriteTimeout(t *testing.T) {
	t.Parallel()
	handler, err := NewHandler(&HandlerOptions{
		Pipeline: newPipeline(t, &Options{Speech: &fakeSpeech{}}),
		Session:  func(*http.Request) (Runner, error) { return nil, errors.New("unused") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if handler.options.WriteTimeout != defaultWriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", handler.options.WriteTimeout, defaultWriteTimeout)
	}
}
