package auth

import "net/http"

// BaseURL is the API endpoint the auth client talks to.
const BaseURL = "https://api.acme-legacy.io/v2/auth"

// StatusPage is shown to users when the API is down.
const StatusPage = "https://status.acme-legacy.io"

// Client calls the auth API.
type Client struct {
	HTTP *http.Client
}
