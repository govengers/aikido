# Observability

aikido never logs (LIB-003). It reports model calls through `llm.Observer`, and three adapter packages translate them into the govengers signals: Prometheus metrics, OpenTelemetry traces, wide-event fields. A service imports only the adapters it uses; the others stay out of its build.

## Wiring

Wrap the chat client once in `main` and give the wrapped client to `agent.Session` (or call it directly):

```go
llmMetrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "billing"}) // llm/prometheusadapter
if err != nil {
    return err
}
llmSpans, err := oteladapter.New(&oteladapter.Options{TracerProvider: traces.TracerProvider()}) // llm/oteladapter, provider from govengers/telemetry/tracing
if err != nil {
    return err
}
chat, err := llm.NewObservedClient(&llm.ObservedClientOptions{
    Client:    openRouter,   // *openrouter.Client
    Name:      "openrouter", // the client label and gen_ai.provider.name
    Observers: []llm.Observer{llmSpans, llmMetrics, wideeventadapter.New()},
})
if err != nil {
    return err
}
session, err := agent.NewSession(&agent.SessionOptions{Client: chat, ...})
```

`ObservedClient` implements only `llm.Client` (`Stream`, `Complete`). The optional capabilities of the wrapped client (`ImageGenerator`, `SpeechGenerator`, `VideoGenerator`, `Decider`) are not reachable through it; keep the original client for those calls.

Observers start in order and finish in reverse order. `CallStarted` returns the context the call runs in, so the tracing observer's span is the parent of the provider's own HTTP spans (an `otelhttp` transport in `openrouter.Options.HTTPClient`). Put `oteladapter` first.

## Metrics (`llm/prometheusadapter`)

| Metric | Labels |
|---|---|
| `<ns>_llm_requests_total` | `client`, `model`, `operation` (`stream`, `complete`), `outcome` (`success`, `failure`, `cancelled`) |
| `<ns>_llm_request_duration_seconds` | `client`, `model`, `operation`; to the end of the stream or response, buckets 0.25 s to about 10 min |
| `<ns>_llm_tokens_total` | `client`, `model`, `type` (`prompt` with the cached ones, `completion`, `cache_read`, `cache_write`) |
| `<ns>_llm_cost_usd_total` | `client`, `model` |

`cancelled` is a caller that gave up (`context.Canceled`); a deadline that passed (`LLMCallTimeout`) is a `failure`. The `model` label is `Request.Model`: keep it a value your code or configuration chooses, never a string a user sends.

```promql
sum by (model) (rate(billing_llm_cost_usd_total[1h])) * 3600 * 24             # dollars per day per model
sum(rate(billing_llm_requests_total{outcome="failure"}[5m])) by (client, model) # failing calls
histogram_quantile(0.95, sum by (le, model) (rate(billing_llm_request_duration_seconds_bucket[5m])))
```

## Traces (`llm/oteladapter`)

One client span per call, named `chat <model>`, in the caller's trace, after the OpenTelemetry GenAI conventions: `gen_ai.operation.name` (`chat`), `gen_ai.provider.name` (the `Name`), `gen_ai.request.model`, `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `gen_ai.response.finish_reasons`, plus `aikido.llm.operation`. A failed call records the error and sets the status to error. Prompts, completions and tool arguments never reach a span.

## Wide events (`llm/wideeventadapter`)

The calls of one unit of work are summed on its event (an agent run makes many): `llm.calls`, `llm.failures`, `llm.duration`, `llm.prompt_tokens`, `llm.completion_tokens`, `llm.cache_read_tokens`, `llm.cost_usd`, and the last call's `llm.client`, `llm.model`, `llm.finish_reason`, `llm.error`. A context without an event records nothing.

## Your own observer

Implement `llm.Observer`: `CallStarted(ctx, *llm.Call) context.Context` and `CallFinished(ctx, *llm.Call, *llm.CallOutcome)`. Both run on the caller's path (the stream's goroutine for `Stream`), so keep them fast; return `ctx` from `CallStarted` when you keep nothing.
