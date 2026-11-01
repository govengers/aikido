# Stage 02: diagnose

## Input
The triage block from stage 01.

## Process
1. **Collect.** For the affected app: `kubectl describe pod` (last state, exit code, limits), `kubectl logs --previous` of a crashing container, `kubectl get events`, the app's Loki logs around the first occurrence (`logcli query ... --since <window>`).
2. **What changed first.** In this order, stop at the first hit: a commit in the component's gitops repo inside the window (`git -C repos/<repo> log --since ...`), a new ReplicaSet / image tag, a platform event. The earliest observation is the cause side; later ones are symptoms.
3. **Rule out the classics** before anything exotic: image pull, expired cert, secret missing, OOM (exit code 137, `OOMKilled`), failing dependency.
4. Form one root-cause hypothesis with a confidence (high / medium / low) and the single observation that would falsify it.

## Output
```
changed: <what, when>   or "nothing found in 48h"
evidence: <2-4 lines, timestamps>
root cause: <one sentence>, confidence <high|medium|low>
falsifier: <what would prove this wrong>
fix options: <a> git change in <repo path>, <b> revert <commit>, <c> operational (restart/rollback)
```

## Completion
Hand the block to `03-fix`.
