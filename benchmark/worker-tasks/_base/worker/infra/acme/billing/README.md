# billing (acme)

acme billing: `invoice-api` (invoices for the shop), `ledger-sync` (nightly CronJob exporting ledger entries to the bank over SFTP), `billing-db` (PostgreSQL). Prod: invoices and the bank export must run daily.

| | |
|---|---|
| Repo | `repos/billing-gitops`, branch `main`, push to `main` = deploy (ArgoCD auto-sync) |
| Cluster | `eu1` |
| Environments | staging → `billing-staging`, prod → `billing-prod`; the database runs in `billing-data` in prod |
| Last sync | see `inventory.md` |

## kubernetes
- Namespaces: `billing-staging`, `billing-prod`, `billing-data` (prod database only).
- `ledger-sync` is a CronJob (`0 2 * * *` UTC, `backoffLimit: 1`); its pods are named `ledger-sync-<run>-<suffix>`.

## logs
- Loki streams `{namespace="billing-prod", app="ledger-sync"}`.

## gitops
- Layout: `apps/<app>/Chart.yaml`, `apps/<app>/values-<stage>.yaml`.
- Changes go on a branch `change/<slug>`, pushed to `origin`; a human merges.

## sync
- App registry: `apps/`; stages from the `values-<stage>.yaml` files; the namespace is the `namespace:` key of each stage file.

## gotchas
- 02.10.2026: invoice-api staging uses the payment-gateway-mock; prod invoices never go through it.
