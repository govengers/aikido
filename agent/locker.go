package agent

import (
	"context"
	"sync"
)

// Locker provides mutual exclusion keyed by session ID.
//
// The Session acquires a lock on its ID before each Run begins (covering
// History.Read at turn start) and releases it after EventEnd. Lock returns an
// error if ctx ends before the lock is acquired. The unlock function returned
// by Lock must be called exactly once; the agent always calls it via defer.
// Implementations must be safe for concurrent use.
type Locker interface {
	Lock(ctx context.Context, sessionID string) (unlock func(), err error)
}

// LocalLocker is the in-process implementation of Locker. Suitable for
// single-replica deployments and tests. Memory grows with the number of
// distinct session IDs seen; call Forget(id) to drop a key once the session
// is finished. It is safe for concurrent use.
type LocalLocker struct {
	mutex sync.Mutex
	// slots holds one single-slot semaphore per session ID: a held lock is a
	// value in the channel, so Lock can wait on it and on ctx in one select.
	slots map[string]chan struct{}
}

var _ Locker = (*LocalLocker)(nil)

// NewLocalLocker returns a fresh in-process Locker.
func NewLocalLocker() *LocalLocker {
	return &LocalLocker{slots: make(map[string]chan struct{})}
}

// Lock blocks until the lock for sessionID is acquired or ctx ends; then it
// returns ctx.Err(). The returned unlock function is idempotent.
func (l *LocalLocker) Lock(ctx context.Context, sessionID string) (func(), error) {
	slot := l.slot(sessionID)
	select {
	case slot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var once sync.Once
	return func() { once.Do(func() { <-slot }) }, nil
}

func (l *LocalLocker) slot(sessionID string) chan struct{} {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	slot, ok := l.slots[sessionID]
	if !ok {
		slot = make(chan struct{}, 1)
		l.slots[sessionID] = slot
	}
	return slot
}

// Forget drops the lock state for sessionID. Call it only after every Run of
// the session has completed; a Lock after Forget starts from a fresh, unheld
// lock.
func (l *LocalLocker) Forget(sessionID string) {
	l.mutex.Lock()
	delete(l.slots, sessionID)
	l.mutex.Unlock()
}
