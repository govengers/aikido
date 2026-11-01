---
date: 20.10.2026
workbook: incident
component: acme/shop
outcome: done
duration: 35 min
---

## Task
shop-web 502 at checkout 20.10.2026 11:10-11:40. Root cause: shop-api exhausted its orders DB connection pool (`pq: sorry, too many clients already`) during a marketing push; pods stayed Running, no restarts. Fixed by a human: `db.maxOpenConns` 20 → 40 in `apps/shop-api/values-prod.yaml`.

## What went wrong on first try
- Looked for restarts first → none; the error was only in the shop-api logs (`logcli query '{namespace="shop-prod", app="shop-api"} |= "too many clients"'`).

## Instructions that were wrong or missing
- infra/acme/shop/README.md#gotchas: DB pool not mentioned → added (fixed now: yes).

## Missing access
- none

## Tool / environment friction
- none

## Suggested improvements
- alert: shop-api `db_pool_wait_seconds` p95 > 1 s.
