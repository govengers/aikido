package invoice

import "math"

// Money is an amount in cents.
type Money int64

// LineItem is one billed position.
type LineItem struct {
	Description string
	Quantity    int
	UnitPrice   Money
}

// CalcTotal returns the gross total of the items: the sum of quantity times
// unit price, plus tax at taxRate (0.19 for 19 %), rounded to whole cents.
func CalcTotal(items []LineItem, taxRate float64) Money {
	var net Money
	for _, it := range items {
		net += Money(it.Quantity) * it.UnitPrice
	}
	tax := math.Round(float64(net) * taxRate)
	return net + Money(tax)
}
