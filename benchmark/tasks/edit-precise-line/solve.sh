#!/usr/bin/env bash
set -euo pipefail
perl -pi -e 's/(SKU: "SKU-02417".*PriceCents: )4599/${1}4999/' catalog/products.go
