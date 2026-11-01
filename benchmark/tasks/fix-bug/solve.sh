#!/usr/bin/env bash
set -euo pipefail
perl -pi -e 's/for p := lo; p < hi; p\+\+/for p := lo; p <= hi; p++/' pages/ranges.go
