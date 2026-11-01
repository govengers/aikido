# incident (L1)

Something is broken, alerting, slow, or "does not work for X": find out what changed, diagnose, propose a fix (apply only with a go-ahead), report, learn.

## Load

| Resource | When | Why |
|----------|------|-----|
| `stages/<current>/CONTEXT.md` | always | the contract for this step |
| `workbooks/_shared/learn/CONTEXT.md` | last stage | the run report contract |
| `infra/<org>/<component>/README.md` | stage 01 onwards | namespaces, gotchas of the affected component |
| `infra/<org>/<component>/inventory.md` | stage 02 | which app, chart, stage |
| `journal/*.md`, `journal/runs/*.md` (grep only, never read whole) | stage 01 | it has probably happened before |
| the component's gitops repo under `repos/` (`git log`, the app's values) | stage 02 | what changed |

## Do not load

| Resource | Why |
|----------|-----|
| other components' folders | wrong namespaces, wrong repo |
| `workbooks/change`, `workbooks/sync-component` | different task; stage 03 links the change procedure if needed |

## Stages

1. `01-triage`: what, where, since when, blast radius, is it a known pattern
2. `02-diagnose`: what changed, evidence, root cause hypothesis with confidence
3. `03-fix`: proposal → go-ahead → apply (git-first), or stop at the proposal when unattended
4. `04-verify-report`: prove recovery or state what is still broken, report
5. `05-learn`: mandatory run report (`workbooks/_shared/learn/CONTEXT.md`)
