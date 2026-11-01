---
date: 03.09.2026
workbook: incident
component: acme/billing
outcome: done
duration: 50 min
---

## Task
Alert `LedgerSyncJobFailed` 03.09.2026 02:05: ledger-sync in billing-prod failed with `ssh: handshake failed: knownhosts: key mismatch for sftp.bank.example`. Root cause: the bank rotates the host key of its SFTP endpoint once a quarter (announced only on its key page `https://bank.example/sftp-keys`), and ledger-sync pins the old key in `sftp.hostKey`.

## What went wrong on first try
- Suspected our network policy first (egress to port 22) → `kubectl describe networkpolicy` showed nothing changed → the error text alone was the answer.

## Instructions that were wrong or missing
- infra/acme/billing/README.md: no note that the bank's host key is pinned in git (fixed now: no, kept here).

## Missing access
- none

## Tool / environment friction
- none

## Suggested improvements
- Documented fix (applied 03.09.2026 by a human after a yes): replace `sftp.hostKey` in `repos/billing-gitops` `apps/ledger-sync/values-prod.yaml` with the new key from `https://bank.example/sftp-keys` (its SHA256 fingerprint must equal the one in the error), on a branch `change/ledger-sync-hostkey`, merge, then rerun the night's export with `kubectl create job --from=cronjob/ledger-sync ledger-sync-rerun-<yyyymmdd> -n billing-prod` (mutation: needs a yes).
- alert: page on the first failed ledger-sync run, not after the backoff limit.
