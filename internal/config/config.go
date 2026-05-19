package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Version is set at build time via ldflags.
var Version = "0.1.0"

// Config holds all agent configuration, loaded from environment variables.
type Config struct {
	Mode          string        // "direct" or "edge"
	Token         string        // Enrollment token from Server
	DirectListen  string        // e.g. ":9273"
	DirectTLS     bool          // Enable TLS for direct mode
	TLSCertDir    string        // Directory for TLS certs (auto-generated if empty)
	EdgeServerURL string        // e.g. "wss://dockpal.example.com:3012"
	EdgeReconnect time.Duration // Reconnect interval
	EdgeHeartbeat time.Duration // Heartbeat ping interval
	DockerSocket  string        // e.g. "/var/run/docker.sock"
}

// Load reads configuration from environment variables and validates required fields.
func Load() (*Config, error) {
	mode := os.Getenv("DOCKPAL_MODE")
	if mode != "direct" && mode != "edge" {
		return nil, fmt.Errorf("DOCKPAL_MODE must be 'direct' or 'edge', got %q", mode)
	}

	token := os.Getenv("DOCKPAL_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DOCKPAL_TOKEN is required")
	}

	cfg := &Config{
		Mode:          mode,
		Token:         token,
		DirectListen:  getEnv("DOCKPAL_DIRECT_LISTEN", ":9273"),
		DirectTLS:     getEnvBool("DOCKPAL_DIRECT_TLS", true),
		TLSCertDir:    getEnv("DOCKPAL_TLS_CERT_DIR", ""),
		EdgeServerURL: os.Getenv("DOCKPAL_EDGE_SERVER"),
		EdgeReconnect: getEnvDuration("DOCKPAL_EDGE_RECONNECT", 5*time.Second),
		EdgeHeartbeat: getEnvDuration("DOCKPAL_EDGE_HEARTBEAT", 30*time.Second),
		DockerSocket:  getEnv("DOCKPAL_DOCKER_SOCKET", "/var/run/docker.sock"),
	}

	if mode == "edge" && cfg.EdgeServerURL == "" {
		return nil, fmt.Errorf("DOCKPAL_EDGE_SERVER is required for edge mode")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
