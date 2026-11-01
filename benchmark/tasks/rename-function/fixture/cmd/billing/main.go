package main

import (
	"fmt"
	"net/http"
	"os"

	"example.com/billing/api"
	"example.com/billing/invoice"
	"example.com/billing/shipping"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "demo" {
		items := []invoice.LineItem{{Description: "demo", Quantity: 1, UnitPrice: 999}}
		fmt.Println("total:", invoice.CalcTotal(items, 0.19))
		fmt.Println("weight:", shipping.CalcTotalWeight([]shipping.Parcel{{WeightGrams: 250, Count: 2}}))
		return
	}
	http.HandleFunc("/quote", api.QuoteHandler)
	_ = http.ListenAndServe(":8080", nil)
}
