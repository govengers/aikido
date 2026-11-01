#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

no_mutation
remote_unchanged billing-gitops
remote_unchanged shop-gitops
pushed
no_stray_file
sha=$(remote_git billing-gitops rev-parse --short=7 main)
actual=$(remote_git worker show main:infra/acme/billing/inventory.md)
diff -u <(sed "s/@SHA7@/$sha/" "$TASK_DIR/expected/inventory.md" | normalize_markdown) <(echo "$actual" | normalize_markdown) ||
	fail "infra/acme/billing/inventory.md differs from origin/main of billing-gitops in the format of sync-component stage 03"
check_report sync-component acme/billing
only_paths "$REPORT|journal/$YEAR-$MONTH\.md|infra/acme/billing/(inventory|README)\.md|infra/README\.md|TODO\.md"
journal_line billing
echo PASS
