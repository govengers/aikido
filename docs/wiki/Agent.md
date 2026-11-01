# Agent

`agent.Session` runs a model in a loop over a tool registry: stream one reply, run the tool calls it asks for, feed the results back, repeat until the model answers without tool calls or `MaximumTurns` is reached. Tools are on [Tools](Tools.md), the files they work on on [Files](Files.md).

## Session

`agent.NewSession(&agent.SessionOptions{...})` needs `ID`, `Client`, `Model`, `History` and `Locker`; `agent.NewLocalSession` fills in an in-memory `History` and a `LocalLocker` when you leave them unset (one replica, tests). A `Session` is safe for concurrent use; runs of the same ID are serialized by the `Locker`, so building a fresh `Session` per HTTP request is fine as long as History and Locker are shared.

A production session, from [agent/example_test.go](../../agent/example_test.go) (`ExampleNewSession`):

```go
	history := historymemory.New()   // a Postgres-backed agent.History in production
	locker := agent.NewLocalLocker() // a Redis-backed agent.Locker with several replicas

	client := llmtest.NewStubClient(llmtest.TurnScript{Events: []llm.Event{
		{Kind: llm.EventTextDelta, Text: "Done."},
		{Kind: llm.EventEnd},
	}})
	session, err := agent.NewSession(&agent.SessionOptions{
		ID:                     "tenant-7/conversation-1",
		Client:                 client,
		History:                history,
		Locker:                 locker,
		Model:                  "anthropic/claude-sonnet-4.6",
		SystemPrompt:           "You keep the project notes.",
		MaximumTurns:           10,
		RunTimeout:             5 * time.Minute,
		LLMCallTimeout:         2 * time.Minute,
		ToolCallTimeout:        30 * time.Second,
		MaximumToolResultBytes: 32 << 10,
		Thinking:               llm.ThinkingByEffort(llm.ThinkingEffortLow),
	})
```

`SystemPrompt` is sent first on every model call and never written to History. `Tools` nil offers no tools (a call then fails with `tools.ErrUnknownTool` and the model sees it).

## Runs

| Method | Conversation | History |
|---|---|---|
| `Run(ctx, userText)` | History so far plus the new user message | appended at the end of a completed run |
| `RunWithOptions(ctx, userText, &agent.RunOptions{...})` | like `Run`; `SystemSuffix` is added to the system prompt for this run, `Thinking` replaces the session's | like `Run` |
| `RunWithMessages(ctx, messages)` | exactly `messages` (no system prompt in them, the session adds it) | never touched: you own the log, `agent.Drain` gives you the produced messages |

The returned error is always nil; everything arrives on the channel, which ends with `EventEnd` and then closes. Read it until it closes. From `ExampleSession_Run`:

```go
	events, _ := session.Run(context.Background(), "Say hello.")
	var reply strings.Builder
	for event := range events {
		switch event.Kind {
		case agent.EventText:
			reply.WriteString(event.Text)
		case agent.EventEnd:
			fmt.Printf("%s (%s)\n", reply.String(), event.EndReason)
		default:
		}
	}
```

`RunWithMessages` with `Drain`, from `ExampleSession_RunWithMessages`:

```go
	events, _ := session.RunWithMessages(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Content: "What time is it?"},
	})
	produced, err := agent.Drain(events)
```

`Drain` reads the channel to the end and returns the assistant messages and tool messages of the run in order, plus the first `EventError`'s error. Tool messages carry `ToolResult.ModelContent`, the capped copy the model received, so a log rebuilt with `Drain` matches what the model saw and what `Run` would store. To stream to a UI as well, fan the channel out yourself and pass one branch to `Drain`.

## Events

| Kind | Field | Meaning |
|---|---|---|
| `EventText` | `Text` | delta of the assistant reply |
| `EventThinking` | `Text` | reasoning delta; never persisted |
| `EventToolCall` | `ToolCall` | a call of the model, sent before it runs: all calls of one reply come first, in order, then their results in the same order, so an `EventToolCall` after an `EventToolResult` starts the next reply |
| `EventToolResult` | `ToolResult` | `CallID`, `Name`, `OK`, `Content` (full result) or `Error`, and `ModelContent` (the JSON the model received, capped) |
| `EventImage` | `Image` | an image of the reply |
| `EventUsage` | `Usage` | one per model call the provider reported usage for, a failed call included (before its `EventError`): tokens, `CostUSD`, `Provider`, `Latency`, `Duration`, `TokensPerSecond` |
| `EventError` | `Err` | the error that ends the run; `EventEnd` follows |
| `EventEnd` | `EndReason` | last event, exactly once |

## End reasons

`agent.EndReason` is a string type with stable values, safe to persist.

| Value | When | History |
|---|---|---|
| `EndReasonStop` (`stop`) | the model replied without tool calls | appended |
| `EndReasonMaximumTurns` (`max_turns`) | `MaximumTurns` model calls without a final reply | appended |
| `EndReasonTimeout` (`timeout`) | the caller's deadline, `RunTimeout` or `LLMCallTimeout` expired | not touched |
| `EndReasonCancelled` (`cancelled`) | the caller cancelled `ctx` | not touched |
| `EndReasonError` (`error`) | provider error, `History` or `Locker` failure, a panic in the run | not touched |

A failed run leaves History as it was, so a retry starts from a consistent transcript. `voice` adds `voice.EndReasonNoSpeech` ([Voice](Voice.md)).

## Limits

Every run is bounded by default.

| Option | Default | Notes |
|---|---|---|
| `MaximumTurns` | 20 | zero or negative means the default |
| `RunTimeout` | 10 min | whole run; negative is an error |
| `LLMCallTimeout` | 180 s | one model call; negative is an error |
| `ToolCallTimeout` | 2 min | one tool call, negative means no cap; a timed-out tool is a tool error (`tool X timed out after ...`) and the run goes on. The cap reaches the handler as its context deadline, so it binds only handlers that honour `ctx` |
| `MaximumToolResultBytes` | 64 KiB | JSON of one tool result as the model sees it and History stores it, negative means no cap; a longer result becomes `{"truncated":true,"total_bytes":N,"notice":"...","content":"<prefix>"}`. `EventToolResult` keeps the full result in `Content` and the capped one in `ModelContent` |
| `MaximumTokens` | 16384 | one reply |
| `Temperature` | model default | `*float32`, e.g. `new(float32(0.2))` |

A failing, panicking or timed-out tool is reported to the model and the run continues; only provider errors, History or Locker failures, panics outside tools, timeouts and cancellation end a run.

## Prompt caching

By default every model request marks the system prompt and its last message (when that has content) with `llm.CacheBreakpoint` (5 minute TTL), so the next call of the run reads the earlier conversation from the provider's cache. A request whose messages already carry a breakpoint is sent unchanged (a History that sets its own keeps control); breakpoints are added to the request only, never to History. Anthropic and Gemini models on OpenRouter honour them; providers that cache on their own (OpenAI, most open models) ignore them. `DisablePromptCaching: true` turns it off.

## Thinking

`SessionOptions.Thinking` sets the reasoning of every model call: `llm.ThinkingByEffort(llm.ThinkingEffortLow|Medium|High)` or `llm.ThinkingByBudget(tokens)`; nil leaves the model default. `RunOptions.Thinking` replaces it for one run. Reasoning arrives as `EventThinking` and is never stored. `llm/codex` rejects `Thinking` with `llm.ErrInvalidRequest`.

## History and Locker

`agent.History` is `Append(ctx, sessionID, messages...)` and `Read(ctx, sessionID)`. The session reads at the start of a run and appends the run's user, assistant and tool messages in one call at the end. `Read` of an unknown session returns none; implementations must be safe for concurrent use. `agent/history/memory.New()` is the in-process backend; check your own with [historytest](Testing.md#conformance-suites).

`agent.Locker` is `Lock(ctx, sessionID) (unlock func(), error)`. The session locks before `History.Read` and unlocks after `EventEnd`; a lock that cannot be acquired before `ctx` ends fails the run. `agent.LocalLocker` serializes within one process; its memory grows with the session IDs it has seen, `Forget(id)` drops one after its last run. With several replicas, implement `Locker` on Redis, etcd or Postgres advisory locks: without it, two replicas can interleave a transcript.

## Files are the authority, History is not rewindable

The loop is not transactional. A tool that wrote a file has written it, also when the run fails later and History is left unchanged; the next run sees the file but not the conversation that produced it. History is append-only: there is no API to remove or rewind messages. To make a run reversible, give the agent a [`vfs/overlay`](Files.md#overlay) and `Commit` only after a successful run (or after review); `Discard` throws its changes away. Treat the files, not the transcript, as the state of the work.

## Robustness

- A panic in a tool handler is a tool error (`tools.ErrToolPanicked`); a panic elsewhere in the run (provider client, History backend) becomes `EventError` with the stack plus `EventEnd` with `EndReasonError`, the lock is released.
- A caller that cancels `ctx` may stop reading: events that no longer fit the channel buffer are dropped, the run still releases the lock and closes the channel.

## Recipe: note then consolidate

Agents that grow a long document turn by turn work better when they take atomic notes and merge them once. On top of the existing API: register `add_note(text)` that writes `_notes/<uuid>.md`, and `consolidate_notes(target_path)` that reads the notes and the target, merges them with one `llm.Collect` call, writes the target and deletes the notes. Set `HideHiddenPaths` so the notes stay out of listings (the `_` prefix). There is no `notes` package (ADR-015).

## Multi-tenant

The library never sees a tenant. Bind it before registration: `storage.Scope(tenantID)` on a `vfs.ScopedStorage`, a session ID that contains the tenant, and a `History` keyed by that ID ([Files](Files.md#scoping)).
