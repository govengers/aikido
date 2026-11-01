# Stage 02: extract

## Input
The resolve block.

## Process
Read files from `origin/main` only: `git -C repos/<repo> ls-tree -r --name-only origin/main apps/` and `git -C repos/<repo> show origin/main:<path>`.

1. Apps: every directory `apps/<app>/`.
2. Stages of an app: one per stage file `apps/<app>/values-<stage>.yaml` (`values.yaml` without a stage is the shared default, not a stage).
3. Namespace of an app in a stage: the top-level `namespace:` key of that stage file. Do not derive it from a naming pattern; some apps deviate.
4. Chart and version: `name` and `version` of `apps/<app>/Chart.yaml` (not `appVersion`, not the versions under `dependencies`).
5. Open items: every `- ` bullet of the repo's `TODO.md`, verbatim, in file order.

## Output
The rows and bullets, ready for the inventory.

## Completion
Proceed to `03-write-reconcile`.
