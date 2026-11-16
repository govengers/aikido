package postgresadapter

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/govengers/aikido/agent"
)

// DefaultMaximumConnections is the number of lock connections a Locker opens at most when
// LockerOptions.MaximumConnections is 0.
const DefaultMaximumConnections = 20

// LockerOptions configures a Locker.
type LockerOptions struct {
	// ConnConfig is the database the locks are taken on; required. Every replica must use the same
	// database. pool.Config().ConnConfig of the application's pool works; it is copied.
	ConnConfig *pgx.ConnConfig
	// MaximumConnections caps the lock connections this Locker holds open at once, that is the
	// sessions of this replica that run or wait on another replica at the same time; further Lock
	// calls wait in the process until a connection is free or their context ends. 0 is
	// DefaultMaximumConnections.
	MaximumConnections int
	// LockLost is told when releasing a lock found it already gone: its connection broke while the
	// run went on, so another replica may have run the same session meanwhile. Optional; nil drops
	// it.
	LockLost func(sessionID string, err error)
}

// Locker is an agent.Locker for several replicas: a lock is a session-level advisory lock on a
// connection of its own, opened by Lock and closed by the unlock function. It is safe for
// concurrent use.
//
// The connection holds the lock, so a replica that dies or loses the connection releases it, and
// nothing has to expire. Calls for one session ID take turns in the process first, so a session
// costs one database connection per replica however many calls wait for it; the connections of a
// Locker are capped by LockerOptions.MaximumConnections, outside the application's pool, so size
// max_connections for the replicas times that. A connection lost during a run releases the lock
// while the run goes on, and the agent.Locker contract has no way to tell the run: another replica
// can then start a run of the same session (LockerOptions.LockLost reports it afterwards). Locks
// need a direct connection or a session-mode pooler: a transaction-mode pooler (PgBouncer) moves
// the session's lock between clients.
type Locker struct {
	config      *pgx.ConnConfig
	connections chan struct{} // one token per open lock connection
	lockLost    func(sessionID string, err error)

	mutex    sync.Mutex
	sessions map[string]*localTurn
}

// localTurn makes the calls of one session ID in the process take turns; it lives while a call
// holds or waits for it.
type localTurn struct {
	token chan struct{} // holds a value while a call has the turn
	users int           // calls holding or waiting, guarded by Locker.mutex
}

var _ agent.Locker = (*Locker)(nil)

// unlockTimeout bounds the unlock statement and the close of a lock's connection; unlock runs after
// the run ended, without a caller context.
const unlockTimeout = 5 * time.Second

// clearTimeouts turns off every server timeout that could end the wait for a lock or the idle
// connection that holds it: the copied ConnConfig, the role or the database may set them. It is
// one statement after connect, not startup parameters, so a session-mode pooler that refuses
// unknown startup parameters still works, and it sets only the parameters the server knows
// (transaction_timeout is new in PostgreSQL 17).
const clearTimeouts = `
SELECT set_config(name, '0', false) FROM pg_settings
WHERE name IN ('statement_timeout', 'lock_timeout', 'transaction_timeout', 'idle_session_timeout',
    'idle_in_transaction_session_timeout')`

// NewLocker returns a Locker on options.ConnConfig.
func NewLocker(options *LockerOptions) (*Locker, error) {
	if options == nil || options.ConnConfig == nil {
		return nil, errors.New("aikido/postgresadapter: LockerOptions.ConnConfig is required")
	}
	maximum := options.MaximumConnections
	if maximum == 0 {
		maximum = DefaultMaximumConnections
	}
	if maximum < 0 {
		return nil, fmt.Errorf("aikido/postgresadapter: LockerOptions.MaximumConnections is %d; want 0 or more", maximum)
	}
	return &Locker{
		config:      options.ConnConfig.Copy(),
		connections: make(chan struct{}, maximum),
		lockLost:    options.LockLost,
		sessions:    map[string]*localTurn{},
	}, nil
}

// Lock waits for the session's turn in the process and a free connection, opens it and waits on it
// for the session's advisory lock, until the lock is granted or ctx ends; then it returns
// ctx.Err(). The unlock function releases the lock and closes the connection; it is idempotent.
func (l *Locker) Lock(ctx context.Context, sessionID string) (func(), error) {
	turn := l.join(sessionID)
	select {
	case turn.token <- struct{}{}:
	case <-ctx.Done():
		l.leave(sessionID, turn, false)
		return nil, ctx.Err()
	}
	select {
	case l.connections <- struct{}{}:
	case <-ctx.Done():
		l.leave(sessionID, turn, true)
		return nil, ctx.Err()
	}
	connection, err := l.lock(ctx, sessionID)
	if err != nil {
		<-l.connections
		l.leave(sessionID, turn, true)
		return nil, err
	}
	return sync.OnceFunc(func() {
		defer l.leave(sessionID, turn, true)
		defer func() { <-l.connections }() // deferred: a panic of LockLost must not keep them taken
		l.unlock(connection, sessionID)
	}), nil
}

// lock opens a connection and takes the session's advisory lock on it.
func (l *Locker) lock(ctx context.Context, sessionID string) (*pgx.Conn, error) {
	connection, err := pgx.ConnectConfig(ctx, l.config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("aikido/postgresadapter: lock %s: connect: %w", sessionID, err)
	}
	_, err = connection.Exec(ctx, clearTimeouts)
	if err == nil {
		_, err = connection.Exec(ctx, "SELECT pg_advisory_lock($1)", lockKey(sessionID))
	}
	if err != nil {
		closeConnection(connection) // also releases the lock if it was granted after all
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("aikido/postgresadapter: lock %s: %w", sessionID, err)
	}
	return connection, nil
}

// unlock releases the session's lock and closes its connection, and tells LockLost when the lock
// was gone already.
func (l *Locker) unlock(connection *pgx.Conn, sessionID string) {
	unlockContext, cancel := context.WithTimeout(context.Background(), unlockTimeout)
	defer cancel()
	// Unlock first, so the next waiter gets the lock now and not once the server noticed the
	// closed connection; the close releases it anyway when the statement fails.
	var released bool
	err := connection.QueryRow(unlockContext, "SELECT pg_advisory_unlock($1)", lockKey(sessionID)).Scan(&released)
	closeConnection(connection)
	if l.lockLost == nil || (err == nil && released) {
		return
	}
	if err == nil {
		err = errors.New("the server held no lock")
	}
	l.lockLost(sessionID, fmt.Errorf("aikido/postgresadapter: unlock %s: %w", sessionID, err))
}

// join registers a call for the session's turn.
func (l *Locker) join(sessionID string) *localTurn {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	turn := l.sessions[sessionID]
	if turn == nil {
		turn = &localTurn{token: make(chan struct{}, 1)}
		l.sessions[sessionID] = turn
	}
	turn.users++
	return turn
}

// leave gives up the session's turn if the call had it and forgets the turn once no call uses it.
func (l *Locker) leave(sessionID string, turn *localTurn, had bool) {
	if had {
		<-turn.token
	}
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if turn.users--; turn.users == 0 {
		delete(l.sessions, sessionID)
	}
}

func closeConnection(connection *pgx.Conn) {
	closeContext, cancel := context.WithTimeout(context.Background(), unlockTimeout)
	defer cancel()
	_ = connection.Close(closeContext) // the server ends the session, and its locks, either way
}

// lockKey maps a session ID to the advisory lock key: the first 8 bytes of a SHA-256 over a
// namespace and the ID. Two IDs with the same key wait for each other, which costs time, not
// correctness.
func lockKey(sessionID string) int64 {
	sum := sha256.Sum256([]byte("govengers/aikido/agent.Locker\x00" + sessionID))
	return int64(binary.BigEndian.Uint64(sum[:8])) //nolint:gosec // G115: the bits are the key, the sign does not matter
}
