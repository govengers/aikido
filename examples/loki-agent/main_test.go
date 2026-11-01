package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
)

// fakeLoki answers labels and query_range like Loki; a query containing "rate("
// gets a matrix result.
func fakeLoki(t *testing.T, requests *[]*http.Request) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		*requests = append(*requests, request)
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.URL.Path == "/loki/api/v1/labels":
			_, _ = writer.Write([]byte(`{"status":"success","data":["app","level"]}`))
		case strings.Contains(request.URL.Query().Get("query"), "rate("):
			_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"matrix","result":[]}}`))
		case request.URL.Path == "/loki/api/v1/query_range":
			_, _ = writer.Write([]byte(`{"status":"success","data":{"resultType":"streams","result":[
				{"stream":{"app":"checkout","level":"error"},"values":[
					["1762000300000000000","payment gateway timeout"],
					["1762000200000000000","payment gateway timeout"]]},
				{"stream":{"app":"checkout","level":"error"},"values":[
					["1762000100000000000","database pool exhausted"]]}]}}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func toolCall(id, name string, arguments map[string]any) llm.Event {
	encoded, _ := json.Marshal(arguments) // maps of strings and ints always encode
	return llm.Event{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: id, Name: name, Arguments: string(encoded)}}
}

func TestTheAgentFetchesLogsAndAnalyzesThemWithBash(t *testing.T) {
	var requests []*http.Request
	server := fakeLoki(t, &requests)
	loki := &lokiClient{http: server.Client(), baseURL: server.URL, token: "secret", now: func() time.Time { return time.Unix(1762003600, 0) }}
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{toolCall("c1", "loki_query", map[string]any{"query": `{app="checkout"} |= "error"`, "name": "checkout", "hours": 1}), {Kind: llm.EventEnd}}},
		llmtest.TurnScript{Events: []llm.Event{toolCall("c2", "bash", map[string]any{"command": "cut -f3 logs/checkout.log | sort | uniq -c | sort -rn"}), {Kind: llm.EventEnd}}},
		llmtest.TurnScript{Events: []llm.Event{toolCall("c3", "loki_query", map[string]any{"query": `sum(rate({app="checkout"}[5m]))`, "name": "rate"}), {Kind: llm.EventEnd}}},
		llmtest.TurnScript{Events: []llm.Event{{Kind: llm.EventTextDelta, Text: "Summary: payment gateway timeouts."}, {Kind: llm.EventEnd}}},
	)
	session, err := newSession(stub, loki, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	events, err := session.Run(context.Background(), "Why does checkout fail?")
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]agent.ToolResult{}
	for event := range events {
		if event.Kind == agent.EventToolResult {
			results[event.ToolResult.CallID] = *event.ToolResult
		}
	}

	query := results["c1"]
	stored, _ := json.Marshal(query.Content) // a map of plain values always encodes
	if !query.OK || !strings.Contains(string(stored), `"file":"logs/checkout.log"`) || !strings.Contains(string(stored), `"lines":3`) {
		t.Fatalf("loki_query result = %+v (%s)", query, stored)
	}
	if !strings.Contains(string(stored), "2025-11-01T12:28:20Z\\t{app=checkout,level=error}\\tdatabase pool exhausted") {
		t.Errorf("the preview does not start with the oldest line in the stored format: %s", stored)
	}
	analysis := results["c2"].ModelContent
	if !strings.Contains(analysis, `2 payment gateway timeout`) || !strings.Contains(analysis, `1 database pool exhausted`) {
		t.Errorf("bash analysis = %s; want counts per message", analysis)
	}
	if results["c3"].OK || !strings.Contains(results["c3"].Error, "metric query") {
		t.Errorf("a metric query must be refused with a hint, got %+v", results["c3"])
	}

	first := requests[0]
	if first.URL.Path != "/loki/api/v1/query_range" || first.Header.Get("Authorization") != "Bearer secret" {
		t.Errorf("request = %s, Authorization %q", first.URL.Path, first.Header.Get("Authorization"))
	}
	if got := first.URL.Query().Get("start"); got != "1762000000000000000" {
		t.Errorf("start = %s; want one hour before the clock", got)
	}
}

func TestLokiErrorsReachTheModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Error(writer, "parse error at line 1, col 7: syntax error: unexpected IDENTIFIER", http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	loki := &lokiClient{http: server.Client(), baseURL: server.URL, now: time.Now}
	_, err := loki.query(context.Background(), "{app=checkout}", time.Hour, 10)
	if err == nil || !strings.Contains(err.Error(), "status 400") || !strings.Contains(err.Error(), "unexpected IDENTIFIER") {
		t.Fatalf("err = %v; want the status and Loki's message", err)
	}
}
