package agent

import (
	"context"

	"github.com/govengers/aikido/llm"
)

// History is the pluggable conversation-history interface (ADR-014).
//
// SessionID is opaque to aikido; implementations use it as a key into whatever
// store the caller has. v1 ships an in-memory backend under
// agent/history/memory; agent/history/historytest is the conformance suite a
// backend runs in its tests.
//
// Append is variadic so the agent can flush a turn's worth of messages (user,
// assistant, tool results) in a single backend round-trip; appending no
// messages is a no-op. Read returns the messages in append order, none (an
// empty or nil slice) for an unknown session; the caller may modify the
// returned slice.
// Implementations must be safe for concurrent use.
type History interface {
	Append(ctx context.Context, sessionID string, messages ...llm.Message) error
	Read(ctx context.Context, sessionID string) ([]llm.Message, error)
}
