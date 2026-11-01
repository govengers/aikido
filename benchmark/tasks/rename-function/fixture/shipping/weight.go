package shipping

// Parcel is one package handed to the carrier.
type Parcel struct {
	WeightGrams int
	Count       int
}

// CalcTotalWeight returns the combined weight of all parcels in grams.
// It is unrelated to invoice totals.
func CalcTotalWeight(parcels []Parcel) int {
	total := 0
	for _, p := range parcels {
		total += p.WeightGrams * p.Count
	}
	return total
}
