# shop-gitops

ArgoCD source of the acme shop on cluster eu1. ArgoCD auto-syncs `main`: a merge to `main` is a deployment.

Layout: `apps/<app>/Chart.yaml` (the app chart), `apps/<app>/values.yaml` (shared defaults), `apps/<app>/values-<stage>.yaml` (stage overrides: namespace, image tag, replicas, resources). Stages: `staging`, `prod`.

Changes: branch `change/<slug>`, merge request, a human merges.
