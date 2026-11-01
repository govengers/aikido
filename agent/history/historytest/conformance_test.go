package historytest_test

import (
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/history/historytest"
	"github.com/govengers/aikido/agent/history/memory"
)

// The suite runs against the reference backend, so a broken check fails here
// and not only in a backend's own tests.
func TestRunConformance_PassesForTheMemoryBackend(t *testing.T) {
	historytest.RunConformance(t, func() agent.History { return memory.NewHistory() })
}
