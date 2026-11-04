package prometheusadapter_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/prometheusadapter"
)

func newMetrics(t *testing.T) (*prometheusadapter.Metrics, *prometheus.Registry) {
	t.Helper()
	registry := prometheus.NewRegistry()
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "billing"})
	if err != nil {
		t.Fatal(err)
	}
	return metrics, registry
}

func TestNewValidatesOptions(t *testing.T) {
	for name, options := range map[string]*prometheusadapter.Options{
		"nil options":   nil,
		"no registerer": {Namespace: "billing"},
		"no namespace":  {Registerer: prometheus.NewRegistry()},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := prometheusadapter.New(options); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestNewReturnsADuplicateAndRegistersNothing(t *testing.T) {
	registry := prometheus.NewRegistry()
	taken := prometheus.NewCounter(prometheus.CounterOpts{Namespace: "billing", Subsystem: "llm", Name: "cost_usd_total", Help: "Taken."})
	registry.MustRegister(taken)

	_, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "billing"})

	if err == nil {
		t.Fatal("want the duplicate's error")
	}
	families, gatherErr := registry.Gather()
	if gatherErr != nil {
		t.Fatal(gatherErr)
	}
	if len(families) != 1 {
		t.Errorf("families = %d, want only the one registered before", len(families))
	}
}

func TestCallFinishedCountsRequestsTokensAndCost(t *testing.T) {
	metrics, registry := newMetrics(t)
	call := &llm.Call{Client: "openrouter", Operation: llm.OperationStream, Model: "anthropic/claude-sonnet-4.6"}
	ctx := metrics.CallStarted(t.Context(), call)

	metrics.CallFinished(ctx, call, &llm.CallOutcome{Duration: 2 * time.Second, Usage: &llm.Usage{
		PromptTokens: 1200, CompletionTokens: 300, CacheReadTokens: 1000, CostUSD: 0.0125,
	}})
	metrics.CallFinished(ctx, call, &llm.CallOutcome{Duration: time.Second, Err: fmt.Errorf("stream: %w", llm.ErrRateLimited)})
	metrics.CallFinished(ctx, call, &llm.CallOutcome{Duration: time.Second, Err: context.Canceled})
	metrics.CallFinished(ctx, call, &llm.CallOutcome{Duration: time.Second, Err: context.DeadlineExceeded})

	labels := `client="openrouter",model="anthropic/claude-sonnet-4.6"`
	expected := `
# HELP billing_llm_requests_total Model calls by client, model, operation and outcome.
# TYPE billing_llm_requests_total counter
billing_llm_requests_total{` + labels + `,operation="stream",outcome="cancelled"} 1
billing_llm_requests_total{` + labels + `,operation="stream",outcome="failure"} 2
billing_llm_requests_total{` + labels + `,operation="stream",outcome="success"} 1
# HELP billing_llm_tokens_total Tokens the provider reported by client, model and type (prompt includes the cached ones).
# TYPE billing_llm_tokens_total counter
billing_llm_tokens_total{` + labels + `,type="cache_read"} 1000
billing_llm_tokens_total{` + labels + `,type="completion"} 300
billing_llm_tokens_total{` + labels + `,type="prompt"} 1200
# HELP billing_llm_cost_usd_total Cost the provider billed, in US dollars, by client and model.
# TYPE billing_llm_cost_usd_total counter
billing_llm_cost_usd_total{` + labels + `} 0.0125
`
	if err := testutil.GatherAndCompare(registry, strings.NewReader(expected), "billing_llm_requests_total", "billing_llm_tokens_total", "billing_llm_cost_usd_total"); err != nil {
		t.Error(err)
	}
	if count := testutil.CollectAndCount(registry, "billing_llm_request_duration_seconds"); count != 1 {
		t.Errorf("duration series = %d, want 1", count)
	}
	problems, err := testutil.GatherAndLint(registry)
	if err != nil || len(problems) != 0 {
		t.Errorf("lint: %v %v", err, problems)
	}
}

func TestCallStartedKeepsTheContext(t *testing.T) {
	metrics, _ := newMetrics(t)
	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "kept")

	if metrics.CallStarted(ctx, &llm.Call{}).Value(key{}) != "kept" {
		t.Error("CallStarted replaced the context")
	}
}

var errExample = errors.New("example")

func ExampleNew() {
	registry := prometheus.NewRegistry() // main's registry
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "billing"})
	if err != nil {
		fmt.Println(err)
		return
	}
	call := &llm.Call{Client: "openrouter", Operation: llm.OperationComplete, Model: "openai/gpt-5.4"}
	metrics.CallFinished(context.Background(), call, &llm.CallOutcome{Err: errExample}) // what an llm.ObservedClient does
	fmt.Println(testutil.CollectAndCount(registry, "billing_llm_requests_total"))
	// Output: 1
}
