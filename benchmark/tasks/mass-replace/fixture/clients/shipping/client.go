package shipping

import "net/http"

// BaseURL is the API endpoint the shipping client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/shipping"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the shipping API.
type Client struct {
	HTTP *http.Client
}
