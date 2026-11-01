# Stage 03: fix

## Input
The diagnosis block from stage 02, or the documented fix of a known pattern.

## Process
1. Pick the smallest fix that removes the root cause, git-first: a change in the gitops repo beats `kubectl`, a revert beats a forward fix under time pressure.
2. Write the proposal as the exact diff or the exact commands, plus the expected effect and the rollback.
3. **Wait for an explicit yes.** Unattended run: stop here, the proposal goes into the report and the run report. Do not run it.
4. Attended and approved: apply git changes via the `change` workbook; operational changes one command at a time.

## Output
```
applied: <commit sha / commands>   or "proposed only"
proposal: <diff or commands>
rollback: <how>
```

## Completion
Proceed to `04-verify-report`.
