// Package wideeventadapter records the model calls of an llm.ObservedClient on the wide event
// of the unit of work they belong to (github.com/govengers/wide-event-logger). Fields implements
// llm.Observer; put it into llm.ObservedClientOptions.Observers. A context without an event
// records nothing.
//
// One unit of work, an agent run inside a request for example, makes many calls; they are
// summed into one set of fields instead of one field per call. No prompt or completion text is
// recorded.
package wideeventadapter

import (
	"context"
	"sync"
	"time"

	wideevent "github.com/govengers/wide-event-logger"

	"github.com/govengers/aikido/llm"
)

// The fields Fields records. The counters and the duration are sums over the event's calls;
// client, model, finish reason and error are the last call's.
const (
	KeyCalls            = "llm.calls"
	KeyFailures         = "llm.failures"
	KeyDuration         = "llm.duration"
	KeyPromptTokens     = "llm.prompt_tokens"     //nolint:gosec // G101: a field name, not a credential
	KeyCompletionTokens = "llm.completion_tokens" //nolint:gosec // G101: a field name, not a credential
	KeyCacheReadTokens  = "llm.cache_read_tokens" //nolint:gosec // G101: a field name, not a credential
	KeyCostUSD          = "llm.cost_usd"
	KeyClient           = "llm.client"
	KeyModel            = "llm.model"
	KeyFinishReason     = "llm.finish_reason"
	KeyError            = "llm.error"
)

// Fields records model calls on the context's wide event; it implements llm.Observer and is
// safe for concurrent use.
type Fields struct {
	mutex sync.Mutex // makes the read-and-add of the sums atomic across concurrent calls
}

var _ llm.Observer = (*Fields)(nil)

// New returns the observer.
func New() *Fields {
	return &Fields{}
}

// CallStarted records nothing.
func (f *Fields) CallStarted(ctx context.Context, _ *llm.Call) context.Context {
	return ctx
}

// CallFinished adds the call to the context's wide event.
func (f *Fields) CallFinished(ctx context.Context, call *llm.Call, outcome *llm.CallOutcome) {
	we := wideevent.FromContext(ctx)
	if we == nil {
		return
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	current := values(we)
	we.Int(KeyCalls, intValue(current[KeyCalls])+1).
		Duration(KeyDuration, durationValue(current[KeyDuration])+outcome.Duration).
		String(KeyClient, call.Client).
		String(KeyModel, call.Model)
	if outcome.Err != nil {
		we.Int(KeyFailures, intValue(current[KeyFailures])+1).Err(KeyError, outcome.Err)
	}
	if outcome.FinishReason != "" {
		we.String(KeyFinishReason, outcome.FinishReason)
	}
	if usage := outcome.Usage; usage != nil {
		we.Int(KeyPromptTokens, intValue(current[KeyPromptTokens])+usage.PromptTokens).
			Int(KeyCompletionTokens, intValue(current[KeyCompletionTokens])+usage.CompletionTokens).
			Int(KeyCacheReadTokens, intValue(current[KeyCacheReadTokens])+usage.CacheReadTokens).
			Float64(KeyCostUSD, floatValue(current[KeyCostUSD])+usage.CostUSD)
	}
}

// values returns the event's llm fields so far.
func values(we *wideevent.Event) map[string]any {
	current := map[string]any{}
	for _, field := range we.Fields() {
		current[field.Key] = field.Value
	}
	return current
}

func intValue(value any) int {
	number, _ := value.(int)
	return number
}

func floatValue(value any) float64 {
	number, _ := value.(float64)
	return number
}

func durationValue(value any) time.Duration {
	duration, _ := value.(time.Duration)
	return duration
}
