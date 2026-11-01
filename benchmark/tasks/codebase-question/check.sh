#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

[ -f ANSWER.md ] || fail "ANSWER.md missing"
# Tolerate backticks, surrounding blanks and a leading ./ but nothing else.
got=$(normalize ANSWER.md | tr -d '`' | sed -e 's/^[[:space:]]*//' -e 's#: \./#: #')
want=$'timeout: 7s\nenv: GATEWAY_HTTP_READ_TIMEOUT\nfile: internal/config/defaults.go'
[ "$got" = "$want" ] || { printf 'got:\n%s\n' "$got"; fail "ANSWER.md is wrong"; }
unchanged_except ANSWER.md
echo PASS
