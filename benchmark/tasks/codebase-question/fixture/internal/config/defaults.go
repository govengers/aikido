package config

import "time"

// Defaults used when the environment does not override a value.
const (
	defaultListenAddr      = ":8443"
	defaultReadTimeout     = 7 * time.Second
	defaultWriteTimeout    = 12 * time.Second
	defaultIdleTimeout     = 90 * time.Second
	defaultShutdownTimeout = 20 * time.Second
	defaultMaxHeaderBytes  = 1 << 16
)
