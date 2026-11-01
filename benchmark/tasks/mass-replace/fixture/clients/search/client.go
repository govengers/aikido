package search

import "net/http"

// BaseURL is the API endpoint the search client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/search"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the search API.
type Client struct {
	HTTP *http.Client
}
