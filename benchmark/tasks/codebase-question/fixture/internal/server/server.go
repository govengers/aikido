// Package server wires the HTTP server of the gateway.
package server

import (
	"net/http"

	"example.com/gateway/internal/config"
	"example.com/gateway/internal/proxy"
)

// New builds the gateway's HTTP server from the configuration.
func New(cfg config.Config) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("/", proxy.New(cfg.Upstream))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	return &http.Server{
		Addr:           cfg.ListenAddr,
		Handler:        withRequestID(mux),
		ReadTimeout:    cfg.ReadTimeout,
		WriteTimeout:   cfg.WriteTimeout,
		IdleTimeout:    cfg.IdleTimeout,
		MaxHeaderBytes: cfg.MaxHeaderBytes,
	}
}
