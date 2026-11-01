#!/usr/bin/env bash
set -euo pipefail

API="https://status.acme-legacy.io"
curl -fsS "https://api.acme-legacy.io/v2/ping?run=0" >/dev/null
