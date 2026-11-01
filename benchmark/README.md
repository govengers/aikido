# Harness benchmark: aikido vs opencode

Two suites, chosen with `-suite`: `coding` (default, this section down to "Fairness") and `worker` (agentic ops on an ICM devops-worker repo, see [Worker suite](#worker-suite)).

The coding suite compares three agent harnesses on the same coding tasks with the same model through OpenRouter:

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

## Worker suite

Question: can aikido (its virtual bash plus allowed host programs) replace starting Claude Code on the devops worker? The suite has an agent operate an ICM-structured worker repo unattended, with git, kubectl and a Loki logcli as host tools, and checks what it left behind: commits on the remotes, files, and the log of every kubectl/logcli call.

```sh
just validate                                  # both suites; worker: every check.sh fails on the fresh setup and passes after solve.sh
just smoke-worker                              # incident-crashloop x claude-code, opencode x haiku, about $0.03
just bench-worker                              # 4 tasks x 3 harnesses x haiku x 3 reps = 36 runs
go run ./cmd/bench -suite worker -harness claude-code -task 'incident-*' -reps 2 -keep
```

Extra needs: `git`, `perl`, `claude` (logged in). Results land in `results/<run-id>/` like the coding suite; the aikido-worker transcript is `transcript.log` (the CLI's text output).

### Harnesses (all on Haiku 5.5)

| harness | what runs |
| --- | --- |
| `claude-code` | `claude -p --model claude-haiku-5-5 --output-format stream-json --verbose --permission-mode bypassPermissions` plus the isolation flags below, cwd = the worker clone. Tool calls per `tool_use` block, LLM calls per distinct assistant message id, tokens and cost (`total_cost_usd`, `usage`) from the final `result` event. `stream-json` instead of `json` because only it carries the tool calls; its `result` event is the object `json` prints. |
| `opencode` | as in the coding suite (`openrouter/anthropic/claude-haiku-5.5`, isolated HOME), but with Claude Code compatibility left on so it reads the worker repo's `CLAUDE.md` (no `~/.claude` exists in its HOME). |
| `aikido-worker` | the `aikido` CLI built from this checkout: `aikido agent --dir <worker clone> --direct --shell --allow git --allow kubectl --allow logcli --env FAKE_CLUSTER_STATE --env FAKE_LOG --env GIT_CONFIG_GLOBAL --env GIT_CONFIG_NOSYSTEM --env KUBECONFIG --system-file CLAUDE.md --max-turns 80 --model <model> "<task.md>"`, cwd = the worker clone. Tool calls from `[tool-result]` lines, tokens and cost from `[usage]` lines (no cache tokens there), end reason from `[end]`. Skipped with a message while `aikido agent --help` lacks one of `--direct --shell --allow --env --system-file`. |

**Claude Code isolation (decided 01.11.2026, Claude Code 2.1.316 on macOS).** An empty per-run `HOME` breaks the login (`Not logged in · Please run /login`), so claude runs with the real `HOME` and the user layer switched off by flags: `--setting-sources project` (no user settings, hooks, plugins or `~/.claude/CLAUDE.md`; the worker repo's `CLAUDE.md` still loads), `--disable-slash-commands` (no skills), `--strict-mcp-config --mcp-config '{"mcpServers":{}}'` (no configured or claude.ai MCP servers), `--no-session-persistence` and `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1` (nothing written under `~/.claude`; without it each run left an empty `~/.claude/projects/<tmp>/memory/`). The parent session's `CLAUDECODE`/`CLAUDE_CODE_*` markers are stripped. The smoke run's `system/init` event showed no MCP servers, skills or slash commands and only Claude Code's builtin plugins; `~/.claude` and `~/.claude.json` stayed unchanged. `ANTHROPIC_BASE_URL` is inherited, so claude goes wherever the user's own claude goes.

### Layout of a run

`setup.sh` (sourcing `worker-tasks/lib.sh`) builds, under the run's temp root:

- `work/`: clone of the worker repo (`_base/worker` plus the task's `fixture/` overlay), the agent's cwd; an untracked `incident-notes.txt` is the stray-file trap for `git add -A`.
- `work/repos/<name>/`: clones of the gitops repos (`shop-gitops`, `billing-gitops`), gitignored in the worker repo.
- `remotes/<name>.git`: bare remotes, so `git push` works and checks read the truth from there. All commits have fixed dates, so shas are identical on every run.
- `state/cluster.json` (the task's canned cluster), `state/tools.log`, `state/<name>.base` (each remote's `main` after setup): outside the workspace, reached only through `FAKE_CLUSTER_STATE` and `FAKE_LOG`.
- `bin/kubectl`, `bin/logcli`: the fakes, first on `PATH`. `GIT_CONFIG_GLOBAL` is a per-run config (identity, no user hooks or signing), `KUBECONFIG=/dev/null`.

The synthetic org is `acme` (components `acme/shop` and `acme/billing` on cluster `eu1`), modelled on the WIT devops worker's structure: L0 routing table and rules in `CLAUDE.md`, workbooks `incident`, `change`, `sync-component` with L1 `CONTEXT.md` load/do-not-load tables and L2 stage files, the shared `learn` stage, `journal/YYYY-MM.md`, `journal/runs/` with the report template, `infra/README.md`, component READMEs and inventories, `TODO.md`. No customer facts, hosts or names were copied.

### Fake tools

- `cmd/fakekubectl`: `get` (pods, deployments, replicasets, jobs, cronjobs, events, services, endpoints, configmaps, ingresses, hpa, applications, nodes, namespaces, `all`, `kind/name`, comma lists; `-n`, `-A`, `-l`, `--field-selector`, `-o wide|yaml|json|name`, `--show-labels`, `--no-headers`), `describe` (exact or prefix name), `logs` (pod, `deploy/x`, `job/x`, `-l`, `-c`, `--previous`, `--tail`), `rollout status|history`, `auth can-i`, `config current-context|get-contexts|view`, `version`, `cluster-info`, `api-resources`, `top` (Metrics API not available). Mutating verbs (`apply create delete patch edit scale replace set label annotate rollout restart|undo|pause|resume exec cp ...`) get the API server's RBAC `Forbidden` error, exit 1, and are logged with `"mutation":true`. Answers come from precomputed rows, manifests and canned texts in `cluster.json`.
- `cmd/fakelogcli`: `logcli query '<logql>' --since --from --to --limit --forward -o default|raw|jsonl -q`, `labels`, `series`, with logcli's stderr header lines and `<ts> {labels} line` output. Streams are the pod logs of `cluster.json` (lines starting with an RFC 3339 time) plus extra `loki.streams`. Stream selectors with `= != =~ !~`, line filters `|= != |~ !~`; parser and label-filter stages are accepted and ignored; metric queries are refused.
- Both append `{"tool","args","mutation","exit"}` to `$FAKE_LOG`. Tests: `go test ./cmd/fakekubectl ./cmd/fakelogcli`.

### Tasks

| task | category | prompt | checker (trusts only remotes, files, tool log) |
| --- | --- | --- | --- |
| `incident-crashloop` | incident | shop-api in acme/shop prod crashloops since 06:40 (alert) | no mutating kubectl call; at least one `kubectl describe` or `logs`; gitops remotes untouched; worker `main` fast-forwarded, stray file not committed, only the run report, the month's journal, the shop README and `TODO.md` changed; exactly one new `journal/runs/<yyyy-mm-dd>-incident-<slug>.md` with the template's front matter (`date` matching the file name, `workbook: incident`, `component: acme/shop`, `outcome`) and all six sections, naming `OOMKilled`, `256Mi` and the trigger (2.14.0 / cart cache); exactly one new journal entry `- dd.MM.yyyy acme/shop: ...` naming the report |
| `incident-known-pattern` | incident | ledger-sync export to the bank failed tonight (alert) | as above for acme/billing (any kubectl or logcli call), plus the report references the earlier `journal/runs/2026-09-03-incident-billing-ledger-sync-hostkey.md` and proposes its fix (`hostKey`) |
| `change-memory-limit` | change | raise shop-api memory limit in staging to 512Mi | shop-gitops `main` untouched; exactly one pushed branch `change/<slug>` based on `main` whose only change is `resources.limits.memory: 512Mi` in `apps/shop-api/values-staging.yaml` (prod file, requests and the exporter's limit are decoys); no mutation; worker run report (`workbook: change`) naming the branch and 512Mi, journal entry, pushed |
| `sync-component` | sync | sync acme/billing | `infra/acme/billing/inventory.md` on the worker remote equals the format of `sync-component` stage 03 filled from billing-gitops `origin/main` (normalized for table padding only); traps: the local checkout is one commit behind (new app `dunning-mailer`, invoice-api 1.9.0) and dirty (billing-db chart version), a shared `values.yaml` is not a stage, billing-db prod runs in `billing-data`; run report, journal entry, pushed |

Every task has `task.md`, `category`, `fixture/cluster.json` (plus optional overlays of `_base`), `setup.sh`, `check.sh` and `solve.sh` (a reference run that also calls the fake tools); `sync-component` has `expected/inventory.md`. Wrong solutions were checked to fail too: `git add -A` (stray file committed), a `kubectl rollout restart`, a push of the change to `main`, and an inventory built from the stale working tree.

### Caveats

- **The cluster clock is frozen** at `cluster.json`'s `now` (ages, `--since`) while git and `date` show the real day; one smoke agent noticed and said so in its report. The checks accept any run date as long as file name, front matter and journal line agree.
- **The fakes are recognisable** if an agent inspects them. With the tools in a `fake-tools/` directory, one opencode smoke run went around them to `/opt/homebrew/bin/kubectl`, which failed because `KUBECONFIG=/dev/null`. The tools now live in the run's neutral `bin/`, and `CLAUDE.md` says (as the real worker does about its wrapper) that only the `PATH`-first tools carry credentials. A real kubectl can still be called by absolute path; it reaches nothing.
- **Metric LogQL queries** (`count_over_time`, `rate`) are refused; agents fall back to log queries.
- **Costs are each harness's own number**: Claude Code's `total_cost_usd`, opencode's computed `cost`, OpenRouter `usage.cost` for aikido. Files and bytes changed are not measured in this suite; the checks read the remotes.
- **Smoke, 01.11.2026, haiku, incident-crashloop, 1 rep each:** claude-code PASS in 65 s, 13 LLM calls, 18 tool calls, $0.015; opencode PASS in 52 s, 17 LLM calls, 21 tool calls, $0.010; aikido-worker skipped (flags not in the CLI yet).
