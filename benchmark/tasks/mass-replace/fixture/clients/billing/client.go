package billing

import "net/http"

// BaseURL is the API endpoint the billing client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/billing"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the billing API.
type Client struct {
	HTTP *http.Client
}
