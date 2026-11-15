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

func newLocker(t *testing.T, pool *pgxpool.Pool) *postgresadapter.Locker {
	t.Helper()
	locker, err := postgresadapter.NewLocker(&postgresadapter.LockerOptions{ConnConfig: pool.Config().ConnConfig})
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
}

func TestLockersOfTwoReplicasExcludeEachOther(t *testing.T) {
	pool := postgrestest.New(t)
	first, second := newLocker(t, pool), newLocker(t, pool)
	unlock, err := first.Lock(t.Context(), "replicated")
	if err != nil {
		t.Fatal(err)
	}
	expired, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	if _, err := second.Lock(expired, "replicated"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second replica's Lock on a held ID = %v; want context.DeadlineExceeded", err)
	}
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
	unlock, err := locker.Lock(t.Context(), "queue")
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
