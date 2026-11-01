#!/usr/bin/env bash
# Reference run: triage, recognise the signature in the journal, propose the documented fix.
source "$TASK_DIR/../lib.sh"

kubectl get pods -n billing-prod -l app=ledger-sync >/dev/null
kubectl logs job/ledger-sync-29361600 -n billing-prod 2>/dev/null | grep 'key mismatch' >/dev/null
known=$(grep -rl 'key mismatch' journal/runs/)
[ "$known" = journal/runs/2026-09-03-incident-billing-ledger-sync-hostkey.md ]

run_day=$(date -u +%d.%m.%Y)
report="journal/runs/$(date -u +%F)-incident-billing-ledger-sync-hostkey.md"
journal="journal/$(date -u +%Y-%m).md"
cat >"$report" <<EOF
---
date: $run_day
workbook: incident
component: acme/billing
outcome: partial
duration: 10 min
---

## Task
ledger-sync prod run 29361600 (01.11.2026 02:00) failed with \`knownhosts: key mismatch for sftp.bank.example\`. Known pattern: $known (the bank rotated its SFTP host key). Proposed only (unattended).

## What went wrong on first try
- none

## Instructions that were wrong or missing
- none

## Missing access
- none

## Tool / environment friction
- none

## Suggested improvements
- proposal (documented fix): replace sftp.hostKey in repos/billing-gitops apps/ledger-sync/values-prod.yaml with the new key from https://bank.example/sftp-keys (fingerprint SHA256:Wq3pN0Ld5pXcY2rH8vKf9aZtE1uB6mJ4sQ7gD2oR8yU), branch change/ledger-sync-hostkey, merge, then rerun via kubectl create job --from=cronjob/ledger-sync; needs a yes.
EOF
echo "- $run_day acme/billing: incident ledger-sync-hostkey - partial; bank host key rotated again, documented fix proposed; run report $report" >>"$journal"
finish_worker "docs(journal): incident ledger-sync-hostkey" "$report" "$journal"
