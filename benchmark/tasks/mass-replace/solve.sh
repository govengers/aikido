#!/usr/bin/env bash
set -euo pipefail
grep -rl 'api.acme-legacy.io' . | xargs perl -pi -e 's/\bapi\.acme-legacy\.io\b/api.acme.dev/g'
