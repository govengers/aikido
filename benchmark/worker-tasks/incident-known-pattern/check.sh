#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

no_mutation
used_tool '(kubectl|logcli)' '.'
remote_unchanged billing-gitops
remote_unchanged shop-gitops
pushed
no_stray_file
check_report incident acme/billing
only_paths "$REPORT|journal/$YEAR-$MONTH\.md|infra/acme/billing/README\.md|TODO\.md"
report_mentions '2026-09-03-incident-billing-ledger-sync-hostkey' 'the earlier run report with the same signature'
report_mentions 'hostKey' 'the documented fix (replace sftp.hostKey in values-prod.yaml)'
journal_line billing
echo PASS
