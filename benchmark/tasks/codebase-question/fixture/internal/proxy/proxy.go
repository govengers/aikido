// Package proxy forwards requests to the upstream services.
package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"

	"example.com/gateway/internal/config"
)

// New returns a reverse proxy to the configured upstream.
func New(cfg config.UpstreamConfig) http.Handler {
	target, err := url.Parse(cfg.BaseURL)
	if err != nil {
		panic(err)
	}
	p := httputil.NewSingleHostReverseProxy(target)
	p.Transport = &http.Transport{
		ResponseHeaderTimeout: cfg.ReadTimeout,
		MaxIdleConnsPerHost:   32,
	}
	return p
}
