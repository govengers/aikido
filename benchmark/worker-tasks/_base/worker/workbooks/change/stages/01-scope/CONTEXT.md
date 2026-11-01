# Stage 01: scope

## Input
The requested change in the human's words.

## Process
1. Resolve component and repo from `infra/README.md`; read the component README fully.
2. Locate today's state in `inventory.md` and in the repo: the app directory and the exact stage file (`values-<stage>.yaml`). Confirm the change belongs in git, not in the cluster.
3. Blast radius: which stage and namespace the file deploys to; other stages must stay untouched.
4. `git -C repos/<repo> status -sb` and `git -C repos/<repo> fetch`: a dirty tree is a stop; a stale `main` is fast-forwarded before branching.

## Output
```
component: <org>/<component>, repo <path> @ main <sha>, tree clean: <yes|no>
change: <one line>   files: <paths>
deploys via: <argocd auto-sync on merge to main>
```

## Completion
Proceed to `02-edit`.
