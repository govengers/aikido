# Gotchas

The rules agents and humans get wrong most often, one line each with the page that explains it.

## Agent

- Read a run's channel until it closes; `Run` always returns a nil error, failures arrive as `EventError` plus `EventEnd`. [Agent](Agent.md#runs)
- Share one `History` and one `Locker` across all `Session` values of the process; a `Locker` per session serializes nothing. [Agent](Agent.md#history-and-locker)
- `NewLocalSession` keeps History in memory: a restart loses every conversation. [Agent](Agent.md#session)
- History changes only on `stop` and `max_turns`; after `error`, `timeout` or `cancelled` it is as before the run, but files the tools wrote stay written. [Agent](Agent.md#files-are-the-authority-history-is-not-rewindable)
- There is no way to rewind History; make runs reversible with an overlay and `Commit` after success. [Files](Files.md#overlay)
- `RunWithMessages` never touches History; persist `Drain`'s messages yourself. [Agent](Agent.md#runs)
- On `EventToolResult`, `ToolResult.Content` is the full result, `ModelContent` the capped JSON the model saw; store `ModelContent` (as `Drain` does) to replay what the model saw. [Agent](Agent.md#limits)
- All `EventToolCall`s of one model reply arrive before the first tool runs, then the `EventToolResult`s follow in the same order; pair them by `ToolResult.CallID`, not by position. [Agent](Agent.md#events)
- Negative `RunTimeout` or `LLMCallTimeout` is an error; negative `ToolCallTimeout` or `MaximumToolResultBytes` disables the cap. [Agent](Agent.md#limits)
- Prompt caching is on by default and adds `Cache` to the request, never to History; a message that already has a breakpoint switches the automatic ones off. [Agent](Agent.md#prompt-caching)
- `llm/codex` cannot run tools: an agent session over a Codex client fails as soon as tools are offered. [Models](Models.md#codex-chatgpt-subscription)

## Tools

- The default tool set has ten names; a host that guards tool names must know `grep`, `glob`, `edit_file`, `multi_edit`, `move_file`, or set `DisableExtended`. [Tools](Tools.md#names-your-code-may-guard)
- `search` exists only for a `vfs.Searchable` store; `todo_write` only after `RegisterPlanTool`. [Tools](Tools.md)
- `read_file` prefixes lines with their number and a tab; `edit_file` wants `old_string` without that prefix. [Tools](Tools.md#built-in-file-tools)
- `AllowedExtensions` also refuses reads of other files, not only writes. [Tools](Tools.md#built-in-file-tools)
- `Dispatch` checks only required top-level fields; parse and validate the rest in the handler. [Tools](Tools.md#what-dispatch-checks)
- A handler error is shown to the model: write it as an instruction, and never put secrets in it. [Tools](Tools.md#registry-and-handlers)
- Handlers that ignore `ctx` are not stopped by `ToolCallTimeout`. [Security](Security.md#known-limits)

## Files

- `vfs/osfs` needs `Close`; `osfs.New` fails for a directory that does not exist. [Files](Files.md#backends)
- `osfs` does not store content types; they come from the extension. [Files](Files.md#backends)
- An overlay over a read-only base fails at `Commit`, not at `WriteFile`. [Files](Files.md#overlay)
- `Commit` stops at the first failing path; earlier paths are already written, the rest stays pending. [Files](Files.md#overlay)
- `mount.New` returns `vfs.Storage`, not a concrete type, so `Searchable` is present only when a mount can search. [Files](Files.md#mount)
- A path equal to a mount prefix (`docs`) is invalid; files live below it (`docs/a.md`). [Files](Files.md#mount)
- `embedfs` over `//go:embed dir/*` needs `fs.Sub(corpus, "dir")`, or every path starts with `dir/`. [Files](Files.md#backends)

## Models and media

- Use `Complete`, not `Stream` or `Collect`, for image output: a base64 image exceeds the SSE line cap. [Media](Media.md#images)
- `openai/gpt-image-2.5-flare` and other images-endpoint models answer only on `GenerateImage`, and are missing from `Models`. [Media](Media.md#images)
- Set `ImageRequest.Size` or `AspectRatio`+`ImageSize`, never both. [Media](Media.md#images)
- Video and decision models take only the values their lists name (`VideoModels`, `DecisionModels`); everything else is rejected, nothing is snapped. [Media](Media.md#video)
- `SpeechResponse` has no usage: price speech per character. [Media](Media.md#speech)
- `WithProvider` replaces the client's provider preferences, it does not merge them. [Models](Models.md#provider-routing)
- `Stream` retries only before the first byte; use `CollectWithRetry` to restart a whole stream, and pay for every attempt. [Errors-and-Retry](Errors-and-Retry.md#retry-per-call-path)
- Match errors with `errors.Is`/`errors.As`, never by text. [Errors-and-Retry](Errors-and-Retry.md)
- One Codex `TokenSource` per login and process; a reused refresh token revokes the login. [Models](Models.md#codex-chatgpt-subscription)
- Keep example sentences out of `TranscribeRequest.Hint`; a silent clip repeats them. [Media](Media.md#transcription-and-vocabulary)

## CLI

- `aikido agent --dir` is a dry run; only `--apply` writes, and only when the run ended with `stop`: after an error, timeout, cancel or the turn cap it prints the changes, writes nothing and exits non-zero. [CLI](CLI.md#agent)
- An explicit `-m` image model is never switched for a frame it cannot render; the CLI errors instead. [CLI](CLI.md#image)
