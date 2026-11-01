#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

src=$(find accounts -name '*.go' ! -name '*_test.go')
# The validation exists exactly once in production code.
for msg in 'email is missing @' 'email is too long' 'has no dot' 'is blocked'; do
	n=$(cat $src | grep -cF "$msg" || true)
	[ "$n" -eq 1 ] || fail "\"$msg\" appears $n times in production code, want 1"
done
grep -qE '^func normalizeEmail\(raw string\) \(string, error\)' $src || fail "normalizeEmail with the required signature not found"
calls=$(cat $src | grep -v '^func normalizeEmail' | grep -c 'normalizeEmail(' || true)
[ "$calls" -ge 2 ] || fail "normalizeEmail is called $calls times, want both use cases to call it"
restore_tests
hidden_tests
go_green
echo PASS
