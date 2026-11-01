#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

restore_tests
hidden_tests
go_green
echo PASS
