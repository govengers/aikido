#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

expected=$(mktemp)
trap 'rm -f "$expected"' EXIT
sed '/{SKU: "SKU-02417",/s/PriceCents: 4599,/PriceCents: 4999,/' "$FIXTURE/catalog/products.go" >"$expected"
cmp -s "$expected" catalog/products.go || { diff "$expected" catalog/products.go | head -20 || true; fail "catalog/products.go is not exactly the fixture with the one price changed"; }
unchanged_except catalog/products.go
echo PASS
