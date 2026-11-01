#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

cmp -s SPEC.md "$FIXTURE/SPEC.md" || fail "SPEC.md was modified"
restore_tests
hidden_tests
go_green

bin=$(mktemp -d)
trap 'rm -rf "$bin"' EXIT
go build -o "$bin/wordstat" ./cmd/wordstat || fail "build cmd/wordstat"
printf 'b a c b a b d e f g h i j k l m n o p q r s\nB\n' >"$bin/in.txt"

[ "$("$bin/wordstat" count "$bin/in.txt")" = "23" ] || fail "count subcommand changed"
got=$("$bin/wordstat" top -n 2 "$bin/in.txt") || fail "top -n 2 exited non-zero"
[ "$got" = $'b 4\na 2' ] || fail "top -n 2 printed: $got"
got=$("$bin/wordstat" top "$bin/in.txt") || fail "top exited non-zero"
want=$'b 4\na 2\nc 1\nd 1\ne 1\nf 1\ng 1\nh 1\ni 1\nj 1'
[ "$got" = "$want" ] || fail "top without -n printed: $got"

# README: ### top after ### count, inside ## Commands, with the usage line.
awk '
	/^## / { section = $0 }
	/^### count/ { count = NR }
	/^### top/ { top = NR; topsec = section }
	END { exit !(count && top > count && topsec == "## Commands") }
' README.md || fail "README.md: ### top section missing or misplaced"
grep -qF 'wordstat top [-n N] <file>' README.md || fail "README.md: usage line missing"
grep -qF 'wordstat count <file>' README.md || fail "README.md: count usage removed"
echo PASS
