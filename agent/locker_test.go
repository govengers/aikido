package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/lockertest"
)

func TestLocalLockerConformance(t *testing.T) {
	lockertest.RunConformance(t, func() agent.Locker { return agent.NewLocalLocker() })
}

func TestLocalLockerForgetStartsFromAnUnheldLock(t *testing.T) {
	locker := agent.NewLocalLocker()
	unlock, err := locker.Lock(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	locker.Forget("s")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	afterForget, err := locker.Lock(ctx, "s")
	if err != nil {
		t.Fatalf("Lock after Forget: %v", err)
	}
	afterForget()
}
