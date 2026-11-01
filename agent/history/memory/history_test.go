package memory_test

import (
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/historytest"
	"github.com/govengers/aikido/agent/history/memory"
)

var _ agent.History = (*memory.History)(nil)

func TestConformance(t *testing.T) {
	historytest.RunConformance(t, func() agent.History { return memory.NewHistory() })
}
