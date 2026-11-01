# Stage 01: triage

## Input
The alert or the human's description. Possibly nothing more than "X is down".

## Process
1. Identify the component from `infra/README.md` (cluster, namespace). If the symptom names a service, find it in the component's `inventory.md`.
2. Read the component README: namespaces, gotchas.
3. Establish the facts with read-only calls only: pod status in the namespace, restarts, last events. Three commands, not thirty.
4. Classify: outage / degraded / single user / cosmetic; production or not; how many services share the cause.
5. Check `## gotchas` in the README and `grep -ri <keyword> journal/` for the same signature (the error message, the app name). A known pattern with a documented fix skips to stage 03 with that fix as the proposal; reference the earlier run report.

## Output
```
component: <org>/<component>, ns <namespace>
symptom: <one line>   since: <dd.MM.yyyy HH:mm or "unknown">
impact: <outage|degraded|single-user|cosmetic>, prod: <yes|no>, scope: <n> services
known pattern: <journal/runs/<file> or none>
```

## Completion
Proceed to `02-diagnose`, or to `03-fix` for a known pattern.
