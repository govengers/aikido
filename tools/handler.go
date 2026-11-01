package tools

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
)

// Environment carries per-call execution context to a Handler.
type Environment struct {
	// SessionID is the agent session the call belongs to.
	SessionID string
	// TurnID identifies the model reply that made the call; calls of the
	// same reply share it.
	TurnID uuid.UUID
}

// Result is what a Handler returns. Content is JSON-serialized to the model;
// nil is sent as JSON null.
type Result struct {
	Content any
}

// Handler is the function signature every tool implements. arguments holds
// the model's raw JSON arguments, unvalidated. A returned error is shown to
// the model as the tool's failure, so it can correct itself.
type Handler func(ctx context.Context, arguments json.RawMessage, environment Environment) (Result, error)
