#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

no_match '\b(formatLegacySubject|dialLegacyGateway|legacySMSGateway|renderTemplateV1|escapeV1)\b' --include='*.go' .
for kept in 'func SendEmail(' 'func SendSMS(' 'func renderTemplate(' 'func Welcome(' 'const maxSMSLength' 'type Transport interface'; do
	grep -rqF "$kept" notify || fail "still-used code removed: $kept"
done
restore_tests
go_green
echo PASS
