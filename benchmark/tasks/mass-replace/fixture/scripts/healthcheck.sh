#!/usr/bin/env bash
set -euo pipefail

API="https://api.acme-legacy.io"
curl -fsS "https://api.acme-legacy.io/v2/ping?run=0" >/dev/null
curl -fsS "https://api.acme-legacy.io/v2/ping?run=1" >/dev/null
curl -fsS "https://api.acme-legacy.io/v2/ping?run=2" >/dev/null
