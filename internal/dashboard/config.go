package dashboard

import (
	"encoding/json"
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// Config holds persisted dashboard settings.
type Config struct {
	ListenAddr    string `json:"listen_addr"`
	AdminPassword string `json:"admin_password,omitempty"`
	// TLSCert and TLSKey are PEM file paths; with both set the dashboard
	// serves HTTPS itself.
	TLSCert string `json:"tls_cert,omitempty"`
	TLSKey  string `json:"tls_key,omitempty"`
	// InsecureHTTP allows a non-loopback listen address without TLS (the
	// password and session cookie then cross the network in clear).
	InsecureHTTP bool `json:"insecure_http,omitempty"`
	// AllowExec enables the one-shot command page. Off by default: it is
	// remote code execution in a container behind one shared password.
	AllowExec bool `json:"allow_exec,omitempty"`
}

// DefaultListenAddr is the default bind address (localhost only).
const DefaultListenAddr = "127.0.0.1:8787"

// LoadConfig reads dashboard.json or returns defaults.
func LoadConfig() (Config, error) {
	cfg := Config{ListenAddr: DefaultListenAddr}
	data, err := os.ReadFile(config.DashboardConfigFile())
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = DefaultListenAddr
	}
	return cfg, nil
}

// SaveConfig writes dashboard.json (mode 0600 when password set).
func SaveConfig(cfg Config) error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if cfg.AdminPassword != "" {
		mode = 0o600
	}
	if err := os.WriteFile(config.DashboardConfigFile(), data, mode); err != nil {
		return err
	}
	// WriteFile applies the mode only when it creates the file; a file first
	// written without a password (0644) must not stay world-readable.
	return os.Chmod(config.DashboardConfigFile(), mode)
}
