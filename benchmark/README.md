# Harness benchmark: aikido vs opencode

Compares three agent harnesses on the same coding tasks with the same model through OpenRouter:

| harness | what runs |
| --- | --- |
| `aikido` | the library in-process: `agent.NewLocalSession`, `openrouter` client, `vfs/osfs` on the work dir, `agent.RegisterVFSTools` (all tools, writable) and `agent.RegisterPlanTool`. 60 turns, run timeout = `-timeout`, every other option at its library default (prompt caching on, 64 KiB tool result cap, 2 min tool timeout). |
| `aikido-bash` | the same plus a prototype `bash` tool (`cmd/bench/bash.go`, benchmark-only, not in the library): `bash -c` in the work dir, 60 s per call, combined stdout/stderr cut at 30 KB with a notice, exit code in the result, stdin empty, process group killed on timeout. |
| `opencode` | `opencode run --standalone --auto --format json -m openrouter/<model> <task.md>` in the work dir. Tokens, cost and tool calls come from its JSON event stream (`step_finish`, `tool_use`). |

The questions it answers: (a) how good aikido's built-in VFS tools are against a mature coding agent, and (b) what a CLI tool would add (aikido vs aikido-bash).

## Running

Needs `go`, `bash`, `jq`, `opencode` on `PATH`, and `OPENROUTER_API_KEY` (falls back to `CLAUDE_OPENROUTER_API_KEY`).

```sh
just validate          # every check.sh fails on its fixture and passes on solve.sh (no LLM)
just smoke             # 2 tasks x 3 harnesses x haiku x 1 rep, about $0.01
just bench             # 12 tasks x 3 harnesses x 2 models x 3 reps = 216 runs
just bench 5 -parallel 8   # more reps, more concurrency
just summarize results/<run-id>
```

Direct: `go run ./cmd/bench -harness aikido,opencode -model z-ai/glm-5.3 -task 'mass-*,fix-bug' -reps 2 -parallel 4 -timeout 10m [-keep]`.

Results land in `results/<run-id>/` (gitignored): `results.jsonl` (one line per run), `summary.md` (pass rate per harness/model, per category and per task, median wall time, LLM and tool calls, tokens, cost, tool-call mix, failures), `run.json` (flags, opencode version, aikido commit, system prompt) and `runs/<harness>__<model>__<task>__<rep>/` with `transcript.jsonl`, `check.log` and for opencode `stderr.log`. `-keep` keeps the work directories and writes their path to `workdir.txt`.

## Models

- `anthropic/claude-haiku-5.5` (default): small, cheap ($0.10/$0.50 per M tokens), current Anthropic model.
- `z-ai/glm-5.3`: open weights, built for agentic software engineering, cheap ($0.07/$1.40 per M), strong on SWE-bench Verified and Terminal-Bench, and already the open model of choice in earlier opencode review rounds. It also shows behaviour on a non-Anthropic tool-calling dialect.

Both verified via `GET https://openrouter.ai/api/v1/models` on 2026-11-01 (tools supported).

## Method

For each (harness, model, task, rep): copy `tasks/<task>/fixture` to a fresh temp dir, run the harness with `task.md` as the only user message and a wall-clock timeout (default 10 min), measure the edit (files changed, bytes changed as the differing middle per file), then run `check.sh` in the result dir. Pass = exit 0. Jobs run in a bounded pool (`-parallel`, default 4), ordered rep, task, model, harness so a partial run covers every setup.

Metrics per run: pass, wall seconds, LLM calls, input/output/cache-read/cache-write tokens, cost USD, tool calls by name, failed tool calls, files and bytes changed, end reason, error, tail of the check output on failure. Tokens follow OpenRouter's shape: input includes cached tokens, output includes reasoning.

## Tasks

| task | category | what it tests | checker |
| --- | --- | --- | --- |
| `edit-precise-line` | edit | change one price in a 3006-line Go file; the target is past line 2000 (beyond one default read) and the same price sits on neighbour SKUs and in a promo file | whole tree byte-equal to fixture plus the one change |
| `rename-function` | refactor | rename `invoice.CalcTotal` across 6 files incl. tests and doc comments; decoy `shipping.CalcTotalWeight` must survive | no `CalcTotal` left, decoy intact, hidden test calls the new name, build/vet/test |
| `fix-bug` | bugfix | off-by-one in a page-range parser makes tests fail | tests restored from fixture, hidden extra cases, build/vet/test |
| `create-from-spec` | create | write a generic ring buffer package from a written spec | hidden test suite, vet/test |
| `mass-replace` | mass-edit | replace one hostname in 40 of 41 files (yaml, md, go, sh) with two decoy hosts | tree equal to `sed` applied to the fixture |
| `config-edits` | config | 4 JSON edits, 4 YAML edits that must keep comments and formatting; decoy ports and limits | JSON semantically via `jq -S`, YAML textually, nothing else changed |
| `delete-dead-code` | delete | delete 4 dead symbols plus a helper that becomes dead, and the imports that become unused | names gone, used code kept, tests restored, build/vet/test |
| `extract-function` | refactor | extract duplicated email validation into `normalizeEmail` | messages occur once, both call sites call it, hidden test of the function, tests restored |
| `codebase-question` | search | find the server read timeout default, its env var (prefix built in code) and file; stale README and legacy constant as decoys | exact three-line ANSWER.md, nothing else changed |
| `docs-update` | docs | edit a date, a table cell, insert a section at an exact place | text equal to expected (trailing blanks and blank-line runs ignored) |
| `multi-step-spec` | multi-step | read SPEC.md, implement `TopWords`, a CLI subcommand with flags, and a README section | hidden tests, builds and runs the CLI against sample input, README structure |
| `count-lines` | shell | list line counts of 35 Go files under `pkg/` plus total, decoy files around | exact LINES.md |

Every task directory holds `task.md` (the prompt, verbatim), `category`, `fixture/` (each Go fixture is its own module, compiles with Go 1.26), `check.sh` (sources `tasks/lib.sh`; looks only at files, never at the agent's claims; tests that must not change are restored from the fixture first), `solve.sh` (reference solution, used by `-validate`), and where needed `checks/` (hidden tests copied in before testing), `solution/` or `expected/`. Checkers and solutions live outside the work dir, so the agent never sees them. `tasks/go.mod` only keeps these Go files out of the benchmark module.

Adding a task: create the directory with the files above and run `just validate`.

## Fairness and known weaknesses

- **System prompts differ.** aikido gets a five-sentence generic coding-agent prompt (`systemPrompt` in `cmd/bench/aikido.go`, also in `run.json`); opencode sends its own prompt (about 13k tokens including tool definitions, environment info and the date). This is the harness as shipped, which is what we want to compare, but part of any difference is the prompt, not the tools.
- **Tool sets differ.** opencode has shell, read, edit, write, grep, glob, list, todo, web fetch and subagents; aikido has its VFS tools and todo_write, aikido-bash adds one bash tool. Tool names in the summary are each harness's own.
- **Prompt caching** is on in both (aikido sets breakpoints by default since wave 2; opencode sets its own). Runs of the same setup that follow each other within 5 minutes share cache hits on the static prefix, which favours setups with large static prompts. Costs are the harness's own report: OpenRouter `usage.cost` for aikido, opencode's computed `cost` for opencode.
- **Isolation of opencode.** Each run gets its own `HOME` and XDG tree under the run's temp dir with a minimal `opencode.json` (no autoupdate, no sharing), `OPENCODE_DISABLE_CLAUDE_CODE=1` and `PWD` set to the work dir (opencode resolves its project from `$PWD`; an inherited `PWD` sent it into this checkout during development). The user's `~/.config/opencode` is never read or written. Go caches are passed through, so `go test` stays warm.
- **No sandbox.** opencode's shell and the prototype bash tool run as the current user and can reach outside the work dir. The runner strips OpenRouter keys from the bash tool and check environments (opencode itself needs its key), and fails a run that modified its task directory.
- **Saturation.** In a 1-rep preview on haiku all 36 runs passed. The tasks are small and realistic, so on capable models the signal is in time, cost, LLM and tool calls, not in pass rate. Harder tasks (larger repos, ambiguous specs) would be the next step if pass rate has to discriminate.
- **Variance.** Model sampling is not seeded; use `-reps 3` or more and compare medians.
- **LLM calls** are counted per usage report (aikido) and per `step_finish` (opencode); one opencode step can hold several parallel tool calls, like one aikido turn.
- **bytes_changed** is a cheap estimate (differing middle per file), not a diff size.
