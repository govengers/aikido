#!/usr/bin/env bash
set -euo pipefail
total=0
find pkg -type f -name '*.go' | LC_ALL=C sort | while read -r f; do
	echo "$f: $(wc -l <"$f" | tr -d ' ')"
done >LINES.md
total=$(find pkg -type f -name '*.go' -exec cat {} + | wc -l | tr -d ' ')
echo "total: $total" >>LINES.md
