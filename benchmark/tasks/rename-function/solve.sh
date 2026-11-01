#!/usr/bin/env bash
# Reference solution: run in a fresh fixture copy with TASK_DIR set.
set -euo pipefail
grep -rlE 'CalcTotal([^W]|$)' --include='*.go' . | while read -r f; do
	perl -pi -e 's/CalcTotal(?!Weight)/ComputeInvoiceTotal/g' "$f"
done
