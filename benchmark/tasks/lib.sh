# Shared helpers for check.sh and solve.sh. Sourced, never executed.
# The runner calls check.sh with the result directory as working directory and
# TASK_DIR set to the absolute task directory. Checks look only at files.
set -euo pipefail

: "${TASK_DIR:?TASK_DIR must be set}"
FIXTURE="$TASK_DIR/fixture"
export GOWORK=off GOTOOLCHAIN=local GOFLAGS=-count=1

fail() {
	echo "FAIL: $*"
	exit 1
}

# restore_tests copies every *_test.go of the fixture back, so an agent cannot
# pass a test task by editing the tests.
restore_tests() {
	(cd "$FIXTURE" && find . -name '*_test.go') | while read -r f; do
		mkdir -p "$(dirname "$f")"
		cp "$FIXTURE/$f" "$f"
	done
}

# hidden_tests copies the task's checks/<pkg>/... files into the result tree.
hidden_tests() {
	cp -R "$TASK_DIR/checks/." .
}

go_green() {
	go build ./... || fail "go build"
	go vet ./... || fail "go vet"
	go test ./... || fail "go test"
}

# normalize strips trailing whitespace, squeezes blank-line runs and drops
# trailing blank lines, so text compares ignore editor noise only.
normalize() {
	awk '{ sub(/[ \t\r]+$/, ""); line[NR] = $0 } END { n = NR; while (n > 0 && line[n] == "") n--; blank = 0; for (i = 1; i <= n; i++) { if (line[i] == "") { if (blank) continue; blank = 1 } else blank = 0; print line[i] } }' "$1"
}

same_text() { # same_text <actual> <expected>
	[ -f "$1" ] || fail "$1 missing"
	diff -u <(normalize "$2") <(normalize "$1") || fail "$1 differs from the expected content"
}

# unchanged_except <paths...>: the result tree equals the fixture apart from
# the listed paths (no other file added, removed or modified).
unchanged_except() {
	local out
	out=$(diff -rq "$FIXTURE" . 2>&1 | grep -v -F -e "$(printf '%s\n' "$@")" || true)
	[ -z "$out" ] || fail "unexpected changes: $out"
}

# no_match <ERE> <grep-args...>: fails when the pattern occurs.
no_match() {
	local pat=$1
	shift
	if grep -rnE "$pat" "$@"; then fail "pattern /$pat/ still present"; fi
}
