# Stage 01: resolve

## Input
A component name (`billing`) or `<org>/<component>`.

## Process
1. Find the row in `infra/README.md`: the repo path under `repos/`.
2. `git -C repos/<repo> fetch -q origin`. Note the `origin/main` sha (7 characters) and its commit date (`git -C repos/<repo> log -1 --format=%cd --date=format:%d.%m.%Y origin/main`). Note whether the local checkout is behind or dirty (informational: the sync reads `origin/main` regardless, never the working tree).
3. Read the README `## sync` section of the component.

## Output
```
component: <org>/<component>, repo repos/<repo>
origin/main: <sha7> <dd.MM.yyyy>, local: <in sync|behind n|dirty>
```

## Completion
Proceed to `02-extract`.
