package report

import (
	"testing"

	"example.com/billing/invoice"
)

func TestSummary(t *testing.T) {
	invs := []invoice.Invoice{{
		Number:   "INV-1",
		Customer: "acme",
		Items:    []invoice.LineItem{{Quantity: 2, UnitPrice: 500}},
		TaxRate:  0.1,
	}}
	if got, want := Summary(invs), "INV-1 acme 1100\n"; got != want {
		t.Fatalf("Summary = %q, want %q", got, want)
	}
}
