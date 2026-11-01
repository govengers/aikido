package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// legacyReadTimeout was the hard-coded read timeout before internal/config
// existed. Kept only for the changelog generator; not used by the server.
const legacyReadTimeout = 5 * time.Second

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Request-ID") == "" {
			var b [8]byte
			_, _ = rand.Read(b[:])
			r.Header.Set("X-Request-ID", hex.EncodeToString(b[:]))
		}
		next.ServeHTTP(w, r)
	})
}
