package llm_test

import (
	"testing"

	"go.uber.org/goleak"
)

// ObservedClient forwards every stream from a goroutine of its own; none may outlive its stream.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
