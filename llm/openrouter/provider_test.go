package openrouter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/govengers/aikido/llm"
)

func TestWithProvider_OverridesClientPreferences(t *testing.T) {
	t.Parallel()
	var captured map[string]any
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		rawBody, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(rawBody, &captured)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"provider":"Azure","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":40}}`))
	})
	client, err := NewClient(&Options{APIKey: "k", BaseURL: server.URL, ProviderOrder: []string{"anthropic"}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithProvider(context.Background(), &ProviderPreferences{
		Only: []string{"azure"}, Sort: "throughput", MaximumPrice: &MaximumPrice{Completion: 6},
	})
	response, err := client.Complete(ctx, llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := captured["provider"].(map[string]any)
	if provider["sort"] != "throughput" || provider["order"] != nil || provider["max_price"].(map[string]any)["completion"] != 6.0 {
		t.Errorf("provider block = %v; want the context preferences only", provider)
	}
	usage := response.Usage
	if usage == nil || usage.Provider != "Azure" || usage.Duration <= 0 || usage.Latency != usage.Duration {
		t.Errorf("usage = %+v", usage)
	}
}

func TestStream_UsageCarriesProviderAndTiming(t *testing.T) {
	t.Parallel()
	chunks := []string{
		`{"provider":"Anthropic","choices":[{"delta":{"role":"assistant","content":""}}]}`,
		`{"provider":"Anthropic","choices":[{"delta":{"content":"Hello"}}]}`,
		`{"provider":"Anthropic","choices":[{"delta":{"content":" world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":30}}`,
	}
	server := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		flusher := writer.(http.Flusher)
		for index, chunk := range chunks {
			if index > 0 {
				time.Sleep(150 * time.Millisecond)
			}
			_, _ = io.WriteString(writer, "data: "+chunk+"\n\n")
			flusher.Flush()
		}
		_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	})
	client := newTestClient(t, server)
	stream, err := client.Stream(context.Background(), llm.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var usage *llm.Usage
	for _, event := range drain(t, stream, 5*time.Second) {
		if event.Kind == llm.EventUsage {
			usage = event.Usage
		}
	}
	if usage == nil {
		t.Fatal("no usage event")
	}
	// The role-only opening chunk is not output: latency covers the first sleep.
	if usage.Provider != "Anthropic" || usage.Latency < 140*time.Millisecond || usage.Duration < usage.Latency+140*time.Millisecond {
		t.Errorf("usage = %+v", usage)
	}
	// 150 ms of generation is under the 250 ms rate window: no rate.
	if usage.TokensPerSecond != 0 {
		t.Errorf("tokens/s = %v; want 0 under the rate window", usage.TokensPerSecond)
	}
}

func TestModelEndpoints(t *testing.T) {
	t.Parallel()
	var path string
	server := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		path = request.URL.Path
		_, _ = writer.Write([]byte(`{"data":{"id":"anthropic/claude-haiku-4.5","endpoints":[{"provider_name":"Azure","tag":"azure/global",
			"pricing":{"prompt":"0.000001","completion":"0.000005","discount":0},"max_prompt_tokens":null,"uptime_last_30m":100,
			"latency_last_30m":{"p50":414,"p75":499,"p90":705.8,"p99":2744.8},"throughput_last_30m":{"p50":84,"p75":78,"p90":59.8,"p99":28.88}},
			{"provider_name":"Idle","latency_last_30m":null,"throughput_last_30m":null}]}}`))
	})
	client := newTestClient(t, server)
	modelEndpoints, err := client.ModelEndpoints(context.Background(), "anthropic/claude-haiku-4.5")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/models/anthropic/claude-haiku-4.5/endpoints" {
		t.Errorf("path = %s", path)
	}
	endpoint := modelEndpoints.Endpoints[0]
	if endpoint.Tag != "azure/global" || endpoint.Pricing.Completion.PerMillion() != 5 || endpoint.LatencyLast30Minutes.P50 != 414 || endpoint.ThroughputLast30Minutes.P50 != 84 || *endpoint.UptimeLast30Minutes != 100 {
		t.Errorf("endpoint = %+v", endpoint)
	}
	if modelEndpoints.Endpoints[1].LatencyLast30Minutes != nil {
		t.Error("null latency should stay nil")
	}
	if _, err := client.ModelEndpoints(context.Background(), "noslash"); err == nil {
		t.Error("id without author should error")
	}
}
