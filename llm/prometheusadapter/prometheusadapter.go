// Package prometheusadapter exports the model calls of an llm.ObservedClient as Prometheus
// metrics on a registry the caller passes in. Metrics implements llm.Observer; put it into
// llm.ObservedClientOptions.Observers.
package prometheusadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/govengers/aikido/llm"
)

const subsystem = "llm"

// The values of the outcome label.
const (
	OutcomeSuccess   = "success"
	OutcomeFailure   = "failure"
	OutcomeCancelled = "cancelled" // the caller cancelled; a deadline that passed is a failure (a slow provider)
)

// The values of the type label of the token counter. Prompt tokens include the cached ones.
const (
	TokensPrompt     = "prompt"
	TokensCompletion = "completion"
	TokensCacheRead  = "cache_read"
	TokensCacheWrite = "cache_write"
)

// durationBuckets reach from 250 ms to about 10 minutes: a short completion to a long agent turn.
var durationBuckets = prometheus.ExponentialBuckets(0.25, 2, 12)

// Options configures Metrics.
type Options struct {
	Registerer prometheus.Registerer // required; the caller's registry, never the default one
	Namespace  string                // required: the service name, e.g. "billing" for billing_llm_requests_total
}

// Metrics records model calls; it implements llm.Observer and is safe for concurrent use.
// The model label holds llm.Request.Model, which the application's code or configuration
// chooses, so its values form a closed set; never let a user pick the model string freely.
//
//	<namespace>_llm_requests_total{client,model,operation,outcome}
//	<namespace>_llm_request_duration_seconds{client,model,operation}
//	<namespace>_llm_tokens_total{client,model,type}
//	<namespace>_llm_cost_usd_total{client,model}
type Metrics struct {
	requests  *prometheus.CounterVec
	durations *prometheus.HistogramVec
	tokens    *prometheus.CounterVec
	cost      *prometheus.CounterVec
}

var _ llm.Observer = (*Metrics)(nil)

// New registers the metrics on options.Registerer, all or none; a failed registration, a
// duplicate for example, is returned as an error.
func New(options *Options) (*Metrics, error) {
	if options == nil || options.Registerer == nil {
		return nil, errors.New("prometheusadapter: Registerer is required")
	}
	if options.Namespace == "" {
		return nil, errors.New("prometheusadapter: Namespace is required")
	}
	metrics := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "requests_total",
			Help: "Model calls by client, model, operation and outcome.",
		}, []string{"client", "model", "operation", "outcome"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "request_duration_seconds",
			Help: "Model call duration, to the end of the stream or response, by client, model and operation.", Buckets: durationBuckets,
		}, []string{"client", "model", "operation"}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "tokens_total",
			Help: "Tokens the provider reported by client, model and type (prompt includes the cached ones).",
		}, []string{"client", "model", "type"}),
		cost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "cost_usd_total",
			Help: "Cost the provider billed, in US dollars, by client and model.",
		}, []string{"client", "model"}),
	}
	collectors := []prometheus.Collector{metrics.requests, metrics.durations, metrics.tokens, metrics.cost}
	for index, collector := range collectors {
		if err := options.Registerer.Register(collector); err != nil {
			for _, registered := range collectors[:index] {
				options.Registerer.Unregister(registered)
			}
			return nil, fmt.Errorf("prometheusadapter: register the %s LLM metrics: %w", options.Namespace, err)
		}
	}
	return metrics, nil
}

// CallStarted records nothing.
func (m *Metrics) CallStarted(ctx context.Context, _ *llm.Call) context.Context {
	return ctx
}

// CallFinished counts the call, its duration, its tokens and its cost.
func (m *Metrics) CallFinished(_ context.Context, call *llm.Call, outcome *llm.CallOutcome) {
	m.requests.WithLabelValues(call.Client, call.Model, call.Operation, outcomeLabel(outcome.Err)).Inc()
	m.durations.WithLabelValues(call.Client, call.Model, call.Operation).Observe(outcome.Duration.Seconds())
	usage := outcome.Usage
	if usage == nil {
		return
	}
	for kind, count := range map[string]int{
		TokensPrompt: usage.PromptTokens, TokensCompletion: usage.CompletionTokens,
		TokensCacheRead: usage.CacheReadTokens, TokensCacheWrite: usage.CacheWriteTokens,
	} {
		if count > 0 {
			m.tokens.WithLabelValues(call.Client, call.Model, kind).Add(float64(count))
		}
	}
	if usage.CostUSD > 0 {
		m.cost.WithLabelValues(call.Client, call.Model).Add(usage.CostUSD)
	}
}

func outcomeLabel(err error) string {
	switch {
	case err == nil:
		return OutcomeSuccess
	case errors.Is(err, context.Canceled):
		return OutcomeCancelled
	default:
		return OutcomeFailure
	}
}
