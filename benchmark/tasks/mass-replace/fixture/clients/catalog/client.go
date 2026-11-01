package catalog

import "net/http"

// BaseURL is the API endpoint the catalog client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/catalog"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the catalog API.
type Client struct {
	HTTP *http.Client
}
