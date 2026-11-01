package reviews

import "net/http"

// BaseURL is the API endpoint the reviews client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/reviews"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the reviews API.
type Client struct {
	HTTP *http.Client
}
