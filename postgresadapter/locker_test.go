package postgresadapter_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/govengers/aikido/agent"
	"github.com/govengers/aikido/agent/lockertest"
	"github.com/govengers/aikido/llm"
	"github.com/govengers/aikido/llm/llmtest"
	"github.com/govengers/aikido/postgresadapter"
	"github.com/govengers/aikido/postgresadapter/internal/postgrestest"
)

// waitBound bounds a wait that must end, so a broken Locker fails the test instead of hanging it.
const waitBound = 10 * time.Second

func newLocker(t *testing.T, pool *pgxpool.Pool) *postgresadapter.Locker {
	t.Helper()
	return newLockerWith(t, &postgresadapter.LockerOptions{ConnConfig: pool.Config().ConnConfig})
}

func newLockerWith(t *testing.T, options *postgresadapter.LockerOptions) *postgresadapter.Locker {
	t.Helper()
	locker, err := postgresadapter.NewLocker(options)
	if err != nil {
		t.Fatal(err)
	}
	return locker
}

func TestLockerConformance(t *testing.T) {
	pool := postgrestest.New(t)
	lockertest.RunConformance(t, func() agent.Locker { return newLocker(t, pool) })
}

func TestNewLockerNeedsAConnConfig(t *testing.T) {
	if _, err := postgresadapter.NewLocker(nil); err == nil {
		t.Error("NewLocker(nil) should fail")
	}
	if _, err := postgresadapter.NewLocker(&postgresadapter.LockerOptions{}); err == nil {
		t.Error("NewLocker without a ConnConfig should fail")
	}
	config, err := pgx.ParseConfig("postgres://postgres@127.0.0.1/postgres")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := postgresadapter.NewLocker(&postgresadapter.LockerOptions{ConnConfig: config, MaximumConnections: -1}); err == nil {
		t.Error("NewLocker with a negative MaximumConnections should fail")
	}
}

func TestLockersOfTwoReplicasExcludeEachOther(t *testing.T) {
	pool := postgrestest.New(t)
	first, second := newLocker(t, pool), newLocker(t, pool)
	unlock, err := first.Lock(t.Context(), "replicated")
	if err != nil {
		t.Fatal(err)
	}
	// A live deadline: the probe must reach the server's lock, not fail before it connects.
	waiting, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := second.Lock(waiting, "replicated"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second replica's Lock on a held ID = %v; want context.DeadlineExceeded", err)
	}
	eventually(t, "the cancelled waiter leaves the queue", func() bool { _, queued := advisoryLocks(t, pool, "replicated"); return queued == 0 })
	unlock()
	if granted, waiting := advisoryLocks(t, pool, "replicated"); granted != 0 || waiting != 0 {
		t.Errorf("after unlock: %d granted, %d waiting; want the lock released at once", granted, waiting)
	}
	again, err := second.Lock(t.Context(), "replicated")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

// A waiter whose context ends must leave the server's lock queue at once, or its backend would wait
// until the holder unlocks and then hold the lock for a client that is gone. pgx sends a cancel
// request when it drops a connection on a cancelled context; this pins that.
func TestACancelledWaiterLeavesTheServersLockQueue(t *testing.T) {
	pool := postgrestest.New(t)
	locker := newLocker(t, pool)
	unlock, err := newLocker(t, pool).Lock(t.Context(), "queue") // another replica holds it
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	waiting, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		_, err := locker.Lock(waiting, "queue")
		result <- err
	}()
	eventually(t, "the waiter queues", func() bool { _, queued := advisoryLocks(t, pool, "queue"); return queued == 1 })
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled waiter = %v; want context.Canceled", err)
	}
	eventually(t, "the waiter leaves the queue", func() bool { _, queued := advisoryLocks(t, pool, "queue"); return queued == 0 })
}

func TestLockReportsAnUnreachableDatabase(t *testing.T) {
	config, err := pgx.ParseConfig("postgres://postgres@127.0.0.1:1/postgres?connect_timeout=5")
	if err != nil {
		t.Fatal(err)
	}
	locker, err := postgresadapter.NewLocker(&postgresadapter.LockerOptions{ConnConfig: config})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := locker.Lock(t.Context(), "s"); err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock on an unreachable database = %v; want a connect error", err)
	}
}

// The lock connection inherits the timeouts of the application's ConnConfig; none of them may end
// a wait for a lock while its context lives.
func TestServerTimeoutsDoNotEndTheWait(t *testing.T) {
	pool := postgrestest.New(t)
	config := pool.Config().ConnConfig.Copy()
	for _, name := range []string{"statement_timeout", "lock_timeout", "transaction_timeout", "idle_session_timeout"} {
		config.RuntimeParams[name] = "20ms"
	}
	holder, waiter := newLocker(t, pool), newLockerWith(t, &postgresadapter.LockerOptions{ConnConfig: config})
	unlock, err := holder.Lock(t.Context(), "timeouts")
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if _, err := waiter.Lock(waiting, "timeouts"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock with server timeouts of 20ms = %v; want it to wait until context.DeadlineExceeded", err)
	}
	unlock()
	again, err := waiter.Lock(t.Context(), "timeouts")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

// Calls for one session wait in the process, so they cost one connection; calls for other
// sessions beyond MaximumConnections wait for a free connection.
func TestLockConnectionsAreBounded(t *testing.T) {
	pool := postgrestest.New(t)
	config := pool.Config().ConnConfig.Copy()
	config.RuntimeParams["application_name"] = "bounded-locker"
	locker := newLockerWith(t, &postgresadapter.LockerOptions{ConnConfig: config, MaximumConnections: 2})
	unlockFirst, err := locker.Lock(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}

	const waiters = 30
	waiting, cancelWaiters := context.WithCancel(t.Context())
	results := make(chan error, waiters)
	for range waiters {
		go func() {
			_, err := locker.Lock(waiting, "first")
			results <- err
		}()
	}
	eventually(t, "every waiter queues in the process", func() bool { return postgresadapter.LocalCalls(locker, "first") == waiters+1 })
	second, cancel := context.WithTimeout(t.Context(), waitBound)
	defer cancel()
	unlockSecond, err := locker.Lock(second, "second")
	if err != nil {
		t.Fatalf("Lock of another session while one session has %d waiters = %v; want the second connection free", waiters, err)
	}
	if open := lockConnections(t, pool); open != 2 {
		t.Errorf("%d lock connections; want 2, one per session", open)
	}
	full, cancelFull := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancelFull()
	if unlock, err := locker.Lock(full, "third"); !errors.Is(err, context.DeadlineExceeded) {
		if err == nil {
			unlock()
		}
		t.Errorf("Lock beyond MaximumConnections = %v; want it to wait until context.DeadlineExceeded", err)
	}
	cancelWaiters()
	for range waiters {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Errorf("cancelled waiter = %v; want context.Canceled", err)
		}
	}

	unlockFirst()
	unlockThird, err := locker.Lock(t.Context(), "third")
	if err != nil {
		t.Fatal(err)
	}
	unlockThird()
	unlockSecond()
	if calls := postgresadapter.LocalCalls(locker, "first"); calls != 0 {
		t.Errorf("%d calls remembered for a released session; want it forgotten", calls)
	}
}

// lockConnections counts the open lock connections of the bounded-locker application.
func lockConnections(t *testing.T, pool *pgxpool.Pool) (open int) {
	t.Helper()
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_stat_activity WHERE application_name = 'bounded-locker'`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	return open
}

func TestLockLostReportsALockWhoseConnectionBroke(t *testing.T) {
	pool := postgrestest.New(t)
	var lost []error
	locker := newLockerWith(t, &postgresadapter.LockerOptions{
		ConnConfig: pool.Config().ConnConfig,
		LockLost: func(sessionID string, err error) {
			if sessionID == "lost" {
				lost = append(lost, err)
			}
		},
	})
	kept, err := locker.Lock(t.Context(), "kept")
	if err != nil {
		t.Fatal(err)
	}
	kept()
	unlock, err := locker.Lock(t.Context(), "lost")
	if err != nil {
		t.Fatal(err)
	}
	terminateLockBackend(t, pool, "lost")
	unlock()
	if len(lost) != 1 {
		t.Errorf("LockLost calls = %v; want one for the broken lock and none for the kept one", lost)
	}
}

// A LockLost that panics still leaves the session's turn and the connection free.
func TestAPanickingLockLostKeepsTheSessionAndTheConnectionFree(t *testing.T) {
	pool := postgrestest.New(t)
	locker := newLockerWith(t, &postgresadapter.LockerOptions{
		ConnConfig:         pool.Config().ConnConfig,
		MaximumConnections: 1,
		LockLost:           func(string, error) { panic("the application's callback broke") },
	})
	unlock, err := locker.Lock(t.Context(), "lost")
	if err != nil {
		t.Fatal(err)
	}
	terminateLockBackend(t, pool, "lost")
	func() {
		defer func() { _ = recover() }() // the agent drops a panic of unlock the same way
		unlock()
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	again, err := locker.Lock(ctx, "lost")
	if err != nil {
		t.Fatalf("Lock after the panic = %v", err)
	}
	again()
}

// terminateLockBackend ends the backend that holds the session's advisory lock.
func terminateLockBackend(t *testing.T, pool *pgxpool.Pool, sessionID string) {
	t.Helper()
	key := uint64(postgresadapter.LockKey(sessionID)) //nolint:gosec // G115: the key's bits, split the way pg_locks shows them
	var terminated bool
	if err := pool.QueryRow(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_locks
		WHERE locktype = 'advisory' AND granted AND classid::bigint = $1 AND objid::bigint = $2`,
		int64(key>>32), int64(key&0xFFFFFFFF)).Scan(&terminated); err != nil || !terminated {
		t.Fatalf("terminate the lock's backend = %v, %v", terminated, err)
	}
}

// Two replicas run the same session: the second run reads the history only after the first one
// appended, because the advisory lock serializes them.
func TestSessionsOnTwoReplicasTakeTurns(t *testing.T) {
	pool := postgrestest.New(t)
	history := mustHistory(t, pool)
	release := make(chan struct{})
	first := newReplicaSession(t, history, newLocker(t, pool), llmtest.NewStubClient(textReply("one", release)))
	second := newReplicaSession(t, history, newLocker(t, pool), llmtest.NewStubClient(textReply("two", nil)))

	firstEvents, err := first.Run(t.Context(), "first")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the first run holds the lock", func() bool { granted, _ := advisoryLocks(t, pool, "shared"); return granted == 1 })
	secondEvents, err := second.Run(t.Context(), "second")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, "the second run waits", func() bool { _, queued := advisoryLocks(t, pool, "shared"); return queued == 1 })
	close(release)
	for range firstEvents {
	}
	for range secondEvents {
	}

	messages, err := history.Read(t.Context(), "shared")
	if err != nil {
		t.Fatal(err)
	}
	var contents []string
	for _, message := range messages {
		contents = append(contents, message.Content)
	}
	if want := []string{"first", "one", "second", "two"}; len(contents) != 4 || contents[0] != want[0] || contents[2] != want[2] {
		t.Errorf("history = %q; want %q", contents, want)
	}
}

func newReplicaSession(t *testing.T, history agent.History, locker agent.Locker, client llm.Client) *agent.Session {
	t.Helper()
	session, err := agent.NewSession(&agent.SessionOptions{
		ID: "shared", Client: client, Model: "m", History: history, Locker: locker, MaximumTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func textReply(text string, block <-chan struct{}) llmtest.TurnScript {
	return llmtest.TurnScript{Block: block, Events: []llm.Event{{Kind: llm.EventTextDelta, Text: text}, {Kind: llm.EventEnd, FinishReason: "stop"}}}
}

const countAdvisoryLocks = `
SELECT count(*) FILTER (WHERE granted), count(*) FILTER (WHERE NOT granted)
FROM pg_locks
WHERE locktype = 'advisory' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())
    AND classid::bigint = $1 AND objid::bigint = $2 AND objsubid = 1`

// advisoryLocks counts the granted and the waiting advisory locks of a session ID.
func advisoryLocks(t *testing.T, pool *pgxpool.Pool, sessionID string) (granted, waiting int) {
	t.Helper()
	key := uint64(postgresadapter.LockKey(sessionID)) //nolint:gosec // G115: the key's bits, split the way pg_locks shows them
	if err := pool.QueryRow(t.Context(), countAdvisoryLocks, int64(key>>32), int64(key&0xFFFFFFFF)).Scan(&granted, &waiting); err != nil {
		t.Fatal(err)
	}
	return granted, waiting
}

// eventually polls condition until it holds, failing the test after 10 seconds.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
	}
}
