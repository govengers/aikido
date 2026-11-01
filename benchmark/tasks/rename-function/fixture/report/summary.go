package report

import (
	"fmt"
	"strings"

	"example.com/billing/invoice"
)

// Summary renders one line per invoice with its recomputed total.
// Totals are recomputed via invoice.CalcTotal so stale stored totals show up.
func Summary(invoices []invoice.Invoice) string {
	var b strings.Builder
	for _, inv := range invoices {
		total := invoice.CalcTotal(inv.Items, inv.TaxRate)
		fmt.Fprintf(&b, "%s %s %d\n", inv.Number, inv.Customer, total)
	}
	return b.String()
}
