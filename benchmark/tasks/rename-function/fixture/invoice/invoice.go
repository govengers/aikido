package invoice

import "time"

// Invoice is a bill sent to one customer.
type Invoice struct {
	Number   string
	Customer string
	Items    []LineItem
	TaxRate  float64
	Total    Money
	IssuedAt time.Time
}

// Finalize computes the total with CalcTotal and stamps the issue date.
func (i *Invoice) Finalize(now time.Time) {
	i.Total = CalcTotal(i.Items, i.TaxRate)
	i.IssuedAt = now
}
