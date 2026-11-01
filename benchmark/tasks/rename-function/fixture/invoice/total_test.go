package invoice

import "testing"

func TestCalcTotal(t *testing.T) {
	items := []LineItem{
		{Description: "widget", Quantity: 3, UnitPrice: 1000},
		{Description: "gadget", Quantity: 1, UnitPrice: 2550},
	}
	if got := CalcTotal(items, 0.19); got != 6605 {
		t.Fatalf("CalcTotal = %d, want 6605", got)
	}
}

func TestCalcTotalEmpty(t *testing.T) {
	if got := CalcTotal(nil, 0.19); got != 0 {
		t.Fatalf("CalcTotal(nil) = %d, want 0", got)
	}
}
