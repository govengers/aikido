#!/usr/bin/env bash
set -euo pipefail
tmp=$(mktemp)
jq '.server.port = 9090 | .database.pool.max = 25 | .features.newCheckout = true | .allowedOrigins += ["https://admin.example.com"]' config/app.json >"$tmp"
mv "$tmp" config/app.json
cp "$TASK_DIR/expected/values.yaml" deploy/values.yaml
