package ledgertest_test

import (
	"testing"

	"github.com/govengers/aikido/llm/budget"
	"github.com/govengers/aikido/llm/budget/ledgertest"
)

// The suite runs against the reference backend, so a broken check fails here and not only in a
// backend's own tests.
func TestRunConformance_PassesForTheMemoryLedger(t *testing.T) {
	ledgertest.RunConformance(t, func() budget.Ledger { return budget.NewMemoryLedger() })
}
