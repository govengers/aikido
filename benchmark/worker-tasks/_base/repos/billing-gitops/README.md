# billing-gitops

ArgoCD source of acme billing on cluster eu1. ArgoCD auto-syncs `main`.

Layout: `apps/<app>/Chart.yaml`, optional `apps/<app>/values.yaml` (shared defaults), `apps/<app>/values-<stage>.yaml` per stage the app runs in (`staging`, `prod`). The target namespace is the `namespace:` key of each stage file; the prod database deliberately runs in `billing-data`.

Changes: branch `change/<slug>`, merge request, a human merges.
