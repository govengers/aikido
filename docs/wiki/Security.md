# Security

aikido runs model output, and model output is untrusted input: tool arguments can be malformed, can try path traversal, can name tools that do not exist, and a run can loop. This page lists what the library guarantees and what it leaves to you. One-line rules: [Gotchas](Gotchas.md).

## Guarantees

- **No operating system access by default.** The library starts no processes, opens no sockets other than the provider HTTP clients (`llm/openrouter`, `llm/codex`), reads no environment variables and never logs. The only package that touches the disk is `vfs/osfs`, and only under the directory you pass. The built-in tools have no shell, no network fetch, no code evaluation.
- **Tools see one store.** The file tools work only on the `vfs.Storage` given to `RegisterVFSTools` and capture it by closure; no argument selects another store or tenant. Multi-tenant callers bind the tenant with `ScopedStorage.Scope` before registration (ADR-013).
- **Path validation.** Every path argument passes `vfs.ValidatePath` (no empty path, no leading or trailing slash, no `//`, no `.` or `..` segment, no backslash, no NUL, at most 512 bytes) and the caller policy (`AllowedExtensions`, `HideHiddenPaths`) before the storage is called; refused paths are `vfs.ErrPathInvalid`. The bundled backends validate again on entry.
- **Directory confinement.** `vfs/osfs` resolves every path through `os.Root`: neither `..` nor a symlink reaches outside the directory. Writes go to an owner-only temp file and are renamed over the target, so a reader never sees half a file and a replaced file keeps its permission bits (a `0600` file stays private). Only regular files are listed or read; `.git` is skipped.
- **Dry runs.** `vfs/overlay` never writes its base before `Commit`; `Changes` and `UnifiedDiff` show what would happen, `Discard` drops it. `vfs/mount` `ReadOnly` mounts refuse writes whatever the storage supports.
- **Bounded runs.** `MaximumTurns` (20), `RunTimeout` (10 min), `LLMCallTimeout` (180 s), `ToolCallTimeout` (2 min), `MaximumToolResultBytes` (64 KiB to the model), `MaximumTokens` (16384) and the tools' own caps: `MaximumFileBytes` (1 MiB written), `read_file` 2000 lines or 40 KiB, `grep` 200 entries and a 32 MiB scan, `glob` 500 paths. The voice handler caps uploads at 8 MiB and turns at 5 minutes.
- **Containment.** A failing, panicking or timed-out tool is a tool error the model sees; a panic anywhere else in a run, in a voice turn or in a provider's stream goroutine becomes an error event, never a crashed process. A cancelled caller cannot leave a run goroutine or a session lock behind.
- **Consistent History.** A run that fails leaves History unchanged; runs of one session ID are serialized by the `Locker`.
- **Secrets.** API keys come only from `Options` and travel only as the `Authorization` header to the configured `BaseURL`. `llm/codex` writes tokens nowhere: every rotated login goes to your `OnRefresh`. Errors carry status, provider code and at most 256 bytes of the provider's message (`APIError.Message`), never the key.

## Known limits

- **Your tools are yours.** A handler registered with `tools.Registry` can do anything Go can; review it like any code that runs model-chosen input. `Dispatch` checks only the top-level `required` fields.
- **`ToolCallTimeout` binds only handlers that honour `ctx`.** A handler that ignores its context blocks the run until it returns.
- **No transactions.** The loop is not transactional and `Commit` is not atomic across files: it stops at the first failure with the earlier paths applied. `move_file` writes, then deletes, and can leave both files.
- **Overlapping mounts alias.** `vfs/mount` routes by the path string after `vfs.ValidatePath`; the storages resolve the files. A writable root over a directory and a read-only mount over its subdirectory are not separate: on a case-insensitive filesystem (macOS, Windows) `Docs/a.md` reaches `docs/a.md` through the root, and so does a symlink in the root. Mount disjoint directories.
- **Prompt injection.** Files the agent reads are model input; a document can instruct the model. The tools limit the reach to the store you gave it; review changes (overlay) before they reach anything that matters.
- **Single process locking.** `agent.LocalLocker` serializes within one process only. Several replicas need your own `Locker`; without one, two replicas can interleave a transcript and race on files.
- **No spending cap.** A run can cost up to `MaximumTurns` model calls; there is no budget or rate limit per session or tenant. Put one in front if users trigger runs.
- **Auth is yours.** `voice.Handler` authenticates nobody; check in a middleware and resolve the tenant in `HandlerOptions.Session`.
- **Output is not escaped.** Model text goes to your users as is; escape it where you render it.
- **Content filters are detected heuristically.** A provider that drops a stream without a signal looks like `ErrServerError`.
- **Codex logins** are single-use refresh tokens: one `TokenSource` per login and process, and `ParseLogin` shares the login with the Codex CLI.

Reporting: open a private security advisory on the GitHub repository.
