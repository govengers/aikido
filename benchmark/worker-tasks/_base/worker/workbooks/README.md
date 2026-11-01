# Workbooks

Staged instructions for recurring tasks, after the Interpretable Context Methodology (ICM: folder structure as agent architecture, numbered stages, explicit load and do-not-load tables, one Input → Process → Output → Completion contract per stage).

```
workbooks/<task>/
  CONTEXT.md              L1: goal, what to load (Resource | When | Why), what NOT to load, stage list
  stages/NN-<name>/
    CONTEXT.md            L2: Input, Process, Output, Completion; max 80 lines
```

Routing (L0) is the table in the root `CLAUDE.md`. Layering: root `CLAUDE.md` (always) → workbook `CONTEXT.md` (on entry) → current stage `CONTEXT.md` (per step) → component README (per step). Nothing else.

Conventions:
- Stages are numbered, run in order, and hand over a small structured result (a few lines of facts), not prose.
- Every workbook ends with the shared, mandatory `learn` stage (`_shared/learn/CONTEXT.md`): a run report in `journal/runs/`.
- A stage that needs a mutation names it explicitly and waits for a yes. In unattended runs the workbook stops there and reports the proposal.
