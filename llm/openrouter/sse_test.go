package openrouter

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

// panickingReader stands in for a decoding bug: it panics on the first read.
type panickingReader struct{}

func (panickingReader) Read([]byte) (int, error) { panic("decoder bug") }

// TestProcessStream_PanicBecomesEventError pins CON-001: a panic in the
// stream goroutine reaches the consumer as EventError followed by EventEnd
// instead of killing the process.
func TestProcessStream_PanicBecomesEventError(t *testing.T) {
	t.Parallel()
	out := make(chan llm.Event, eventBufferSize)
	processStream(context.Background(), panickingReader{}, out, time.Now())
	close(out)

	var events []llm.Event
	for event := range out {
		events = append(events, event)
	}
	if len(events) != 2 {
		t.Fatalf("events = %+v, want EventError then EventEnd", events)
	}
	if events[0].Kind != llm.EventError || !strings.Contains(events[0].Err.Error(), "decoder bug") {
		t.Errorf("first event = %+v, want EventError carrying the panic", events[0])
	}
	if events[1].Kind != llm.EventEnd || events[1].FinishReason != finishReasonError {
		t.Errorf("last event = %+v, want EventEnd with finish reason %q", events[1], finishReasonError)
	}
}

func TestSSEPayload(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name        string
		line        string
		wantPayload string
		wantOK      bool
	}{
		{name: "data line", line: `data: {"id":"x"}`, wantPayload: `{"id":"x"}`, wantOK: true},
		{name: "data without space", line: `data:{"id":"x"}`, wantPayload: `{"id":"x"}`, wantOK: true},
		{name: "carriage return", line: "data: [DONE]\r", wantPayload: "[DONE]", wantOK: true},
		{name: "blank line", line: ""},
		{name: "keepalive comment", line: ": OPENROUTER PROCESSING"},
		{name: "other field", line: "event: message"},
		{name: "empty payload", line: "data:   "},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			payload, ok := ssePayload(testCase.line)
			if payload != testCase.wantPayload || ok != testCase.wantOK {
				t.Errorf("ssePayload(%q) = (%q, %v), want (%q, %v)", testCase.line, payload, ok, testCase.wantPayload, testCase.wantOK)
			}
		})
	}
}

func TestDecodeDeltaContent(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name       string
		raw        string
		wantText   string
		wantImages int
	}{
		{name: "empty", raw: ""},
		{name: "null", raw: "null"},
		{name: "plain string", raw: `"hello"`, wantText: "hello"},
		{name: "inline data URI", raw: `"data:image/png;base64,QUJD"`, wantImages: 1},
		{name: "broken data URI stays text", raw: `"data:image/png;base64,***"`, wantText: "data:image/png;base64,***"},
		{name: "typed parts", raw: `[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"https://x/y.png"}},{"type":"text","text":"b"}]`, wantText: "ab", wantImages: 1},
		{name: "image part without url", raw: `[{"type":"image_url"}]`},
		{name: "unknown part type", raw: `[{"type":"audio","text":"ignored"}]`},
		{name: "unknown shape", raw: `{"weird":true}`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			text, images := decodeDeltaContent([]byte(testCase.raw))
			if text != testCase.wantText || len(images) != testCase.wantImages {
				t.Errorf("decodeDeltaContent(%s) = (%q, %d images), want (%q, %d images)", testCase.raw, text, len(images), testCase.wantText, testCase.wantImages)
			}
		})
	}
}

func TestDecodeDataURI(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name            string
		uri             string
		wantOK          bool
		wantContentType string
		wantData        string
		wantURL         string
	}{
		{name: "base64", uri: "data:image/png;base64,QUJD", wantOK: true, wantContentType: "image/png", wantData: "ABC"},
		{name: "not base64 keeps the URL", uri: "data:text/plain,hi", wantOK: true, wantContentType: "text/plain", wantURL: "data:text/plain,hi"},
		{name: "no comma", uri: "data:image/png;base64"},
		{name: "bad base64", uri: "data:image/png;base64,***"},
		{name: "not a data URI", uri: "https://example.com/a.png"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			image, ok := decodeDataURI(testCase.uri)
			if ok != testCase.wantOK || image.ContentType != testCase.wantContentType || string(image.Data) != testCase.wantData || image.URL != testCase.wantURL {
				t.Errorf("decodeDataURI(%q) = (%+v, %v)", testCase.uri, image, ok)
			}
		})
	}
}

func TestIsContentFilterErrorEnvelope(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		envelope *apiError
		want     bool
	}{
		{name: "nil", envelope: nil},
		{name: "string code", envelope: &apiError{Code: []byte(`"content_policy_violation"`)}, want: true},
		{name: "string code any case", envelope: &apiError{Code: []byte(`"SAFETY"`)}, want: true},
		{name: "numeric code", envelope: &apiError{Code: []byte(`400`), Message: "bad input"}},
		{name: "message", envelope: &apiError{Message: "Blocked by the Safety Filter"}, want: true},
		{name: "unrelated message", envelope: &apiError{Message: "upstream timeout"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := isContentFilterErrorEnvelope(testCase.envelope); got != testCase.want {
				t.Errorf("isContentFilterErrorEnvelope(%+v) = %v, want %v", testCase.envelope, got, testCase.want)
			}
		})
	}
}

func TestToolCallAssembler_Flush(t *testing.T) {
	t.Parallel()
	assembler := newToolCallAssembler()
	feed := func(fragment toolCallFragment) {
		t.Helper()
		if err := assembler.feed(fragment); err != nil {
			t.Fatalf("feed: %v", err)
		}
	}
	feed(toolCallFragment{Index: 1, Function: functionFragment{Name: "second"}})
	feed(toolCallFragment{Index: 0, ID: "call_a", Function: functionFragment{Name: "first", Arguments: `{"a":`}})
	feed(toolCallFragment{Index: 0, ID: "call_a", Function: functionFragment{Name: "ignored", Arguments: `1}`}})
	// A new id at an index already in use is a new call (complete calls all
	// at index 0); a fragment without id continues it.
	feed(toolCallFragment{Index: 0, ID: "call_b", Function: functionFragment{Name: "third", Arguments: `{"b":`}})
	feed(toolCallFragment{Index: 0, Function: functionFragment{Arguments: `2}`}})

	calls := assembler.flush()
	want := []llm.ToolCall{
		{ID: "call_a", Name: "first", Arguments: `{"a":1}`},
		{ID: "call_b", Name: "third", Arguments: `{"b":2}`},
		{ID: "call_1", Name: "second", Arguments: "{}"},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %+v", calls, want)
	}
	for index := range want {
		if calls[index] != want[index] {
			t.Errorf("calls[%d] = %+v, want %+v", index, calls[index], want[index])
		}
	}
	if again := assembler.flush(); again != nil {
		t.Errorf("second flush = %+v, want nil (flush resets)", again)
	}
}

// TestStream_End pins how a stream may end: [DONE] or a finish_reason
// completes it and flushes pending tool calls; a clean EOF with neither is a
// cut-off stream and an ErrServerError.
func TestStream_End(t *testing.T) {
	t.Parallel()
	const toolFragment = "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{}\"}}]}}]}\n\n"
	const text = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}]}\n\n"
	const stop = "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	testCases := []struct {
		name          string
		body          string
		wantToolCalls int
		wantErr       error
	}{
		{name: "done without finish reason flushes tool calls", body: toolFragment + "data: [DONE]\n\n", wantToolCalls: 1},
		{name: "finish reason without done", body: text + stop},
		{name: "eof without either", body: text, wantErr: llm.ErrServerError},
		{name: "eof with pending tool call", body: toolFragment, wantToolCalls: 1, wantErr: llm.ErrServerError},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			events := streamBody(t, []byte(testCase.body))
			assertEventOrder(t, events)
			summary := summarize(t, events)
			if len(summary.toolCalls) != testCase.wantToolCalls {
				t.Errorf("tool calls = %d, want %d", len(summary.toolCalls), testCase.wantToolCalls)
			}
			switch {
			case testCase.wantErr == nil && len(summary.errors) != 0:
				t.Errorf("errors = %v, want none", summary.errors)
			case testCase.wantErr != nil && (len(summary.errors) != 1 || !errors.Is(summary.errors[0], testCase.wantErr)):
				t.Errorf("errors = %v, want one %v", summary.errors, testCase.wantErr)
			}
		})
	}
}

// TestToolCallAssembler_Caps pins the per-stream bounds on what tool calls
// accumulate, across flushes: the line cap alone bounds one fragment only.
func TestToolCallAssembler_Caps(t *testing.T) {
	t.Parallel()
	quarter := strings.Repeat("x", maximumToolCallArgumentBytes/4)
	testCases := []struct {
		name      string
		fragments []toolCallFragment
	}{
		{name: "argument bytes", fragments: slices.Repeat([]toolCallFragment{{Function: functionFragment{Arguments: quarter}}}, 5)},
		{name: "calls", fragments: func() []toolCallFragment {
			fragments := make([]toolCallFragment, maximumToolCalls+1)
			for index := range fragments {
				fragments[index] = toolCallFragment{Index: index, Function: functionFragment{Name: "t"}}
			}
			return fragments
		}()},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assembler := newToolCallAssembler()
			for count, fragment := range testCase.fragments {
				err := assembler.feed(fragment)
				// Flushing in between does not reset the stream's budget.
				assembler.flush()
				last := count == len(testCase.fragments)-1
				if last != errors.Is(err, llm.ErrInvalidResponse) {
					t.Fatalf("fragment %d: err = %v, want ErrInvalidResponse only on the last", count, err)
				}
			}
		})
	}
}

// TestStream_TooManyToolCallsFailsTheStream pins the stream side of the cap:
// EventError with ErrInvalidResponse, no tool calls, EventEnd last.
func TestStream_TooManyToolCallsFailsTheStream(t *testing.T) {
	t.Parallel()
	var body strings.Builder
	for index := range maximumToolCalls + 1 {
		fmt.Fprintf(&body, "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":%d,\"id\":\"c%d\",\"function\":{\"name\":\"t\",\"arguments\":\"{}\"}}]}}]}\n\n", index, index)
	}
	body.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	events := streamBody(t, []byte(body.String()))
	assertEventOrder(t, events)
	summary := summarize(t, events)
	if len(summary.errors) != 1 || !errors.Is(summary.errors[0], llm.ErrInvalidResponse) {
		t.Errorf("errors = %v, want one ErrInvalidResponse", summary.errors)
	}
	if len(summary.toolCalls) != 0 {
		t.Errorf("tool calls = %d, want none", len(summary.toolCalls))
	}
}
