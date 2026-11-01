#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

no_mutation
used_tool kubectl '"(describe|logs)"'
remote_unchanged shop-gitops
remote_unchanged billing-gitops
pushed
no_stray_file
check_report incident acme/shop
only_paths "$REPORT|journal/$YEAR-$MONTH\.md|infra/acme/shop/README\.md|TODO\.md"
report_mentions 'OOM ?-?kill' 'the OOMKilled termination'
report_mentions '256 ?Mi' 'the 256Mi memory limit'
report_mentions '2\.14\.0|cart.cache' 'the trigger (shop-api 2.14.0, in-memory cart cache)'
journal_line shop
echo PASS
