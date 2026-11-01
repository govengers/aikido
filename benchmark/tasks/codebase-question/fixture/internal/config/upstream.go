package config

import "time"

// UpstreamConfig tunes the HTTP client that talks to backend services.
type UpstreamConfig struct {
	BaseURL     string
	ReadTimeout time.Duration
	Retries     int
}

func loadUpstream() UpstreamConfig {
	return UpstreamConfig{
		BaseURL:     str("UPSTREAM_URL", "http://backend.internal"),
		ReadTimeout: duration("UPSTREAM_READ_TIMEOUT", 30*time.Second),
		Retries:     integer("UPSTREAM_RETRIES", 2),
	}
}
