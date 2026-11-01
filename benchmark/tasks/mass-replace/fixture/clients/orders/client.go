package orders

import "net/http"

// BaseURL is the API endpoint the orders client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/orders"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the orders API.
type Client struct {
	HTTP *http.Client
}
