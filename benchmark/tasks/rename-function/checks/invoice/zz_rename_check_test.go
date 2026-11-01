package invoice

import "testing"

func TestBenchRenamedTotal(t *testing.T) {
	items := []LineItem{{Quantity: 4, UnitPrice: 250}}
	if got := ComputeInvoiceTotal(items, 0.07); got != 1070 {
		t.Fatalf("ComputeInvoiceTotal = %d, want 1070", got)
	}
}
