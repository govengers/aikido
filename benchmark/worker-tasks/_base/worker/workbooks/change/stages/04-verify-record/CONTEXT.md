# Stage 04: verify and record

## Input
The ship block.

## Process
1. Merged and synced: prove it live (`kubectl get deploy -n <ns>`, the new value in `kubectl describe pod`). Pending merge: read the current live value once (`kubectl describe pod` of the app) so the report states before and after; claim nothing as live.
2. Report in five lines: what changed, on which branch, what is live, what was assumed, what is left (the merge).

## Output
The report.

## Completion
Proceed to `05-learn`; the run is not done before it.
