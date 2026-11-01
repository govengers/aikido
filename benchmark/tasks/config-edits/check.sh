#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

# JSON is compared semantically (formatting is free), YAML textually because
# the task requires comments and formatting to survive.
jq -e . config/app.json >/dev/null || fail "config/app.json is not valid JSON"
want=$(jq -S '.server.port = 9090 | .database.pool.max = 25 | .features.newCheckout = true | .allowedOrigins += ["https://admin.example.com"]' "$FIXTURE/config/app.json")
got=$(jq -S . config/app.json)
diff <(echo "$want") <(echo "$got") || fail "config/app.json content differs"
same_text deploy/values.yaml "$TASK_DIR/expected/values.yaml"
unchanged_except config/app.json deploy/values.yaml
echo PASS
