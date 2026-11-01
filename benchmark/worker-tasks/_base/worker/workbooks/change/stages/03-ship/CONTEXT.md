# Stage 03: ship

## Input
The edit block and the diff.

## Process
1. Commit on the branch: conventional commit (`fix:`/`feat:`/`chore:` with the app as scope), one concern, no attribution lines.
2. Push the branch: `git -C repos/<repo> push -u origin change/<slug>`. A branch is not a deployment; this needs no go-ahead.
3. **Merging to `main` is the deployment** (ArgoCD auto-syncs `main`). Never push to or merge into `main` of a gitops repo yourself: the merge request is opened and merged by a human. Unattended: stop after the branch push.

## Output
```
shipped: <commit sha> on change/<slug>, pushed to origin
deploy: pending merge to main by a human
```

## Completion
Proceed to `04-verify-record`.
