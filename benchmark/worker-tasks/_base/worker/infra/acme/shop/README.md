# shop (acme)

The acme web shop: `shop-web` (storefront), `shop-api` (cart, checkout, catalogue API, Go), `shop-worker` (order export). Business critical: checkout down = no revenue.

| | |
|---|---|
| Repo | `repos/shop-gitops`, branch `main`, push to `main` = deploy (ArgoCD auto-sync) |
| Cluster | `eu1` |
| Environments | staging → `shop-staging`, prod → `shop-prod` |
| Last sync | see `inventory.md` |

## kubernetes
- Namespaces: staging `shop-staging`, prod `shop-prod`. Pods carry the label `app=<app>`.
- The worker's service account is read-only (get, list, watch, logs).

## logs
- Loki streams `{namespace="shop-prod", app="shop-api"}`; shop-api logs a `memstats` line every 15 s (heap_alloc, num_gc).

## gitops
- Layout: `apps/<app>/Chart.yaml`, `apps/<app>/values.yaml` (shared defaults), `apps/<app>/values-<stage>.yaml` (per stage: image tag, resources, replicas).
- Resources (requests/limits) live in the stage files, never in `values.yaml`.
- Changes go on a branch `change/<slug>`, pushed to `origin`; a human opens and merges the merge request.

## sync
- App registry: `apps/`; stages from the `values-<stage>.yaml` files.

## gotchas
- 20.10.2026: shop-api 502s at checkout can also be the orders DB connection pool (see `journal/runs/2026-10-20-incident-shop-api-db-pool.md`), not only the API pods.
- 14.10.2026: shop-web caches catalogue responses for 5 min; a fixed shop-api error can stay visible that long.
