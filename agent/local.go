package agent

import (
	historymemory "github.com/govengers/aikido/agent/history/memory"
)

// NewLocalSession is a convenience constructor for single-replica deployments
// and tests. It auto-supplies in-memory implementations for History and
// Locker if the caller leaves them unset, then validates like NewSession.
//
// For multi-replica deployments, or any setup needing a custom Locker (e.g.,
// Redis) or a persistent History (e.g., Postgres), use NewSession directly
// and supply your own plug-ins.
//
// History defaults to an in-memory store; conversation is lost when the
// process restarts.
func NewLocalSession(options *SessionOptions) (*Session, error) {
	if options == nil {
		return NewSession(options)
	}
	withPlugins := *options
	if withPlugins.History == nil {
		withPlugins.History = historymemory.New()
	}
	if withPlugins.Locker == nil {
		withPlugins.Locker = NewLocalLocker()
	}
	return NewSession(&withPlugins)
}
