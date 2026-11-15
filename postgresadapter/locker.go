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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgconn/ctxwatch"

	"github.com/govengers/aikido/agent"
)

// LockerOptions configures a Locker.
type LockerOptions struct {
	// ConnConfig is the database the locks are taken on; required. Every replica must use the same
	// database. pool.Config().ConnConfig of the application's pool works; it is copied.
	ConnConfig *pgx.ConnConfig
}

// Locker is an agent.Locker for several replicas: a lock is a session-level advisory lock on a
// connection of its own, opened by Lock and closed by the unlock function. It is safe for
// concurrent use.
//
// The connection holds the lock, so a replica that dies or loses the connection releases it, and
// nothing has to expire. The cost is one database connection per running and per waiting session,
// outside the application's pool; size max_connections for it. A connection lost during a run
// releases the lock while the run goes on, and the agent.Locker contract has no way to tell the run:
// another replica can then start a run of the same session. Locks need a direct connection or a
// session-mode pooler: a transaction-mode pooler (PgBouncer) moves the session's lock between
// clients.
type Locker struct {
	config *pgx.ConnConfig
}

var _ agent.Locker = (*Locker)(nil)

// unlockTimeout bounds the unlock statement and the close of a lock's connection; unlock runs after
// the run ended, without a caller context.
const unlockTimeout = 5 * time.Second

// NewLocker returns a Locker on options.ConnConfig.
func NewLocker(options *LockerOptions) (*Locker, error) {
	if options == nil || options.ConnConfig == nil {
		return nil, errors.New("aikido/postgresadapter: LockerOptions.ConnConfig is required")
	}
	config := options.ConnConfig.Copy()
	// A waiter whose context ends cancels its pg_advisory_lock with a cancel request instead of
	// dropping the socket, so the server stops waiting at once instead of holding a lock for a
	// client that is gone.
	config.BuildContextWatcherHandler = func(connection *pgconn.PgConn) ctxwatch.Handler {
		return &pgconn.CancelRequestContextWatcherHandler{Conn: connection, DeadlineDelay: unlockTimeout}
	}
	// The lock connection idles for the whole run; a server-wide idle timeout must not end it.
	if config.RuntimeParams == nil {
		config.RuntimeParams = map[string]string{} // Copy clones a non-nil map
	}
	config.RuntimeParams["idle_session_timeout"] = "0"
	return &Locker{config: config}, nil
}

// Lock opens a connection and waits on it for the session's advisory lock until it is granted or
// ctx ends; then it returns ctx.Err(). The unlock function releases the lock and closes the
// connection; it is idempotent.
func (l *Locker) Lock(ctx context.Context, sessionID string) (func(), error) {
	connection, err := pgx.ConnectConfig(ctx, l.config)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("aikido/postgresadapter: lock %s: connect: %w", sessionID, err)
	}
	key := lockKey(sessionID)
	if _, err := connection.Exec(ctx, "SELECT pg_advisory_lock($1)", key); err != nil {
		closeConnection(connection) // also releases the lock if it was granted after all
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("aikido/postgresadapter: lock %s: %w", sessionID, err)
	}
	return sync.OnceFunc(func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer cancel()
		// Unlock first, so the next waiter gets the lock now and not once the server noticed the
		// closed connection; the close releases it anyway when the statement fails.
		_, _ = connection.Exec(unlockContext, "SELECT pg_advisory_unlock($1)", key) //nolint:errcheck // closing the connection releases the lock too
		closeConnection(connection)
	}), nil
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
