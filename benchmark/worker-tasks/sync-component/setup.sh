#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"
worker_setup

# A colleague pushed after the worker's clone: the local checkout is one commit behind origin/main.
colleague_change() {
	perl -pi -e 's/^version: 1\.8\.0$/version: 1.9.0/' apps/invoice-api/Chart.yaml
	mkdir -p apps/dunning-mailer
	printf 'apiVersion: v2\nname: dunning-mailer\ndescription: payment reminder mails\ntype: application\nversion: 0.1.0\nappVersion: "0.1.0"\n' >apps/dunning-mailer/Chart.yaml
	printf '# dunning-mailer staging: prod after the legal review\nnamespace: billing-staging\nreplicas: 1\n' >apps/dunning-mailer/values-staging.yaml
	printf -- '- dunning-mailer: add a prod stage after the legal review\n' >>TODO.md
}
remote_commit billing-gitops "2026-10-30T16:20:00+0000" "feat: invoice-api 1.9.0, dunning-mailer on staging" colleague_change

# Someone's uncommitted work in the local checkout: must not leak into the inventory.
perl -pi -e 's/^version: 0\.9\.4$/version: 0.10.0-rc1/' "$WORK/repos/billing-gitops/apps/billing-db/Chart.yaml"
record_bases
