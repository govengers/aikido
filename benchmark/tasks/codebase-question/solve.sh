#!/usr/bin/env bash
set -euo pipefail
printf 'timeout: 7s\nenv: GATEWAY_HTTP_READ_TIMEOUT\nfile: internal/config/defaults.go\n' >ANSWER.md
