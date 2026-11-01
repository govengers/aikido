package agent

import "github.com/govengers/aikido/llm"

// EventKind identifies the kind of an agent.Event. String-valued so kinds can
// be added in any order without renumbering callers.
type EventKind string

const (
	EventText       EventKind = "text"
	EventThinking   EventKind = "thinking"
	EventToolCall   EventKind = "tool_call"
	EventToolResult EventKind = "tool_result"
	EventImage      EventKind = "image"
	EventUsage      EventKind = "usage"
	EventError      EventKind = "error"
	EventEnd        EventKind = "end"
)

// EndReason says why a run ended; EventEnd carries it. The string values are
// stable and safe to persist or send over the wire.
type EndReason string

// EndReason values emitted on EventEnd.
const (
	EndReasonStop         EndReason = "stop"
	EndReasonMaximumTurns EndReason = "max_turns"
	EndReasonError        EndReason = "error"
	EndReasonTimeout      EndReason = "timeout"
	EndReasonCancelled    EndReason = "cancelled"
)

// ToolResult is one tool execution result surfaced to the caller.
type ToolResult struct {
	CallID  string
	Name    string
	OK      bool
	Content any
	Error   string
}

// Event is one streaming event from a session run.
type Event struct {
	Kind       EventKind
	Text       string
	ToolCall   *llm.ToolCall
	ToolResult *ToolResult
	Image      *llm.ImagePart
	Usage      *llm.Usage
	Err        error
	EndReason  EndReason
}
