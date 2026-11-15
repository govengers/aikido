package postgresadapter

// AppendMessages exposes Append's statement, for a test that runs it in a transaction of its own.
const AppendMessages = appendMessages

// LockKey exposes the advisory lock key of a session ID to the tests.
var LockKey = lockKey

// LocalCalls returns how many calls of the locker hold or wait for the session's turn in the
// process.
func LocalCalls(locker *Locker, sessionID string) int {
	locker.mutex.Lock()
	defer locker.mutex.Unlock()
	if turn := locker.sessions[sessionID]; turn != nil {
		return turn.users
	}
	return 0
}
