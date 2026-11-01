# Run reports

One file per workbook run, written by the mandatory `learn` stage (`workbooks/_shared/learn/CONTEXT.md`). Never delete reports; they are the evidence for why a README says what it says.

File name: `<yyyy-mm-dd>-<workbook>-<slug>.md`. Template:

```markdown
---
date: dd.MM.yyyy
workbook: incident | change | sync-component | none
component: <org>/<component>
outcome: done | partial | aborted | handed-over
duration: <approx, e.g. 25 min>
---

## Task
<one line>

## What went wrong on first try
- <exact command or step> → <what happened> → <what worked instead>

## Instructions that were wrong or missing
- <file>#<section>: <what it said> / <what is true>   (fixed now: yes|no)

## Missing access
- <scope or precondition> → <how it was worked around, or blocked>

## Tool / environment friction
- <tool version, flag, timeout, quirk>

## Suggested improvements
- <readme|workbook|alert>: <concrete change>
```

`outcome`: `done` = solved and verified; `partial` = diagnosed or changed, but something is left (a proposal waiting for a yes, a merge pending); `aborted` = stopped early; `handed-over` = a human took over.
