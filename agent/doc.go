// Package agent provides the session-based streaming agent loop that ties llm,
// tools, History, and Locker together.
//
// A Session runs the model in a loop: each turn streams one reply, runs the
// tool calls it asks for and feeds the results back, until the model replies
// without tool calls or MaximumTurns is reached. Run streams the run as Events
// on a channel; Drain turns such a channel into messages. RegisterVFSTools
// adds file tools over a vfs.Storage, RegisterPlanTool a todo list.
//
// Every run is bounded by default: MaximumTurns, RunTimeout, LLMCallTimeout,
// ToolCallTimeout and MaximumToolResultBytes. A failing, panicking or
// timed-out tool is reported to the model and the run continues. Model
// requests carry prompt-cache breakpoints unless DisablePromptCaching is set.
//
// The library never sees a scope parameter. Multi-tenant callers bind scope at
// VFS-tool registration time via vfs.ScopedStorage.Scope (ADR-013).
//
// Concurrency is bounded at the session granularity by a pluggable Locker
// (ADR-024). The package ships LocalLocker for in-process use; callers
// implement Locker themselves for any other backend (e.g. Redis).
package agent
