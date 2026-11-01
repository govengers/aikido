package catalog

// Promotion is a temporary price reduction for one SKU.
type Promotion struct {
	SKU                string
	OriginalPriceCents int
	PromoPriceCents    int
}

// Promotions currently running. OriginalPriceCents is the price at the
// start of the promotion and must not follow later catalog changes.
var Promotions = []Promotion{
	{SKU: "SKU-02417", OriginalPriceCents: 4599, PromoPriceCents: 3999},
	{SKU: "SKU-02471", OriginalPriceCents: 4599, PromoPriceCents: 3899},
	{SKU: "SKU-00042", OriginalPriceCents: 1999, PromoPriceCents: 1499},
}
