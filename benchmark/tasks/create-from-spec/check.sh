#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

[ -f ringbuffer/ringbuffer.go ] || fail "ringbuffer/ringbuffer.go missing"
grep -q '^package ringbuffer' ringbuffer/ringbuffer.go || fail "wrong package clause"
restore_tests
hidden_tests
go_green
echo PASS
