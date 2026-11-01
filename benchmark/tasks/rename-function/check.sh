#!/usr/bin/env bash
source "$TASK_DIR/../lib.sh"

no_match 'CalcTotal([^W]|$)' --include='*.go' .
grep -q 'func ComputeInvoiceTotal(items \[\]LineItem, taxRate float64) Money' invoice/total.go || fail "ComputeInvoiceTotal not defined in invoice/total.go"
grep -q '^// ComputeInvoiceTotal ' invoice/total.go || fail "doc comment not updated"
grep -q 'func CalcTotalWeight(' shipping/weight.go || fail "shipping.CalcTotalWeight was renamed"
grep -q 'shipping.CalcTotalWeight(' cmd/billing/main.go || fail "call of shipping.CalcTotalWeight changed"
hidden_tests
go_green
echo PASS
