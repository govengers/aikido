# Migrating to govengers/aikido v1

`github.com/govengers/aikido` v1.0.0 is the successor of `github.com/mxcd/aikido` (last release v0.12.0). The packages are the same; the exported API is spelled out (`Max*` becomes `Maximum*`, `Def` becomes `Definition`, ...), components are built from options, and the agent harness got limits, argument checks and new default tools. There are no compatibility aliases; the old module is not touched.

Most of the work is mechanical: change the import path, then follow the compiler with the rename table. Read [Behaviour changes](#behaviour-changes) afterwards: those compile unchanged and act differently.

## Module

| Before | Now |
|---|---|
| `github.com/mxcd/aikido` v0.x | `github.com/govengers/aikido` v1 (`go get github.com/govengers/aikido@v1`) |
| `go install github.com/mxcd/aikido/cmd/aikido@...` | `go install github.com/govengers/aikido/cmd/aikido@latest` |
| Go 1.25 | Go 1.26 |
| MIT | Apache 2.0 |

```bash
grep -rl 'github.com/mxcd/aikido' --include='*.go' . | xargs sed -i '' 's|github.com/mxcd/aikido|github.com/govengers/aikido|g'   # GNU sed: -i without ''
go get github.com/govengers/aikido@v1 && go mod tidy
```

## Renames

| Package | Before | Now |
|---|---|---|
| `llm` | `ToolDef` | `ToolDefinition` |
| `llm` | `ThinkingConfig` | `ThinkingConfiguration` (`ThinkingByEffort`, `ThinkingByBudget` keep their names) |
| `llm` | `ImageConfig`, `Request.ImageConfig` | `ImageConfiguration`, `Request.ImageConfiguration` |
| `llm` | `Request.MaxTokens` | `Request.MaximumTokens` |
| `llm` | `ErrAuth` | `ErrAuthentication` |
| `llm` | `CacheTTL5Min`, `CacheTTL1Hour` | `CacheTTLFiveMinutes`, `CacheTTLOneHour` |
| `llm` | `MaxVocabularyPromptTerms`, `MaxVocabularyPromptBytes` | `MaximumVocabularyPromptTerms`, `MaximumVocabularyPromptBytes` |
| `llm` | `VideoRequest.Duration` | `VideoRequest.DurationSeconds` |
| `llm/openrouter` | `ProviderPrefs`, `Options.Provider *ProviderPrefs` | `ProviderPreferences` |
| `llm/openrouter` | `MaxPrice`, `ProviderPrefs.MaxPrice` | `MaximumPrice`, `ProviderPreferences.MaximumPrice` |
| `llm/openrouter` | `Options.MaxAttempts` | `Options.MaximumAttempts` |
| `llm/openrouter` | `Endpoint.MaxCompletionTokens`, `MaxPromptTokens`, `TopProvider.MaxCompletionTokens` | `MaximumCompletionTokens`, `MaximumPromptTokens` (JSON tags unchanged) |
| `llm/openrouter` | `Endpoint.UptimeLast30m`, `LatencyLast30m`, `ThroughputLast30m` | `UptimeLast30Minutes`, `LatencyLast30Minutes`, `ThroughputLast30Minutes` |
| `llm/codex` | `AuthOptions` | `LoginOptions` |
| `llm/codex` | `ParseAuthJSON` | `ParseLogin` |
| `llm/codex` | `ErrReauthRequired` | `ErrReauthenticationRequired` |
| `llm/codex` | `DeviceCode.DeviceAuthID` | `DeviceCode.DeviceAuthorizationID` |
| `retry` | `RetryAfterError` | `AfterError` |
| `retry` | `Policy.MaxAttempts`, `Policy.MaxDelay` | `Policy.MaximumAttempts`, `Policy.MaximumDelay` |
| `tools` | `Env` | `Environment` (`Handler` is `func(ctx, arguments json.RawMessage, environment tools.Environment) (tools.Result, error)`) |
| `tools` | `Registry.Defs()` | `Registry.Definitions()` |
| `vfs` | `FileMeta` | `FileMetadata` |
| `vfs` | `MaxPathBytes` | `MaximumPathBytes` |
| `vfs` | `vfs.RunConformance` | `vfs/vfstest.RunConformance` |
| `agent` | `SessionOptions.MaxTurns`, `SessionOptions.MaxTokens` | `MaximumTurns`, `MaximumTokens` |
| `agent` | `VFSToolOptions.MaxFileBytes` | `VFSToolOptions.MaximumFileBytes` |
| `agent` | `EndReasonMaxTurns` | `EndReasonMaximumTurns` (value still `"max_turns"`) |
| `agent/history` | `history.RunConformance` | `agent/history/historytest.RunConformance` |
| `voice` | `HandlerOptions.MaxAudioBytes` | `HandlerOptions.MaximumAudioBytes` |
| `voice` | `Options.SpeechUSDPerChar` | `Options.SpeechUSDPerCharacter` |
| `voice` | `SpeechChunk.Seq` | `SpeechChunk.Sequence` (NDJSON field still `seq`) |
| `voice` | `Usage.SpeechChars` | `Usage.SpeechCharacters` |

Wire formats did not change: JSON tags of the OpenRouter catalogue types, the NDJSON lines of `voice.Handler` and the `EndReason` strings are the same.

## Constructors and signatures

| Before | Now |
|---|---|
| `memory.NewStorage()` (`vfs/memory`) | `memory.New()` |
| `memory.NewHistory()` (`agent/history/memory`) | `memory.New()` |
| `embedfs.NewStorage(fsys) *Storage` | `embedfs.New(&embedfs.Options{FileSystem: fsys}) (*Storage, error)`; nil options or file system is an error |
| `osfs.New(dir)` | `osfs.New(&osfs.Options{Directory: dir})` |
| `codex.NewTokenSource(tokens, onRefresh, authOptions) *TokenSource` | `codex.NewTokenSource(&codex.TokenSourceOptions{Tokens: tokens, OnRefresh: onRefresh, Login: loginOptions}) (*TokenSource, error)`; a login without `RefreshToken` is refused |
| `codex.StartDeviceLogin(ctx, *AuthOptions)`, `PollDeviceLogin(ctx, *AuthOptions, dc)` | the same with `*codex.LoginOptions` |
| `llm.Collect(ctx, c, req) (text, calls, images, usage, err)` | `llm.Collect(ctx, client, request) (llm.Response, error)`: `response.Text`, `ToolCalls`, `Images`, `Usage`, plus `FinishReason` |
| `llm.CollectWithRetry(...)` with the same five results | `(llm.Response, error)` |
| `llm.Float32(0.7)` | `new(float32(0.7))` (Go 1.26) |
| `agent.EndReason*` untyped string constants, `Event.EndReason string` | `type agent.EndReason string`; `Event.EndReason agent.EndReason`, `voice.Event.EndReason agent.EndReason`, `voice.EndReasonNoSpeech agent.EndReason`. Compare with the constants, or convert with `string(...)` |

## Removed

| Removed | Instead |
|---|---|
| `tools.Result.Display` | nothing: the agent never surfaced it. Show your own text from `EventToolResult` |
| `agent.SessionOptions.Logger` | nothing: the session never wrote to it. Log from the events |
| `llm.Float32` | `new(float32(v))` |
| `docs/v1/API.md`, `docs/ROADMAP.md`, `docs/ARCHITECTURE.md` and the other planning documents | [docs/wiki](wiki/Home.md); decisions stay in [DECISIONS.md](DECISIONS.md) |

## New API

Additive; nothing to migrate, listed so you know it exists.

- `vfs/overlay` (copy-on-write over any store: `Changes`, `Commit`, `Discard`, `UnifiedDiff`) and `vfs/mount` (several stores by path prefix). [Files](wiki/Files.md)
- `agent.SessionOptions.ToolCallTimeout`, `MaximumToolResultBytes`, `DisablePromptCaching`; `agent.ToolResult.ModelContent`. [Agent](wiki/Agent.md)
- `agent.RegisterPlanTool` (`todo_write`). [Tools](wiki/Tools.md#plan-tool)
- `tools.ErrInvalidArguments`, `tools.ErrToolPanicked`. [Tools](wiki/Tools.md#what-dispatch-checks)
- `openrouter.APIError`, `llm.ErrInvalidResponse`. [Errors-and-Retry](wiki/Errors-and-Retry.md)
- CLI: `aikido agent --dir`, `--apply`, `--mount`; `OPENROUTER_BASE_URL`. [CLI](wiki/CLI.md#agent)

## Behaviour changes

| Area | Before (v0.12) | Now (v1) |
|---|---|---|
| Default file tools | `read_file`, `list_files`, `write_file`, `delete_file`, `grep`, `glob`, `edit_file`, `search` | adds `multi_edit` and `move_file` (both skipped by `ReadOnly` and `DisableExtended`). A host that allow-lists tool names must add them |
| `read_file` output | the lines as they are | every line prefixed with its number (right-aligned to the widest one) and a tab, at most 2000 lines or 40 KiB per call (at least one line). Prompts or parsers that read `content` verbatim must strip the prefix; `edit_file` still wants text without it |
| Tool timeout | none | `ToolCallTimeout` 2 min by default; a timed-out tool is a tool error and the run goes on. Negative disables it |
| Tool result size | sent to the model in full | `MaximumToolResultBytes` 64 KiB by default; longer results become a truncation notice with a prefix (also in History). `EventToolResult.Content` stays complete. Negative disables it |
| Prompt caching | only the breakpoints you set | the agent marks the system prompt and the last message of every request (5 min TTL) unless a message already carries one; `DisablePromptCaching: true` restores the old behaviour. Anthropic and Gemini bill cache writes and reads differently, so costs per run change (usually down) |
| Tool arguments | handlers received anything | `Dispatch` refuses arguments that are not a JSON object or lack a `required` field (`tools.ErrInvalidArguments`, empty arguments count as `{}`); the model sees the error. Built-in tools report wrong argument types the same way |
| Tool panics | crashed the process | `Dispatch` recovers them as `tools.ErrToolPanicked`, a tool error the model sees; panics elsewhere in a run end it with `EventError` and `EndReasonError` |
| Abandoned runs | a caller that cancelled and stopped reading left the run blocked, holding the session lock | events that do not fit the buffer are dropped once `ctx` is done; the lock is released |
| Cancellation | a cancelled request could surface as `llm.ErrServerError` (and be retried) | `context.Canceled` / `context.DeadlineExceeded`, in `openrouter` and `codex`, on `Stream` and `Complete` |
| Provider errors | sentinel-wrapped text | `*openrouter.APIError` with `StatusCode`, `Code`, `Message`; `errors.Is` on the sentinels and the `Error()` text are unchanged |
| Undecodable answers | a 2xx whose body did not decode was `llm.ErrServerError` and retried | `llm.ErrInvalidResponse`, not transient, not retried (`Complete`, images, decisions, models, video) |
| Provider preferences | `NewClient` and `WithProvider` kept your `*ProviderPreferences`; changing it later changed routing | both keep a copy; build a new client or context to change routing |
| OpenRouter stream end | a body that ended without `[DONE]` or a finish reason was a success with what had arrived; tool calls of a stream without a finish reason were dropped | a cut-off body is `EventError` (`llm.ErrServerError`); `[DONE]` without a finish reason still delivers the tool calls |
| OpenRouter tool calls | complete calls without an index (`Complete`) or several at index 0 with their own ids (`Stream`) were merged into one call with broken arguments | one call each; a fragment with a new id starts a new call |
| OpenRouter answer size | every body read in full, however large | non-streaming answers up to 64 MiB, video clips up to 512 MiB, else `llm.ErrInvalidResponse`; error bodies read up to 64 KiB; a stream with more than 128 tool calls or 16 MiB of tool-call arguments ends with `EventError` ([Models](wiki/Models.md#openrouter)) |
| OpenRouter HTTP client | no timeout at all | the default client waits at most 10 minutes for response headers (no overall timeout); a custom `HTTPClient` is used as is |
| Stream panics | a decoding panic crashed the process | `EventError` followed by `EventEnd` |
| `retry.Do` | could retry context errors; `MaxDelay` 0 meant no cap; `Multiplier` 0 meant no growth | never retries `context.Canceled`/`DeadlineExceeded`; skips a wait that would end after the deadline and returns the last error; `MaximumDelay` 0 means 30 s; `Multiplier` below 1 means 2 |
| `vfs.ValidatePath` | allowed `.` segments and backslashes (`./a.md` and `a.md` were two keys in memory but one file in osfs) | both are `vfs.ErrPathInvalid` |
| `vfs/embedfs` content type | unknown extensions were `text/plain` | `application/octet-stream`, like `osfs` |
| `voice.Handler` | a broken multipart upload was taken as a text turn | 400 |

## Coming from v0.3.x (ASO Nexus Hub)

The Hub pins v0.3.2. Everything above applies, and between v0.3.2 and v0.12.0 the default tool set grew from `read_file`, `write_file`, `list_files`, `delete_file`, `search` to the ten names listed in [Tools](wiki/Tools.md#built-in-file-tools), and `read_file` gained `offset`/`limit` ranges. What the Hub touches:

| Hub uses | Change to |
|---|---|
| `vfs.FileMeta` | `vfs.FileMetadata` (in every `Storage` implementation: `ListFiles` and `ReadFile` signatures) |
| `llm.ToolDef` | `llm.ToolDefinition` |
| `tools.Env` | `tools.Environment` |
| `llm.ThinkingConfig` | `llm.ThinkingConfiguration`; per session now also `SessionOptions.Thinking`, per run `RunOptions.Thinking` |
| `llm.ImageConfig` | `llm.ImageConfiguration`, field `Request.ImageConfiguration` |
| `MaxTokens`, `MaxTurns`, `MaxFileBytes`, `MaxAttempts`, ... | `Maximum*` |
| `embedfs.NewStorage(fsys)` | `embedfs.New(&embedfs.Options{FileSystem: fsys})`, handle the error |
| `tools.Result.Display` | remove; show text from `EventToolResult` |
| a tool-name guard on the built-in set | add `grep`, `glob`, `edit_file`, `multi_edit`, `move_file`, or set `VFSToolOptions.DisableExtended: true` to keep the v0.3 set |
| prompts or tests that read `read_file` content | expect line-number prefixes |

Check the Hub's tests for tool results over 64 KiB and tool handlers that run longer than 2 minutes; set `MaximumToolResultBytes` or `ToolCallTimeout` (negative disables) where the old behaviour is wanted.
