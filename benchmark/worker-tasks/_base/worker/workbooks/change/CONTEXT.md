# change (L1)

A planned change to a component: config or version bump, resource sizing, replicas. Git-first, on a branch, recorded.

## Load

| Resource | When | Why |
|----------|------|-----|
| `stages/<current>/CONTEXT.md` | always | contract |
| `workbooks/_shared/learn/CONTEXT.md` | last stage | the run report contract |
| `infra/<org>/<component>/README.md` | stage 01 | conventions of that repo (branch naming, stage files, push = deploy) |
| `infra/<org>/<component>/inventory.md` | stage 01 | where the thing lives today |
| the gitops repo's own `README.md` | stage 02 | repo-local rules win over this workbook |

## Do not load

| Resource | Why |
|----------|-----|
| other components | different conventions |
| `workbooks/incident` | not a diagnosis task |
| other apps of the repo | read only the app you change |

## Stages

1. `01-scope`: what exactly, which file, blast radius
2. `02-edit`: the change on a branch, validated locally
3. `03-ship`: commit, push the branch; merging to `main` (= deploy) is a human's yes
4. `04-verify-record`: what is live, what is pending
5. `05-learn`: mandatory run report (`workbooks/_shared/learn/CONTEXT.md`)
