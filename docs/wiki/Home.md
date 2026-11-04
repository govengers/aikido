# govengers/aikido

AI KIt DOing things: one Go library for model calls, tool calling, file-backed agent loops, images, video, speech and decision models, plus the `aikido` CLI. Module `github.com/govengers/aikido`, Go 1.26, Apache 2.0.

## Model

Three interfaces carry everything.

- **`llm.Client`** is a model provider: `Stream` (events on a channel) and `Complete` (one `llm.Response`). `llm/openrouter` and `llm/codex` implement it, `llm/llmtest.StubClient` fakes it. Further abilities are optional interfaces a caller type-asserts on: `llm.ImageGenerator`, `llm.SpeechGenerator`, `llm.VideoGenerator`, `llm.Decider`. Helpers that work on any client: `llm.Collect`, `llm.CollectWithRetry`, `llm.CompleteWithRetry`, `llm.Transcribe`.
- **`agent.Session`** runs the model in a loop: it streams one reply, dispatches the tool calls through a `tools.Registry`, feeds the results back and repeats until the model answers without tool calls. The conversation lives in an `agent.History`, runs of one session ID are serialized by an `agent.Locker`. Both are interfaces; `NewLocalSession` fills in the in-process ones.
- **`vfs.Storage`** is the file store the built-in tools work on: four methods (`ListFiles`, `ReadFile`, `WriteFile`, `DeleteFile`). Optional capabilities: `vfs.Searchable` (adds the `search` tool) and `vfs.ScopedStorage` (bind a tenant before handing the store over). Backends: memory, a directory on disk (`osfs`), any `fs.FS` (`embedfs`); composition: copy-on-write `overlay`, `mount` by path prefix.

The agent never touches the operating system on its own: no shell, no network, no host files. A directory is reachable only when you hand the agent a `vfs/osfs` root, best behind a `vfs/overlay` so you review the diff before anything is written ([Files](Files.md)). The opt-in `tools/shell` adds a `bash` tool that runs over the same `vfs.Storage`, not the host ([Shell](Shell.md)).

Constructors with settings are `New(options *Options) (*X, error)` or `NewX(options *XOptions) (*X, error)`: they validate at startup, copy the options and never write to your struct. Components without settings take none (`memory.New()`, `tools.NewRegistry()`, `agent.NewLocalLocker()`). Components never log and never read environment variables; the CLI does.

## Packages

| Import `github.com/govengers/aikido/...` | Use it for | Page |
|---|---|---|
| `llm` | `Client`, `Request`, `Message`, `Event`, `Response`, `Usage`, `Collect`, errors, capability interfaces, `Transcribe`, `Vocabulary`, decisions | [Models](Models.md), [Media](Media.md) |
| `llm/openrouter` | OpenRouter: chat, images, speech, video, decisions, model catalogue, provider routing, `APIError` | [Models](Models.md#openrouter) |
| `llm/codex` | the Codex backend of a ChatGPT subscription, device-code login, `TokenSource` | [Models](Models.md#codex-chatgpt-subscription) |
| `llm/llmtest` | `StubClient`, a scripted client for tests | [Testing](Testing.md) |
| `llm/prometheusadapter`, `llm/oteladapter`, `llm/wideeventadapter` | metrics, traces and wide-event fields of the model calls, through `llm.NewObservedClient` | [Observability](Observability.md) |
| `retry` | `Do`, `Policy`, `AfterError` | [Errors-and-Retry](Errors-and-Retry.md) |
| `tools` | `Registry`, `Handler`, `Environment`, schema helpers | [Tools](Tools.md) |
| `tools/shell` | a virtual bash over a `vfs.Storage`: `New`, `Run`, `Register`, `Builtins`, `External` for allowed host programs | [Shell](Shell.md) |
| `agent` | `Session`, events, end reasons, limits, `History`, `Locker`, `Drain`, `RegisterVFSTools`, `RegisterPlanTool` | [Agent](Agent.md), [Tools](Tools.md#built-in-file-tools) |
| `agent/history/memory` | in-process `History` | [Agent](Agent.md#history-and-locker) |
| `agent/history/historytest` | conformance suite for your own `History` | [Testing](Testing.md#conformance-suites) |
| `vfs` | `Storage`, `Searchable`, `ScopedStorage`, `ValidatePath`, errors | [Files](Files.md) |
| `vfs/memory`, `vfs/osfs`, `vfs/embedfs` | backends: in-process, a directory, an `fs.FS` | [Files](Files.md#backends) |
| `vfs/overlay` | copy-on-write over any `Storage`: `Changes`, `Commit`, `Discard`, `UnifiedDiff` | [Files](Files.md#overlay) |
| `vfs/mount` | several storages in one tree by path prefix | [Files](Files.md#mount) |
| `vfs/vfstest` | conformance suite for your own `Storage` | [Files](Files.md#writing-your-own-backend) |
| `voice` | speech in and out around one agent turn, NDJSON `net/http` handler | [Voice](Voice.md) |
| `cmd/aikido` | the CLI | [CLI](CLI.md) |

## Which package for which need

- One answer from a model: `llm/openrouter` + `llm.Collect`. Example: [examples/chat-oneshot](../../examples/chat-oneshot/main.go).
- An agent that writes notes or documents in your application: `agent` + `vfs/memory` (tests) or your own `vfs.Storage` (production). Example: [examples/agent-vfs](../../examples/agent-vfs/main.go).
- A chatbot over a fixed corpus: `vfs/embedfs` + `RegisterVFSTools` with `ReadOnly: true`. Example: [examples/chatbot](../../examples/chatbot/main.go).
- An agent on a real directory, reviewed before writing: `vfs/osfs` under `vfs/overlay`. Example: [examples/agent-overlay](../../examples/agent-overlay/main.go).
- Reference documents next to a writable workspace: `vfs/mount` with `ReadOnly` mounts.
- Images, video, speech, transcription, decision models: [Media](Media.md). Example: [examples/image-generation](../../examples/image-generation/main.go).
- An agent that works with shell commands (grep, sed, find, xargs, loops) on its files, or runs a few allowed host programs: `tools/shell` ([Shell](Shell.md)).
- Voice in and out of an agent turn over HTTP: `voice`. Example: [examples/voicebot](../../examples/voicebot/main.go).
- A ChatGPT subscription instead of an API key: `llm/codex` (no tools).
- More than one replica: your own `agent.History` (database) and `agent.Locker` (Redis or advisory locks), checked with `historytest`.
- Tests without network: `llm/llmtest.StubClient` ([Testing](Testing.md)).
- From the shell: the [CLI](CLI.md).

## Wiring order (agent)

1. Client: `openrouter.NewClient(&openrouter.Options{APIKey: ...})`.
2. Storage: a backend, wrapped in `overlay.New` if the run must not write directly; `Scope(tenant)` first on a multi-tenant store.
3. Tools: `tools.NewRegistry()`, `agent.RegisterVFSTools(registry, &agent.VFSToolOptions{Storage: ...})`, optionally `agent.RegisterPlanTool(registry)`, `shell.Register(registry, &shell.Options{Storage: ...})` and your own `registry.Register`.
4. Session: `agent.NewSession` with `History` and `Locker` shared by the whole process (or `agent.NewLocalSession` for one replica), one `Session` per conversation ID.
5. `session.Run(ctx, text)` and read the channel until it closes; `EventEnd` carries the `EndReason`.
6. With an overlay: `Changes`, `UnifiedDiff`, then `Commit` or `Discard`.

## Pages

- [Agent](Agent.md): `Session`, runs, events, end reasons, limits, prompt caching, thinking, `History`, `Locker`, `Drain`.
- [Tools](Tools.md): `Registry`, schemas, handlers, argument checks, panics, the built-in file tools and `todo_write`.
- [Shell](Shell.md): the virtual bash tool, its options, built-in commands and flags, `External`, filesystem semantics, security.
- [Files](Files.md): the `Storage` contract, backends, overlay, mount, search and scoping, your own backend.
- [Models](Models.md): requests, responses, streaming, OpenRouter (routing, catalogue, `APIError`), Codex.
- [Media](Media.md): images, video, speech, transcription and vocabulary, decision models.
- [Voice](Voice.md): `voice.Pipeline`, events, usage, the NDJSON handler.
- [Observability](Observability.md): `llm.NewObservedClient`, `llm.Observer`, Prometheus metrics, OpenTelemetry spans, wide-event fields.
- [Errors-and-Retry](Errors-and-Retry.md): sentinels, `APIError`, retry semantics per call path, `retry.Do`.
- [Testing](Testing.md): `StubClient`, conformance suites, testing an agent offline.
- [CLI](CLI.md): every command and flag.
- [Security](Security.md): guarantees and known limits.
- [Gotchas](Gotchas.md): the rules that are easy to get wrong.

Reference: [README](../../README.md) (overview), [decisions](../DECISIONS.md) (ADRs), [migration](../migration.md) (from `github.com/mxcd/aikido` v0.x), [pkg.go.dev](https://pkg.go.dev/github.com/govengers/aikido).
