# Testing

Everything in aikido can be tested without network or API key: a scripted model client, conformance suites for your backends, and an end-to-end suite against a fake OpenRouter.

## llmtest.StubClient

`llmtest.NewStubClient(turns...)` is an `llm.Client` that plays one `llmtest.TurnScript` per `Stream` or `Complete` call and records every request. From [llm/llmtest/example_test.go](../../llm/llmtest/example_test.go):

```go
	stub := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "call-1", Name: "weather", Arguments: `{"city":"Stuttgart"}`}},
			{Kind: llm.EventEnd, FinishReason: "tool_calls"},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "It is sunny in Stuttgart."},
			{Kind: llm.EventEnd, FinishReason: "stop"},
		}},
	)
```

- The stub adds no `EventEnd`: script it.
- A call after the last script fails with `llmtest.ErrStubExhausted` and is not recorded; `Requests()` returns what the code under test sent (model, messages, tools, cache breakpoints).
- `TurnScript.Block` holds a turn until the channel is closed or `ctx` ends, for timeout and cancellation tests.
- The stub also implements the optional capabilities, each with its own scripts and records: `ScriptImages`/`ImageRequests`, `ScriptSpeech`/`SpeechRequests`, `ScriptVideos`/`VideoRequests`, `ScriptDecisions`/`DecisionRequests`.

## Testing an agent offline

Script the model's turns, register your real tools over `vfs/memory`, run the session and assert on events, the store and History. From [agent/example_test.go](../../agent/example_test.go) (`ExampleSession_RunWithMessages`):

```go
	client := llmtest.NewStubClient(
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventToolCall, Tool: &llm.ToolCall{ID: "call-1", Name: "clock", Arguments: "{}"}},
			{Kind: llm.EventEnd},
		}},
		llmtest.TurnScript{Events: []llm.Event{
			{Kind: llm.EventTextDelta, Text: "It is noon."},
			{Kind: llm.EventEnd},
		}},
	)
	session, err := agent.NewLocalSession(&agent.SessionOptions{
		ID: "conversation-2", Client: client, Model: "openai/gpt-5.4", Tools: registry,
	})
```

Useful assertions: the tool calls and their results (`ToolResult.OK` and `Content` of `EventToolResult`), the final files (`storage.ListFiles`), what History holds after `Run`, and `client.Requests()` for the prompt and tool definitions the model got. A tool handler alone is tested through `registry.Dispatch` ([Tools](Tools.md#what-dispatch-checks)).

## Conformance suites

Your own backends run the shared suites in their tests:

- `vfstest.RunConformance(t, func() vfs.Storage { ... })` for a `vfs.Storage`; the search sub-tests run when the store is `Searchable` ([Files](Files.md#writing-your-own-backend)).
- `historytest.RunConformance(t, func() agent.History { ... })` for an `agent.History`, as [agent/history/memory/history_test.go](../../agent/history/memory/history_test.go) does:

```go
	historytest.RunConformance(t, func() agent.History { return memory.New() })
```

The factory must return a new, empty instance per call. There is no suite for `agent.Locker`: test that two runs of the same session ID on different `Session` values serialize when they share your Locker, and that different IDs run in parallel.

## OpenRouter without network

`openrouter.Options.BaseURL` points the client at an `httptest.Server`; the examples in [llm/openrouter/example_test.go](../../llm/openrouter/example_test.go) show the minimal SSE and JSON answers. The CLI takes the same root from `OPENROUTER_BASE_URL`.

## This repository

- `just test`: unit tests with `-race` and the 80 % coverage gate.
- `just test-e2e`: [test/e2e](../../test/e2e) runs the built `aikido` binary and the public library API against a fake OpenRouter that speaks the real wire format (build tag `e2e`); skipped when suite and inputs equal a green run.
- `just smoke`: the `smoke`-tagged tests against the live API; needs `OPENROUTER_API_KEY` and spends money.
- Every package's `example_test.go` holds runnable examples; the snippets in this wiki are taken from them.
