package payments

import "net/http"

// BaseURL is the API endpoint the payments client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/payments"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the payments API.
type Client struct {
	HTTP *http.Client
}
