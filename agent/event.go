package agent

import "github.com/govengers/aikido/llm"

// EventKind identifies the kind of an agent.Event. String-valued so kinds can
// be added in any order without renumbering callers.
type EventKind string

// EventKind values. Each names the Event field that carries its payload.
const (
	// EventText carries a streamed text delta of the assistant reply in Text.
	EventText EventKind = "text"
	// EventThinking carries streamed model reasoning in Text. It is not part
	// of the persisted conversation.
	EventThinking EventKind = "thinking"
	// EventToolCall carries a tool call of the model in ToolCall. It is
	// emitted after the tool ran, right before its EventToolResult.
	EventToolCall EventKind = "tool_call"
	// EventToolResult carries the outcome of the preceding EventToolCall in
	// ToolResult.
	EventToolResult EventKind = "tool_result"
	// EventImage carries an image of the assistant reply in Image.
	EventImage EventKind = "image"
	// EventUsage carries the token usage of one model call in Usage.
	EventUsage EventKind = "usage"
	// EventError carries the error that ends the run in Err; EventEnd follows.
	EventError EventKind = "error"
	// EventEnd is the last event of a run and carries EndReason.
	EventEnd EventKind = "end"
)

// EndReason says why a run ended; EventEnd carries it. The string values are
// stable and safe to persist or send over the wire.
type EndReason string

// EndReason values emitted on EventEnd.
const (
	// EndReasonStop means the model replied without tool calls.
	EndReasonStop EndReason = "stop"
	// EndReasonMaximumTurns means SessionOptions.MaximumTurns model calls ran
	// without a final reply. History is still updated.
	EndReasonMaximumTurns EndReason = "max_turns"
	// EndReasonError means the run failed; EventError carries the cause.
	EndReasonError EndReason = "error"
	// EndReasonTimeout means the caller's deadline, RunTimeout or
	// LLMCallTimeout expired.
	EndReasonTimeout EndReason = "timeout"
	// EndReasonCancelled means the caller cancelled the run's context.
	EndReasonCancelled EndReason = "cancelled"
)

// ToolResult is one tool execution result surfaced to the caller.
type ToolResult struct {
	// CallID is the llm.ToolCall ID this result answers.
	CallID string
	// Name is the called tool's name.
	Name string
	// OK is false when the tool failed; Error then holds its message.
	OK bool
	// Content is the tool's full result when OK. The model gets it as JSON,
	// cut at SessionOptions.MaximumToolResultBytes.
	Content any
	// Error is the failure message when not OK.
	Error string
	// ModelContent is the tool message content the model received: the JSON
	// of Content or Error, cut at SessionOptions.MaximumToolResultBytes. Drain
	// stores this copy, so a history rebuilt from events matches what the
	// model saw.
	ModelContent string
}

// Event is one streaming event from a session run. Kind says which of the
// other fields is set.
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
