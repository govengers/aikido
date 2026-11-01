// Package config loads the gateway configuration from the environment.
package config

import (
	"os"
	"strconv"
	"time"
)

// envPrefix namespaces every variable the gateway reads.
const envPrefix = "GATEWAY_"

// Config is the runtime configuration of the gateway.
type Config struct {
	ListenAddr      string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration
	MaxHeaderBytes  int
	Upstream        UpstreamConfig
}

// Load reads the configuration, falling back to the defaults.
func Load() Config {
	return Config{
		ListenAddr:      str("LISTEN_ADDR", defaultListenAddr),
		ReadTimeout:     duration("HTTP_READ_TIMEOUT", defaultReadTimeout),
		WriteTimeout:    duration("HTTP_WRITE_TIMEOUT", defaultWriteTimeout),
		IdleTimeout:     duration("HTTP_IDLE_TIMEOUT", defaultIdleTimeout),
		ShutdownTimeout: duration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout),
		MaxHeaderBytes:  integer("MAX_HEADER_BYTES", defaultMaxHeaderBytes),
		Upstream:        loadUpstream(),
	}
}

func str(name, def string) string {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		return v
	}
	return def
}

func duration(name string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func integer(name string, def int) int {
	if v, ok := os.LookupEnv(envPrefix + name); ok {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
