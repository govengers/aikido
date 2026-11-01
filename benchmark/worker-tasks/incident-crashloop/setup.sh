#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"
worker_setup
# The deploy that broke prod: a colleague's push at 06:31, not yet in the worker's clone.
remote_commit shop-gitops "2026-11-01T06:31:00+0000" "feat(shop-api): 2.14.0 with in-memory cart cache in prod" \
	"perl -pi -e 's/tag: \"2.13.4\"/tag: \"2.14.0\"/; s/CART_CACHE_MODE: redis/CART_CACHE_MODE: memory\n  CART_CACHE_MAX_ENTRIES: \"0\"/' apps/shop-api/values-prod.yaml"
record_bases
