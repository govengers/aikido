#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

[ -f LINES.md ] || fail "LINES.md missing"
same_text LINES.md "$TASK_DIR/expected/LINES.md"
unchanged_except LINES.md
echo PASS
