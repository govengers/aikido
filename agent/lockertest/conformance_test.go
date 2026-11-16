package lockertest_test

import (
	"testing"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/lockertest"
)

// The suite runs against the reference backend, so a broken check fails here and not only in a
// backend's own tests.
func TestRunConformance_PassesForTheLocalLocker(t *testing.T) {
	lockertest.RunConformance(t, func() agent.Locker { return agent.NewLocalLocker() })
}
