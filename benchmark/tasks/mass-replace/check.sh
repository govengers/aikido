#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

expected=$(mktemp -d)
trap 'rm -rf "$expected"' EXIT
cp -R "$FIXTURE/." "$expected"
(cd "$expected" && find . -type f | while read -r f; do
	sed 's/api\.acme-legacy\.io/api.acme.dev/g' "$f" >"$f.new" && mv "$f.new" "$f"
done)
diff -r "$expected" . || fail "tree differs from the expected replacement"
echo PASS
