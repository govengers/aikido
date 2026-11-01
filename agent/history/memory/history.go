// Package memory provides an in-process agent.History backend.
//
// Suitable for single-replica deployments and tests; messages are lost when
// the process restarts.
package memory

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/govengers/aikido/llm"
)

// History is the in-memory implementation of agent.History. It is safe for
// concurrent use; the zero value is not usable, use New.
type History struct {
	mutex    sync.RWMutex
	sessions map[string][]llm.Message
}

// New returns an empty in-memory History.
func New() *History {
	return &History{sessions: make(map[string][]llm.Message)}
}

// Append stores deep copies of messages (images, audio, tool calls and cache
// breakpoints included) after the session's earlier messages, so the caller
// may change its messages afterwards. Appending no messages is a no-op. It
// returns ErrEmptySessionID for an empty sessionID and the context's error if
// ctx is already done.
func (h *History) Append(ctx context.Context, sessionID string, messages ...llm.Message) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("aikido/agent/history/memory: append to %s: %w", sessionID, err)
	}
	if sessionID == "" {
		return ErrEmptySessionID
	}
	if len(messages) == 0 {
		return nil
	}
	stored := cloneMessages(messages)
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.sessions[sessionID] = append(h.sessions[sessionID], stored...)
	return nil
}

// Read returns a deep copy of the session's messages in append order, nil for
// an unknown session. It returns ErrEmptySessionID for an empty sessionID and
// the context's error if ctx is already done.
func (h *History) Read(ctx context.Context, sessionID string) ([]llm.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("aikido/agent/history/memory: read %s: %w", sessionID, err)
	}
	if sessionID == "" {
		return nil, ErrEmptySessionID
	}
	h.mutex.RLock()
	defer h.mutex.RUnlock()
	stored := h.sessions[sessionID]
	if stored == nil {
		return nil, nil
	}
	return cloneMessages(stored), nil
}

// cloneMessages copies messages down to their byte slices, so the copy
// shares no memory with them.
func cloneMessages(messages []llm.Message) []llm.Message {
	cloned := make([]llm.Message, len(messages))
	for index, message := range messages {
		message.Images = slices.Clone(message.Images)
		for imageIndex := range message.Images {
			message.Images[imageIndex].Data = bytes.Clone(message.Images[imageIndex].Data)
		}
		message.Audio = slices.Clone(message.Audio)
		for audioIndex := range message.Audio {
			message.Audio[audioIndex].Data = bytes.Clone(message.Audio[audioIndex].Data)
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		if message.Cache != nil {
			cache := *message.Cache
			message.Cache = &cache
		}
		cloned[index] = message
	}
	return cloned
}
