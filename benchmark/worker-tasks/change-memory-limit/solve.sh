#!/usr/bin/env bash
# Reference run: branch, edit the one value, push the branch, record; main stays a human's merge.
source "$TASK_DIR/../lib.sh"

repo=repos/shop-gitops
branch=change/shop-api-memory-staging
git -C "$repo" fetch -q
git -C "$repo" switch -q -c "$branch" origin/main
perl -0pi -e 's/(  limits:\n    cpu: 500m\n    memory: )256Mi/${1}512Mi/' "$repo/apps/shop-api/values-staging.yaml"
git -C "$repo" commit -q -am "chore(shop-api): staging memory limit 256Mi -> 512Mi"
git -C "$repo" push -q -u origin "$branch"
kubectl describe pod -n shop-staging -l app=shop-api | grep 'memory:  256Mi' >/dev/null
sha=$(git -C "$repo" rev-parse --short HEAD)

run_day=$(date -u +%d.%m.%Y)
report="journal/runs/$(date -u +%F)-change-shop-api-memory-staging.md"
journal="journal/$(date -u +%Y-%m).md"
cat >"$report" <<EOF
---
date: $run_day
workbook: change
component: acme/shop
outcome: partial
duration: 5 min
---

## Task
shop-api staging memory limit 256Mi -> 512Mi in repos/shop-gitops apps/shop-api/values-staging.yaml, commit $sha on $branch, pushed; live is still 256Mi until a human merges to main.

## What went wrong on first try
- none

## Instructions that were wrong or missing
- none

## Missing access
- none

## Tool / environment friction
- none

## Suggested improvements
- none
EOF
echo "- $run_day acme/shop: change shop-api-memory-staging - partial; limit 512Mi on $branch ($sha), merge pending; run report $report" >>"$journal"
finish_worker "docs(journal): change shop-api-memory-staging" "$report" "$journal"
