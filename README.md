# aikido

[![CI](https://github.com/govengers/aikido/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/govengers/aikido/actions/workflows/ci.yml)
[![unit tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/aikido/badges/unit-tests.json)](https://github.com/govengers/aikido/actions/workflows/ci.yml)
[![e2e tests](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/aikido/badges/e2e-tests.json)](test/e2e)
[![coverage](https://img.shields.io/endpoint?url=https://raw.githubusercontent.com/govengers/aikido/badges/coverage.json)](https://github.com/govengers/aikido/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/govengers/aikido.svg)](https://pkg.go.dev/github.com/govengers/aikido)
[![License](https://img.shields.io/github/license/govengers/aikido)](LICENSE)

Coding agents: start at [llms.txt](llms.txt) and [docs/wiki/Home.md](docs/wiki/Home.md); runnable programs are in [examples/](examples/).

## What it is

**AI KIt DOing things.** One Go library for model calls and agents: a provider-agnostic `llm.Client` (OpenRouter, a ChatGPT subscription through Codex, a scripted stub for tests), a tool registry, an agent loop with pluggable history and locking, and a small file-store interface the agent's tools work on, from memory to a real directory behind a reviewable copy-on-write overlay. Images, video, speech, transcription and decision models are optional capabilities of the same client. The `aikido` CLI puts it on the shell. It moved from `github.com/mxcd/aikido` ([migration guide](docs/migration.md)).

## Features

- `llm.Client` with streaming events and `Complete`, tool calls, images in and out, audio input, structured output, prompt-cache breakpoints, thinking effort, and per-call usage with provider-billed cost, latency and throughput.
- OpenRouter: provider routing per client or per call, model catalogue with per-provider latency and uptime, typed `APIError`, retries at request start honouring `Retry-After`.
- Codex backend of a ChatGPT subscription: device-code login and a rotating `TokenSource`.
- Agent loop: `Session` over `History` and `Locker` interfaces, bounded by turns, run, model-call and tool-call timeouts and a tool-result cap, prompt caching on by default, panics contained.
- Built-in file tools over any `vfs.Storage`: `read_file` (numbered lines), `write_file`, `edit_file`, `multi_edit`, `move_file`, `delete_file`, `list_files`, `grep`, `glob`, `search`, plus an optional `todo_write` plan tool.
- File stores: memory, a directory confined by `os.Root`, any `fs.FS`; a copy-on-write overlay with change list and unified diff, mounts by path prefix; conformance suites for your own backends.
- Images (chat and images endpoint), video jobs, text-to-speech, transcription with a correcting vocabulary, decision models; a `voice` package with speech in and out around an agent turn and an NDJSON `net/http` handler.
- `tools/shell`, an opt-in virtual bash over the same file stores: pipes, loops, redirects and GNU-compatible commands (grep, sed, find, xargs, jq, awk, ...) checked against GNU in CI, with no host access unless the caller allows a program (git, kubectl).
- Observability without logging: `llm.NewObservedClient` reports every model call to observers; adapters export Prometheus metrics (requests, duration, tokens, cost), OpenTelemetry GenAI spans and summed `llm.*` wide-event fields.
- No host shell, no network tools, no logging, no environment reads in the library.

## Install

```bash
go get github.com/govengers/aikido@v1
go install github.com/govengers/aikido/cmd/aikido@latest   # the CLI
```

## Quick start

An agent over an in-memory workspace, from [examples/agent-vfs/main.go](examples/agent-vfs/main.go):

```go
	client, err := openrouter.NewClient(&openrouter.Options{
		APIKey:      apiKey,
		HTTPReferer: "https://github.com/govengers/aikido",
		XTitle:      "aikido agent-vfs example",
	})
	if err != nil {
		return err
	}

	storage := vfsmem.New()
	session, err := newSession(client, storage)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	events, err := session.Run(ctx, prompt)
	if err != nil {
		return err
	}
	for event := range events {
		printEvent(event)
	}
```

```go
func newSession(client llm.Client, storage vfs.Storage) (*agent.Session, error) {
	registry := tools.NewRegistry()
	if err := agent.RegisterVFSTools(registry, &agent.VFSToolOptions{
		Storage:         storage,
		HideHiddenPaths: true,
	}); err != nil {
		return nil, err
	}
	return agent.NewLocalSession(&agent.SessionOptions{
		ID:     "demo",
		Client: client,
		Tools:  registry,
		Model:  "anthropic/claude-sonnet-4.6",
		SystemPrompt: "You are a research assistant working in a virtual file system. " +
			"You can read, write, list, delete, and search files using your tools. " +
			"Files end in .md and use markdown formatting.",
	})
}
```

One completion, from [examples/chat-oneshot/main.go](examples/chat-oneshot/main.go):

```go
	response, err := llm.Collect(ctx, client, llm.Request{
		Model: "anthropic/claude-haiku-4.5",
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Content: "Be concise. Reply in one sentence."},
			{Role: llm.RoleUser, Content: "Give me one fun fact about the Go programming language."},
		},
		MaximumTokens: 200,
		Temperature:   new(float32(0.7)),
	})
```

## Packages

| Package | Purpose |
|---|---|
| [`llm`](docs/wiki/Models.md) | `Client`, `Request`, `Event`, `Response`, `Collect`, errors, capability interfaces, transcription, decisions |
| [`llm/openrouter`](docs/wiki/Models.md#openrouter) | OpenRouter: chat, images, speech, video, decisions, catalogue, routing |
| [`llm/codex`](docs/wiki/Models.md#codex-chatgpt-subscription) | ChatGPT subscription through the Codex backend |
| [`llm/llmtest`](docs/wiki/Testing.md) | scripted `StubClient` |
| [`llm/prometheusadapter`, `llm/oteladapter`, `llm/wideeventadapter`](docs/wiki/Observability.md) | metrics, traces and wide-event fields of the model calls |
| [`retry`](docs/wiki/Errors-and-Retry.md) | `Do`, `Policy`, `AfterError` |
| [`tools`](docs/wiki/Tools.md) | `Registry`, handlers, schema helpers |
| [`tools/shell`](docs/wiki/Shell.md) | virtual bash over a `vfs.Storage`, host programs only when allowed |
| [`agent`](docs/wiki/Agent.md) | `Session`, events, limits, `History`, `Locker`, built-in tools |
| [`agent/history/memory`](docs/wiki/Agent.md#history-and-locker), [`agent/history/historytest`](docs/wiki/Testing.md#conformance-suites) | in-process `History`, conformance suite |
| [`vfs`](docs/wiki/Files.md) | `Storage`, `Searchable`, `ScopedStorage`, path rules |
| [`vfs/memory`, `vfs/osfs`, `vfs/embedfs`](docs/wiki/Files.md#backends) | backends |
| [`vfs/overlay`](docs/wiki/Files.md#overlay), [`vfs/mount`](docs/wiki/Files.md#mount) | copy-on-write with diff, mounts by prefix |
| [`vfs/vfstest`](docs/wiki/Files.md#writing-your-own-backend) | conformance suite |
| [`voice`](docs/wiki/Voice.md) | speech in and out around an agent turn, NDJSON handler |
| [`cmd/aikido`](docs/wiki/CLI.md) | the CLI |

## Docs

- [Home](docs/wiki/Home.md): model, package map, which package for which need, wiring order.
- [Agent](docs/wiki/Agent.md): sessions, runs, events, end reasons, limits, prompt caching, History and Locker.
- [Tools](docs/wiki/Tools.md): registry, schemas, argument checks, the built-in file tools and `todo_write`.
- [Files](docs/wiki/Files.md): the `Storage` contract, backends, overlay, mount, own backends.
- [Models](docs/wiki/Models.md): requests, streaming, OpenRouter, Codex.
- [Media](docs/wiki/Media.md): images, video, speech, transcription, decision models.
- [Voice](docs/wiki/Voice.md): voice turns and the NDJSON handler.
- [Observability](docs/wiki/Observability.md): `NewObservedClient`, the metrics, spans and wide-event fields.
- [Errors-and-Retry](docs/wiki/Errors-and-Retry.md): sentinels, `APIError`, what is retried where.
- [Testing](docs/wiki/Testing.md): `StubClient`, conformance suites, testing an agent offline.
- [CLI](docs/wiki/CLI.md): every command and flag.
- [Security](docs/wiki/Security.md): guarantees and known limits.
- [Gotchas](docs/wiki/Gotchas.md): the rules that are easy to get wrong.
- [llms.txt](llms.txt): index for coding agents.
- [docs/migration.md](docs/migration.md): from `github.com/mxcd/aikido` v0.x.
- [docs/DECISIONS.md](docs/DECISIONS.md): architecture decisions.
- [pkg.go.dev](https://pkg.go.dev/github.com/govengers/aikido): API reference.

Releases and changelog: [GitHub Releases](https://github.com/govengers/aikido/releases).

## Examples

Each needs `OPENROUTER_API_KEY`: `go run ./examples/<name>`.

- [chat-oneshot](examples/chat-oneshot/main.go): one completion with `llm.Collect` and its cost.
- [agent-vfs](examples/agent-vfs/main.go): an agent writes and reads notes in an in-memory workspace.
- [agent-overlay](examples/agent-overlay/main.go): an agent edits a real directory through an overlay; you see the diff and confirm before anything is written.
- [loki-agent](examples/loki-agent/main.go): answers questions about logs in Grafana Loki: discovers labels, pulls lines with LogQL into an in-memory workspace, analyzes them with the virtual bash and reports findings, likely cause and next queries.
- [chatbot](examples/chatbot/main.go): a read-only Q&A loop over an embedded markdown corpus.
- [image-generation](examples/image-generation/main.go): an image from an image-capable model written to disk.
- [voicebot](examples/voicebot/main.go): a push-to-talk web page over `voice.Handler`.

## CLI

```bash
export OPENROUTER_API_KEY=sk-or-...   # or put it into .env in the working directory
# optional: OPENROUTER_BASE_URL replaces https://openrouter.ai/api/v1 (a proxy, a test server)

aikido chat "Give me one fun fact about Go."
aikido agent --dir ./notes "Fix the spelling in every file."   # prints changes and diff; --apply writes
aikido agent --dir ./notes --shell "Replace http:// with https:// in every markdown file."   # adds the virtual bash
# an unattended worker on a repo: virtual bash, real git and kubectl, the repo's own instructions
aikido agent --dir ~/worker --direct --shell --allow git --allow kubectl --env KUBECONFIG --system-file ~/worker/CLAUDE.md "Handle the alert: ..."
aikido video -d 4 -r 720p -a 16:9 --audio=false "A paper boat drifts across a pond."
aikido decide --noul 'is_bug=Is this a bug report?' 'Checkout shows a blank page after I click Pay.'
aikido models anthropic/claude-haiku-4.5
aikido skill install                # the Claude Code skill for this CLI
```

Every command and flag: [docs/wiki/CLI.md](docs/wiki/CLI.md).

### Images

```sh
aikido image [--model MODEL] [--out DIR] [--aspect W:H] [--size 1K|2K|4K] \
  [--ref FILE]... [--quality auto|low|medium|high|xhigh|max] [--max-tokens N] \
  [--images-api-models IDS] "<prompt>"
```

- `--model`, `-m`: default `openai/gpt-image-2.5-flare`, or `OPENROUTER_IMAGE_MODEL`.
- `--out`, `-o`: output directory, default `out`; files are `image-YYYYMMDD-HHMMSS-NN.<ext>`.
- `--aspect`, `-a`: `W:H` such as `1:1`, `4:5`, `9:16`, `16:9`, `21:9`. Images-endpoint models get the exact pixel frame (4:5 at 1K is 1024x1280), up to 3:1.
- `--size`: `1K` (default), `2K`, `4K` (`0.5K` on gemini-3.1-flash-image-preview); with `--aspect` on images-endpoint models the short edge is 1024/2048/4096, capped at a 3840 px edge and 8.3 MP.
- `--ref`: reference image file, repeat for several; both model families.
- `--quality`: images-endpoint models only; `max` costs about 16x `auto`.
- `--max-tokens`: default 1024, chat-completions models only.
- `--images-api-models`: ids routed to `/api/v1/images` (default `openai/gpt-image-2.5-flare,openai/gpt-image-2.5-sunburst,openai/gpt-image-2,meta/muse-image`, or `OPENROUTER_IMAGES_API_MODELS`); a value replaces the list, `none` disables the routing.

A frame the images endpoint cannot render (wider than 3:1, `--size 0.5K`) switches to `google/gemini-3.1-flash-image-preview` with a notice, unless the model was chosen explicitly; then it is an error.

```bash
aikido image --aspect 16:9 --size 2K "A pixel-art fox in tall grass."
aikido image -m google/gemini-3.1-flash-image-preview "A quick draft."
aikido image --ref logo.png --quality high "The same logo on a matte black mug."
```

## Security

Guarantees and known limits (no shell, path validation, `os.Root` confinement, overlay dry runs, no spending cap, among others): [docs/wiki/Security.md](docs/wiki/Security.md).

## Benchmark

[benchmark/](benchmark/) compares the agent with opencode on 12 coding tasks (precise edits, renames, bug fixes, mass replace, config edits, refactors, a multi-step spec) with the same models through OpenRouter. On 01.11.2026 (Claude Haiku 5.5 and GLM 5.3, 144 runs) aikido's file tools solved 47 of 48 runs, opencode 46 of 48, at a lower median time; a shell tool only paid off on shell-shaped tasks ([results](benchmark/RESULTS-2026-11-01.md), [ADR-041](docs/DECISIONS.md#adr-041---shell-access-for-agents)). The virtual shell (`tools/shell`) gets almost all of a real shell's gain on those tasks without host access. A second suite drives an ops worker through ICM workbooks with git, a fake cluster and a fake log CLI: `aikido agent --direct --shell --allow ...` solved 11 of 12 runs like Claude Code on the same repo and model, 17% faster and 36% cheaper ([results](benchmark/RESULTS-2026-11-01-v1.1.md)).

## Standards

Code follows the [govengers standards](https://github.com/govengers/standards) with the `library` profile. `.govengers.yml` lists the waived rules with their reasons: DES-001 for `agent.NewLocalSession` and `llmtest.NewStubClient`, RES-005 for `llm.CollectWithRetry`. `just check` runs lint, unit tests with `-race` and an 80 % coverage gate, the end-to-end suite in [test/e2e](test/e2e) (the CLI binary and the library against a fake OpenRouter) and `govulncheck`; `just smoke` runs the live tests (spends money). `docs/wiki` is the source of the GitHub wiki; CI mirrors it for every release (`just wiki` by hand). v1 follows semver: changes within v1 are additive. Releases: push a `vX.Y.Z` tag.

## License

[Apache 2.0](LICENSE)
