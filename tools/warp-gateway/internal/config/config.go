package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Config holds warp-gateway process configuration.
type Config struct {
	Listen           string        `json:"listen"`
	Token            string        `json:"token"`
	DataDir          string        `json:"data_dir"`
	DefaultHost      string        `json:"default_host"`
	PortRangeStart   int           `json:"port_range_start"`
	PortRangeEnd     int           `json:"port_range_end"`
	HealthInterval   time.Duration `json:"health_interval"`
	ProbeURL         string        `json:"probe_url"`
	Runtime          string        `json:"runtime"` // mock | sing-box
	SingBoxPath      string        `json:"sing_box_path"`
	UnhealthyAfter   int           `json:"unhealthy_after"`
	ReconcileOnStart bool          `json:"reconcile_on_start"`
	// ProfileKey encrypts private keys at rest (AES-256-GCM). Never falls back to Token.
	ProfileKey string `json:"profile_key,omitempty"`
	// TLS for multi-host control plane (optional).
	TLSCertFile string `json:"tls_cert_file,omitempty"`
	TLSKeyFile  string `json:"tls_key_file,omitempty"`
	// ClientCAFile enables mTLS: require client certs signed by this CA.
	ClientCAFile string `json:"client_ca_file,omitempty"`
}

func Default() Config {
	return Config{
		Listen:         "127.0.0.1:19798",
		Token:          "",
		DataDir:        "./data/warp-gateway",
		DefaultHost:    "127.0.0.1",
		PortRangeStart: 41001,
		PortRangeEnd:   41100,
		HealthInterval: 30 * time.Second,
		// Use IP URL so health probes work even when local DNS is fake-ip hijacked.
		ProbeURL:         "https://1.1.1.1/cdn-cgi/trace",
		Runtime:          "mock",
		SingBoxPath:      "sing-box",
		UnhealthyAfter:   3,
		ReconcileOnStart: true,
	}
}

// LoadFromEnv overlays environment variables on defaults.
func LoadFromEnv() Config {
	cfg := Default()
	if v := os.Getenv("WARP_GATEWAY_LISTEN"); v != "" {
		cfg.Listen = v
	}
	if v := os.Getenv("WARP_GATEWAY_TOKEN"); v != "" {
		cfg.Token = v
	}
	if v := os.Getenv("WARP_GATEWAY_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("WARP_GATEWAY_RUNTIME"); v != "" {
		cfg.Runtime = v
	}
	if v := os.Getenv("WARP_GATEWAY_SING_BOX"); v != "" {
		cfg.SingBoxPath = v
	}
	if v := os.Getenv("WARP_GATEWAY_PROBE_URL"); v != "" {
		cfg.ProbeURL = v
	}
	if v := os.Getenv("WARP_GATEWAY_DEFAULT_HOST"); v != "" {
		cfg.DefaultHost = v
	}
	if v := os.Getenv("WARP_GATEWAY_PROFILE_KEY"); v != "" {
		cfg.ProfileKey = v
	}
	if v := os.Getenv("WARP_GATEWAY_TLS_CERT"); v != "" {
		cfg.TLSCertFile = v
	}
	if v := os.Getenv("WARP_GATEWAY_TLS_KEY"); v != "" {
		cfg.TLSKeyFile = v
	}
	if v := os.Getenv("WARP_GATEWAY_CLIENT_CA"); v != "" {
		cfg.ClientCAFile = v
	}
	return cfg
}

// ProfileSecret returns the explicit at-rest profile encryption key.
// API tokens must not be reused: rotating the control-plane token would
// otherwise make existing ciphertext unreadable.
func (c Config) ProfileSecret() string {
	return strings.TrimSpace(c.ProfileKey)
}

func (c Config) Validate() error {
	if c.Listen == "" {
		return fmt.Errorf("listen is required")
	}
	if c.PortRangeStart <= 0 || c.PortRangeEnd < c.PortRangeStart {
		return fmt.Errorf("invalid port range %d-%d", c.PortRangeStart, c.PortRangeEnd)
	}
	switch c.Runtime {
	case "mock", "sing-box":
	default:
		return fmt.Errorf("unsupported runtime %q (mock|sing-box)", c.Runtime)
	}
	if err := c.validateListenAuth(); err != nil {
		return err
	}
	return nil
}

func (c Config) validateListenAuth() error {
	tokenOK := strings.TrimSpace(c.Token) != ""
	ca := strings.TrimSpace(c.ClientCAFile)
	cert := strings.TrimSpace(c.TLSCertFile)
	key := strings.TrimSpace(c.TLSKeyFile)
	hasCA, hasCert, hasKey := ca != "", cert != "", key != ""
	if hasCert != hasKey {
		return fmt.Errorf("tls_cert_file and tls_key_file must both be set")
	}
	if hasCA && (!hasCert || !hasKey) {
		return fmt.Errorf("client_ca_file requires tls_cert_file and tls_key_file for mTLS")
	}
	mtlsOK := hasCA && hasCert && hasKey
	if tokenOK || mtlsOK {
		return nil
	}
	if listenHostIsLoopback(c.Listen) {
		return nil
	}
	return fmt.Errorf("token is required when listening on non-loopback %q without mTLS", c.Listen)
}

// listenHostIsLoopback reports whether listen binds only loopback.
// Empty host (":19798"), 0.0.0.0, :: and other unspecified addresses are
// treated as all-interfaces — not loopback — and require a token or mTLS.
func listenHostIsLoopback(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		host = listen
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if host == "localhost" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsUnspecified() {
		return false
	}
	return ip.IsLoopback()
}

func (c Config) String() string {
	b, _ := json.Marshal(c)
	return string(b)
}

const profileKeyFileName = "profile.key"

// EnsureProfileKey loads or creates a 0600 key file under DataDir when ProfileKey is empty.
func EnsureProfileKey(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if strings.TrimSpace(cfg.ProfileKey) != "" {
		return nil
	}
	if strings.TrimSpace(cfg.DataDir) == "" {
		return fmt.Errorf("data_dir is required to persist profile key")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	keyPath := filepath.Join(cfg.DataDir, profileKeyFileName)
	if b, err := os.ReadFile(keyPath); err == nil {
		if key := strings.TrimSpace(string(b)); key != "" {
			cfg.ProfileKey = key
			return nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	key := hex.EncodeToString(raw)
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	cfg.ProfileKey = key
	return nil
}
