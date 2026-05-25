package dashboard

import (
	"encoding/json"
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// Config holds persisted dashboard settings.
type Config struct {
	ListenAddr     string `json:"listen_addr"`
	AdminPassword  string `json:"admin_password,omitempty"`
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
	return os.WriteFile(config.DashboardConfigFile(), data, mode)
}
