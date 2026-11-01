#!/usr/bin/env bash
# Reference run: triage and diagnose read-only, propose, report, journal, push.
source "$TASK_DIR/../lib.sh"

kubectl get pods -n shop-prod -l app=shop-api >/dev/null
kubectl describe pod shop-api-6f8b9c7d5-k2x9p -n shop-prod | grep OOMKilled >/dev/null
kubectl logs shop-api-6f8b9c7d5-k2x9p -n shop-prod --previous | grep heap_alloc >/dev/null
logcli query '{namespace="shop-prod", app="shop-api"} |= "memstats"' --since 2h --limit 20 >/dev/null 2>&1
git -C repos/shop-gitops fetch -q
git -C repos/shop-gitops log -1 --format=%s origin/main | grep 2.14.0 >/dev/null

run_day=$(date -u +%d.%m.%Y)
report="journal/runs/$(date -u +%F)-incident-shop-api-oomkilled.md"
journal="journal/$(date -u +%Y-%m).md"
cat >"$report" <<EOF
---
date: $run_day
workbook: incident
component: acme/shop
outcome: partial
duration: 15 min
---

## Task
shop-api prod CrashLoopBackOff since 01.11.2026 06:40. Root cause: shop-api 2.14.0 (shop-gitops, pushed 06:31) turned on the in-memory cart cache without a size bound; the heap grows past the 256Mi memory limit within a minute and the container is OOMKilled (exit 137). Proposed only (unattended).

## What went wrong on first try
- none

## Instructions that were wrong or missing
- none

## Missing access
- none

## Tool / environment friction
- none

## Suggested improvements
- proposal: revert apps/shop-api/values-prod.yaml to 2.13.4 with CART_CACHE_MODE=redis, or set CART_CACHE_MAX_ENTRIES and raise the limit; needs a yes.
- alert: shop-api heap_alloc above 80 % of the memory limit.
EOF
echo "- $run_day acme/shop: incident shop-api-oomkilled - partial; 2.14.0 cart cache OOMKilled at 256Mi, revert proposed; run report $report" >>"$journal"
finish_worker "docs(journal): incident shop-api-oomkilled" "$report" "$journal"
