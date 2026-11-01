package users

import "net/http"

// BaseURL is the API endpoint the users client talks to.
const BaseURL = "https://docs.acme-legacy.io/v2/users"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the users API.
type Client struct {
	HTTP *http.Client
}
