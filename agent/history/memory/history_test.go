package memory_test

import (
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history"
	"github.com/govengers/aikido/agent/history/memory"
)

var _ agent.History = (*memory.History)(nil)

func TestConformance(t *testing.T) {
	history.RunConformance(t, func() agent.History { return memory.NewHistory() })
}
