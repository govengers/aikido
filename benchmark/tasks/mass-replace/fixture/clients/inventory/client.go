package inventory

import "net/http"

// BaseURL is the API endpoint the inventory client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/inventory"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the inventory API.
type Client struct {
	HTTP *http.Client
}
