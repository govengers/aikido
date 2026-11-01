# CLI

`aikido` is a thin CLI over the library, against OpenRouter.

```bash
go install github.com/govengers/aikido/cmd/aikido@latest
```

## Environment

| Variable | Used by | Meaning |
|---|---|---|
| `OPENROUTER_API_KEY` | every command | required; without it: `OPENROUTER_API_KEY is not set` |
| `OPENROUTER_BASE_URL` | every command | replaces the API root `https://openrouter.ai/api/v1` (a proxy, a test server) |
| `OPENROUTER_IMAGE_MODEL` | `image` | default image model |
| `OPENROUTER_IMAGES_API_MODELS` | `image` | comma-separated ids routed to `/api/v1/images` |
| `OPENROUTER_VIDEO_MODEL` | `video` | default video model |
| `OPENROUTER_DECISION_MODEL` | `decide` | default decision model |

A `.env` in the working directory is loaded first; variables already set in the environment win. `aikido --version` prints the module version the binary was built from (a checkout build prints its pseudo-version).

## chat

`aikido chat [options] <prompt...>`: one completion, then a usage line (tokens, cost, serving provider, latency, duration, throughput).

| Flag | Default |
|---|---|
| `--model`, `-m` | `anthropic/claude-haiku-4.5` |
| `--system`, `-s` | none |
| `--max-tokens` | 512 |
| `--temperature`, `-t` | -1 (below 0 leaves the provider default) |
| `--provider` (repeatable) | none; one slug pins it (no fallbacks), several form a fallback chain; slugs from `aikido models author/slug` |

## agent

`aikido agent [options] <prompt...>`: an agent run with the built-in file tools ([Tools](Tools.md#built-in-file-tools): `read_file`, `list_files`, `write_file`, `delete_file`, `grep`, `glob`, `edit_file`, `multi_edit`, `move_file`, `search`). It prints text, every tool call and result, usage per model call and the end reason.

- Without `--dir` the workspace is empty and in memory; the final file list is printed.
- `--dir path` works on a copy-on-write overlay of the directory ([Files](Files.md#overlay)). At the end the CLI prints the changes and a unified diff; nothing is written unless `--apply` is set.
- `--mount name=path` adds a directory under `name/`, `name=path:ro` read-only; writable mounts are written with `--apply` like `--dir`.
- `--apply` without `--dir` or `--mount` is an error.
- `--shell` adds the virtual `bash` tool ([Shell](Shell.md)). `--direct` (needs `--dir`) writes every change to disk as it happens instead of through an overlay; the shell then shows the directory at its absolute host path (`shell.Options.MountPoint`), so `pwd`, `$PWD` and every absolute path a script hands to a host program are host paths, and `/`, or any path outside the directory except `/tmp` and `/dev`, does not exist. `--allow name` or `--allow name=path` (needs `--shell` and `--direct`) lets the script run that host program in the directory the script `cd`'d to (not combinable with `--mount`: the mounts do not exist on disk); `--env NAME` or `--env NAME=value` passes variables to those programs only.
- **`--allow` gives the agent everything that program can do.** git runs programs named in its configuration and options (`core.sshCommand`, `diff.external`, `core.fsmonitor`, `--upload-pack`), kubectl runs plugins. For an allowed `git` the CLI refuses `-c`, `--config-env`, `--exec-path`, `-C`/`--git-dir`/`--work-tree` outside `--dir`, and `--upload-pack`, `--receive-pack`, `--exec`, `--config`, `--template` (and `clone -u`/`-c`); with `--hide-hidden` the shell cannot write `.git/config` or hooks. A repository's own configuration is trusted. Allow only programs whose every option you would let the agent use.
- `--apply` writes only when the run ended with `stop`. After an error, a timeout, a cancel or `--max-turns` the changes are printed, nothing is written, and the command fails with `--apply refused: the run ended with <reason>`, also when there were no changes.
- A run that ended with an error, a timeout or a cancel exits non-zero (`the run ended with <reason>: <cause>`), with or without `--apply`; an interrupt exits 130. A run that hit `--max-turns` without `--apply` exits zero.

| Flag | Default |
|---|---|
| `--model`, `-m` | `anthropic/claude-sonnet-4.6` |
| `--system`, `-s` | a short prompt for working in a virtual file system |
| `--session` | `cli-session` |
| `--max-tokens` | 4096 per reply |
| `--temperature`, `-t` | -1 |
| `--provider` (repeatable) | none |
| `--max-turns` | 20 |
| `--hide-hidden` | on; hides paths with a segment starting with `_` or `.` from the file tools and from `bash` (`--hide-hidden=false` shows them); symbolic links in the directories are not followed then, since a visible link could point at a hidden file |
| `--dir` | none |
| `--apply` | off |
| `--mount` (repeatable) | none |

```bash
aikido agent "Create plan.md with one bullet, then list files."
aikido agent --dir ./docs "Fix the spelling in every file."            # dry run: changes and diff
aikido agent --dir ./docs --apply "Fix the spelling in every file."    # writes them
aikido agent --dir . --mount spec=../spec:ro "Implement what spec/api.md describes."
```

## image

`aikido image [options] <prompt...>`: generates images and writes them to `--out` as `image-YYYYMMDD-HHMMSS-NN.<ext>`, then prints each path, returned URLs, text and a usage line.

| Flag | Default | Meaning |
|---|---|---|
| `--model`, `-m` | `openai/gpt-image-2.5-flare` (`$OPENROUTER_IMAGE_MODEL`) | image model |
| `--out`, `-o` | `out` | output directory |
| `--aspect`, `-a` | none | `W:H`: `1:1`, `4:5`, `9:16`, `16:9`, `21:9`, ...; on images-endpoint models sent as the exact pixel frame (4:5 at 1K is 1024x1280), up to 3:1 |
| `--size` | `1K` | `1K`, `2K`, `4K` (`0.5K` on gemini-3.1-flash-image-preview); with `--aspect` on images-endpoint models the short edge is 1024/2048/4096, capped at a 3840 px edge and 8.3 MP |
| `--ref` (repeatable) | none | reference image file, both model families |
| `--quality` | provider default | `auto`, `low`, `medium`, `high`, `xhigh`, `max`; images-endpoint models only, `max` costs about 16x `auto` |
| `--max-tokens` | 1024 | chat-completions models only |
| `--images-api-models` | `openai/gpt-image-2.5-flare,openai/gpt-image-2.5-sunburst,openai/gpt-image-2,meta/muse-image` (`$OPENROUTER_IMAGES_API_MODELS`) | ids routed to `/api/v1/images`; a value replaces the list, a non-matching value such as `none` disables the routing |

A frame the images endpoint cannot render (wider than 3:1, or `--size 0.5K`) switches to `google/gemini-3.1-flash-image-preview` with a notice on stderr, unless the model was chosen explicitly (`-m` or the environment); then it is an error that names that model. An error that mentions the images endpoint or output modalities means the model is served only on `/api/v1/images`: add it to `--images-api-models`.

```bash
aikido image --aspect 16:9 --size 2K "A pixel-art fox in tall grass."
aikido image -m google/gemini-3.1-flash-image-preview "A quick draft."
aikido image --ref logo.png --quality high "The same logo on a matte black mug."
```

## video

`aikido video [options] <prompt...>`: submits a job, polls it and writes `out/video-<timestamp>.<ext>`, then prints cost, wall time and the job id. Takes minutes. Unset flags are not sent, leaving the model default; `aikido models --video` lists what each model accepts.

| Flag | Default |
|---|---|
| `--model`, `-m` | `google/veo-3.1-lite` (`$OPENROUTER_VIDEO_MODEL`) |
| `--out`, `-o` | `out` |
| `--aspect`, `-a` | none (`16:9`, `9:16`, `1:1`, ...) |
| `--resolution`, `-r` | none (`480p`, `720p`, `1080p`, `4K`) |
| `--size` | none; exact `WIDTHxHEIGHT` instead of aspect and resolution |
| `--duration`, `-d` | none (seconds) |
| `--audio` | model default; `--audio=false` for a silent, cheaper clip |
| `--seed` | none |
| `--first-frame`, `--last-frame` | none; image files the clip starts from or ends on |
| `--ref` (repeatable) | none; ignored when a frame is set |

```bash
aikido video -d 4 -r 720p -a 16:9 --audio=false "A paper boat drifts across a pond."
```

## decide

`aikido decide [options] <state...>` (or the state on stdin): asks a decision model typed questions and prints one answer per question. Questions are `NAME=INSTRUCTIONS`; choice and score append `=OPT1,OPT2,...` (score levels ordered first to last). CLI options carry names only; descriptions per option need the library.

| Flag | Default |
|---|---|
| `--model`, `-m` | `~typesafe/jev-latest` (`$OPENROUTER_DECISION_MODEL`) |
| `--noul`, `--choice`, `--score` (repeatable) | questions |
| `--box` (repeatable) | bounding-box question on `--image` |
| `--image` (repeatable) | image file added to the state |
| `--provider` (repeatable) | none |

```bash
aikido decide --noul 'is_bug=Is this a bug report?' \
  --choice 'team=Which team owns it?=payments,frontend,account' \
  --score 'urgency=How urgent?=can wait,this week,blocking revenue' \
  'Checkout shows a blank page after I click Pay.'
```

## models

`aikido models [options] [filter | author/slug]`: without an argument every model (id, context, USD per million in and out); a word filters by substring; an `author/slug` id lists its providers (tag for `--provider`, quantization, prices, uptime, p50 latency and throughput over 30 minutes). `--video` lists video models with their accepted values, `--decisions` the Decisions API models.

## skill

`aikido skill install` writes the bundled Claude Code skill (embedded in the binary) that teaches an agent to drive this CLI, chiefly for images.

| Flag | Default |
|---|---|
| `--scope` | `user` (`~/.claude/skills`); `project` is `./.claude/skills` |
| `--dir` | overrides `--scope` |
| `--force`, `-f` | overwrite an existing SKILL.md |
| `--print` | print the skill instead of installing |
