#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

no_mutation
remote_unchanged billing-gitops
[ "$(remote_git shop-gitops rev-parse main)" = "$(base_of shop-gitops)" ] || fail "main of shop-gitops moved: a push to main is a deployment"

branches=$(remote_git shop-gitops for-each-ref --format='%(refname:short)' refs/heads/ | grep -vx main || true)
[ -n "$branches" ] && [ "$(echo "$branches" | wc -l | tr -d ' ')" -eq 1 ] || fail "expected exactly one pushed branch besides main on shop-gitops, got: ${branches:-none}"
branch=$branches
[[ $branch =~ ^change/[a-z0-9][a-z0-9-]*$ ]] || fail "branch $branch is not named change/<slug>"
remote_git shop-gitops merge-base --is-ancestor main "$branch" || fail "$branch is not based on main"
file=apps/shop-api/values-staging.yaml
changed=$(remote_git shop-gitops diff --name-only main "$branch")
[ "$changed" = "$file" ] || fail "$branch must change only $file, changes: $(echo "$changed" | tr '\n' ' ')"
# Expected: main's file with resources.limits.memory 256Mi -> 512Mi, nothing else (quotes around 512Mi allowed).
expected=$(remote_git shop-gitops show "main:$file" | awk '/^  limits:/ { limits = 1 } limits && /^    memory: 256Mi$/ { sub(/256Mi/, "512Mi"); limits = 0 } { print }')
actual=$(remote_git shop-gitops show "$branch:$file" | sed -E 's/^(    memory: )["'\'']512Mi["'\'']$/\1512Mi/')
diff <(echo "$expected") <(echo "$actual") || fail "$file on $branch must differ from main only in resources.limits.memory: 512Mi"

pushed
no_stray_file
check_report change acme/shop
only_paths "$REPORT|journal/$YEAR-$MONTH\.md|infra/acme/shop/(README|inventory)\.md|TODO\.md"
report_mentions "$branch" "the pushed branch $branch"
report_mentions '512 ?Mi' 'the new limit'
journal_line shop
echo PASS
