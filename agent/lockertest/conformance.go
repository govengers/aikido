// Package lockertest is the conformance suite every agent.Locker backend runs in its tests.
package lockertest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/govengers/aikido/agent"
)

// waitBound bounds every wait of the suite for an event that must happen, so a broken backend
// fails the test instead of hanging it.
const waitBound = 10 * time.Second

// probeBound is how long a probe waits for a lock that must stay held. It is a live deadline, not
// an expired one: a backend that checks the context before it looks at the lock (one that
// connects first) would fail an expired probe whatever the lock's state.
const probeBound = 100 * time.Millisecond

// RunConformance executes the standard suite against a Locker factory.
//
// factory must return a Locker with no lock held on every call; lockers of one factory may share
// state (a database), so the suite uses its own session IDs per sub-test. Sub-tests are serial.
// A backend calls this from a TestX function in its package.
func RunConformance(t *testing.T, factory func() agent.Locker) {
	t.Helper()
	conformanceTests := []struct {
		name string
		run  func(t *testing.T, locker agent.Locker)
	}{
		{"HeldIDWaitsUntilTheContextEnds", testHeldIDWaitsUntilTheContextEnds},
		{"OtherIDsAreIndependent", testOtherIDsAreIndependent},
		{"UnlockHandsTheLockToAWaiter", testUnlockHandsTheLockToAWaiter},
		{"RepeatedUnlockKeepsTheNextHoldersLock", testRepeatedUnlockKeepsTheNextHoldersLock},
		{"CancelledWaiterLeavesTheLockUsable", testCancelledWaiterLeavesTheLockUsable},
		{"MutualExclusionUnderContention", testMutualExclusionUnderContention},
	}
	for _, conformanceTest := range conformanceTests {
		t.Run(conformanceTest.name, func(t *testing.T) {
			conformanceTest.run(t, factory())
		})
	}
}

func testHeldIDWaitsUntilTheContextEnds(t *testing.T, locker agent.Locker) {
	unlock := mustLock(t, locker, "held")
	defer unlock()
	if err := probe(locker, "held"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock on a held ID = %v; want it to wait until context.DeadlineExceeded", err)
	}
}

func testOtherIDsAreIndependent(t *testing.T, locker agent.Locker) {
	unlock := mustLock(t, locker, "first")
	defer unlock()
	other := mustLock(t, locker, "second")
	other()
}

func testUnlockHandsTheLockToAWaiter(t *testing.T, locker agent.Locker) {
	unlock := mustLock(t, locker, "handover")
	acquired := make(chan func(), 1)
	failed := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitBound)
		defer cancel()
		next, err := locker.Lock(ctx, "handover")
		if err != nil {
			failed <- err
			return
		}
		acquired <- next
	}()
	select {
	case next := <-acquired:
		next()
		t.Fatal("the waiter got the lock while it was held")
	case err := <-failed:
		t.Fatalf("waiter: %v", err)
	default:
	}
	unlock()
	select {
	case next := <-acquired:
		next()
	case err := <-failed:
		t.Fatalf("waiter after unlock: %v", err)
	}
}

func testRepeatedUnlockKeepsTheNextHoldersLock(t *testing.T, locker agent.Locker) {
	unlock := mustLock(t, locker, "repeat")
	unlock()
	unlock()
	relock := mustLock(t, locker, "repeat")
	defer relock()
	unlock() // a stale unlock must not release the new holder's lock
	if err := probe(locker, "repeat"); err == nil {
		t.Error("a repeated unlock released the next holder's lock")
	}
}

// testCancelledWaiterLeavesTheLockUsable cancels a waiter while the lock is held, then checks that
// the lock still passes from the holder to the next caller: a backend that leaves the cancelled
// waiter queued would hand it the lock and block everyone after it.
func testCancelledWaiterLeavesTheLockUsable(t *testing.T, locker agent.Locker) {
	unlock := mustLock(t, locker, "cancel")
	if err := probe(locker, "cancel"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter whose context ended = %v; want context.DeadlineExceeded", err)
	}
	unlock()
	next := mustLock(t, locker, "cancel")
	next()
}

func testMutualExclusionUnderContention(t *testing.T, locker agent.Locker) {
	const contenders, rounds = 4, 20
	var (
		mutex  sync.Mutex
		inside int
		errs   []error
	)
	enter := func() {
		mutex.Lock()
		defer mutex.Unlock()
		inside++
		if inside > 1 {
			errs = append(errs, errors.New("two holders inside the critical section"))
		}
	}
	leave := func() {
		mutex.Lock()
		defer mutex.Unlock()
		inside--
	}
	var group sync.WaitGroup
	for range contenders {
		group.Go(func() {
			for range rounds {
				ctx, cancel := context.WithTimeout(context.Background(), waitBound)
				unlock, err := locker.Lock(ctx, "contended")
				cancel()
				if err != nil {
					mutex.Lock()
					errs = append(errs, err)
					mutex.Unlock()
					return
				}
				enter()
				handOff() // a scheduling point inside, so a lock that excludes nothing gets caught
				leave()
				unlock()
			}
		})
	}
	group.Wait()
	for _, err := range errs {
		t.Error(err)
	}
}

// handOff lets another goroutine run.
func handOff() {
	done := make(chan struct{})
	go close(done)
	<-done
}

// probe tries for probeBound to take a lock that should stay held and releases it if it got it.
func probe(locker agent.Locker, sessionID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), probeBound)
	defer cancel()
	unlock, err := locker.Lock(ctx, sessionID)
	if err == nil {
		unlock()
	}
	return err
}

func mustLock(t *testing.T, locker agent.Locker, sessionID string) func() {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	unlock, err := locker.Lock(ctx, sessionID)
	if err != nil {
		t.Fatalf("Lock(%s): %v", sessionID, err)
	}
	return unlock
}
