# sync-component (L1)

Map one component's gitops repo into `infra/<org>/<component>/inventory.md` and reconcile the hand-written README. Also the onboarding path for a new component.

## Load

| Resource | When | Why |
|----------|------|-----|
| `stages/<current>/CONTEXT.md` | always | contract |
| `workbooks/_shared/learn/CONTEXT.md` | last stage | the run report contract |
| `infra/<org>/<component>/README.md` `## sync` section | stage 01 | where things live in this repo's layout |
| the repo's `README.md`, `TODO.md` at `origin/main` | stage 02 | the repo's own map and open items |

## Do not load

| Resource | Why |
|----------|-----|
| the working tree of the checkout | sync from `origin/main`; a dirty or stale checkout is someone's half-done work |
| other components | one component per run |

## Stages

1. `01-resolve`: repo path, fetch, `origin/main` sha and date
2. `02-extract`: apps, stages, namespaces, charts and versions, open items, all from `origin/main`
3. `03-write-reconcile`: `inventory.md` in the fixed format, README corrections, `infra/README.md` row
4. `04-learn`: mandatory run report (`workbooks/_shared/learn/CONTEXT.md`)
