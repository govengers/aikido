package llm

import (
	"testing"
	"time"
)

func TestUsageSetTiming(t *testing.T) {
	start := time.Unix(0, 0)
	usage := Usage{CompletionTokens: 100}
	usage.SetTiming(start, start.Add(500*time.Millisecond), start.Add(2500*time.Millisecond))
	if usage.Latency != 500*time.Millisecond || usage.Duration != 2500*time.Millisecond || usage.TokensPerSecond != 50 {
		t.Errorf("stream timing = %+v", usage)
	}
	usage.SetTiming(start, time.Time{}, start.Add(2*time.Second))
	if usage.Latency != 2*time.Second || usage.TokensPerSecond != 50 {
		t.Errorf("complete timing = %+v", usage)
	}
	usage.SetTiming(start, start.Add(time.Second), start.Add(time.Second+100*time.Millisecond))
	if usage.TokensPerSecond != 0 {
		t.Errorf("burst rate = %v; want 0", usage.TokensPerSecond)
	}
}
