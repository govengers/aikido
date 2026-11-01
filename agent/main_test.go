package agent

import (
	"testing"

	"go.uber.org/goleak"
)

// Every run starts a goroutine; a test that leaves one behind fails (TST-009).
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
