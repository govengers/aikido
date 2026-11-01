# Components

One row per component: a thing with its own gitops repo. Repo = the checkout path in this worker (gitignored, remote `origin`); ArgoCD auto-syncs `main` of every gitops repo.

| Org | Component | Purpose | Criticality | Repo | Cluster | Namespaces | Last sync |
|-----|-----------|---------|-------------|------|---------|------------|-----------|
| acme | [shop](acme/shop/README.md) | acme web shop: storefront, API, order worker | prod, business critical | `repos/shop-gitops` | eu1 | `shop-staging`, `shop-prod` | 14.10.2026 |
| acme | [billing](acme/billing/README.md) | invoicing, ledger export to the bank, billing database | prod | `repos/billing-gitops` | eu1 | `billing-staging`, `billing-prod`, `billing-data` | 01.10.2026 |

Adding a component: create `infra/<org>/<component>/README.md` with the sections of an existing one, add the checkout under `repos/`, run the `sync-component` workbook, add the row here.
