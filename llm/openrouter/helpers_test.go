package openrouter

import (
	"bytes"
	"context"
	"embed"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/govengers/aikido/llm"
)

// Stream starts a producer goroutine per call (TST-009).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

//go:embed testdata
var testdata embed.FS

// loadFixture reads a recorded response body (SSE transcript or JSON) from
// testdata/.
func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	body, err := testdata.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// sseHandler answers 200 text/event-stream with body.
func sseHandler(body []byte) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(body)
	}
}

// jsonHandler answers 200 application/json with body.
func jsonHandler(body []byte) http.HandlerFunc {
	return func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(body)
	}
}

// newTestServer starts handler and closes it when the test ends.
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

// newTestClient builds a Client targeting server.URL.
func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	client, err := NewClient(&Options{APIKey: "sk-test", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// streamBody streams one request against a server answering with the SSE
// body and returns every event.
func streamBody(t *testing.T, body []byte) []llm.Event {
	t.Helper()
	client := newTestClient(t, newTestServer(t, sseHandler(body)))
	stream, err := client.Stream(context.Background(), llm.Request{Model: "x"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	return drain(t, stream, 5*time.Second)
}

// drain collects events from a stream until the channel closes.
func drain(t *testing.T, stream <-chan llm.Event, timeout time.Duration) []llm.Event {
	t.Helper()
	var events []llm.Event
	deadline := time.After(timeout)
	for {
		select {
		case event, ok := <-stream:
			if !ok {
				return events
			}
			events = append(events, event)
		case <-deadline:
			t.Fatalf("timeout draining stream after %d events", len(events))
		}
	}
}

// streamSummary folds a stream's events by kind.
type streamSummary struct {
	text      string
	thinking  string
	toolCalls []llm.ToolCall
	images    []llm.ImagePart
	usages    []*llm.Usage
	errors    []error
	end       *llm.Event
}

// summarize folds events and fails the test on an event without its payload.
func summarize(t *testing.T, events []llm.Event) streamSummary {
	t.Helper()
	var summary streamSummary
	for index, event := range events {
		switch event.Kind {
		case llm.EventTextDelta:
			summary.text += event.Text
		case llm.EventThinking:
			summary.thinking += event.Text
		case llm.EventToolCall:
			if event.Tool == nil {
				t.Fatal("EventToolCall with nil Tool")
			}
			summary.toolCalls = append(summary.toolCalls, *event.Tool)
		case llm.EventImage:
			if event.Image == nil {
				t.Fatal("EventImage with nil Image")
			}
			summary.images = append(summary.images, *event.Image)
		case llm.EventUsage:
			if event.Usage == nil {
				t.Fatal("EventUsage with nil Usage")
			}
			summary.usages = append(summary.usages, event.Usage)
		case llm.EventError:
			summary.errors = append(summary.errors, event.Err)
		case llm.EventEnd:
			summary.end = &events[index]
		}
	}
	return summary
}

// assertEventOrder verifies the per-spec event ordering invariant: EventEnd
// appears exactly once, as the last event.
func assertEventOrder(t *testing.T, events []llm.Event) {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	if last := events[len(events)-1].Kind; last != llm.EventEnd {
		t.Errorf("last event kind = %s, want EventEnd", last)
	}
	var ends int
	for _, event := range events {
		if event.Kind == llm.EventEnd {
			ends++
		}
	}
	if ends != 1 {
		t.Errorf("EventEnd count = %d, want 1", ends)
	}
}

func eventKinds(events []llm.Event) []llm.EventKind {
	kinds := make([]llm.EventKind, 0, len(events))
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	return kinds
}

var pngSignature = []byte("\x89PNG\r\n\x1a\n")

func isPNG(data []byte) bool {
	return bytes.HasPrefix(data, pngSignature)
}
