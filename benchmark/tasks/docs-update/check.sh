#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

same_text docs/configuration.md "$TASK_DIR/expected/configuration.md"
unchanged_except docs/configuration.md
echo PASS
