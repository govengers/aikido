package api

import (
	"encoding/json"
	"net/http"

	"example.com/billing/invoice"
)

type quoteRequest struct {
	Items   []invoice.LineItem `json:"items"`
	TaxRate float64            `json:"taxRate"`
}

type quoteResponse struct {
	Total invoice.Money `json:"total"`
}

// QuoteHandler returns the total for a basket without creating an invoice.
func QuoteHandler(w http.ResponseWriter, r *http.Request) {
	var req quoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	resp := quoteResponse{Total: invoice.CalcTotal(req.Items, req.TaxRate)}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
