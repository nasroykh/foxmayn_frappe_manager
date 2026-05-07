// Package tunnel manages VPS tunnel server profiles and the per-bench frpc sidecar.
package tunnel

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// Server holds connection details for a VPS tunnel server running frps.
type Server struct {
	Name       string `json:"name"`
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Token      string `json:"token"`
	BaseDomain string `json:"base_domain"`
	TLS        bool   `json:"tls"`
}

// Config is the top-level structure persisted to tunnel.json.
type Config struct {
	Default string            `json:"default,omitempty"`
	Servers map[string]Server `json:"servers"`
}

// Load reads tunnel.json. Returns an empty Config when the file does not exist.
func Load() (Config, error) {
	data, err := os.ReadFile(config.TunnelConfigFile())
	if errors.Is(err, os.ErrNotExist) {
		return Config{Servers: make(map[string]Server)}, nil
	}
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.Servers == nil {
		cfg.Servers = make(map[string]Server)
	}
	return cfg, nil
}

// Save persists cfg to tunnel.json with mode 0o600 (contains auth tokens).
func Save(cfg Config) error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(config.TunnelConfigFile(), data, 0o600)
}

// Lookup returns the named server or an error if not found.
func Lookup(name string) (*Server, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	srv, ok := cfg.Servers[name]
	if !ok {
		return nil, errors.New("tunnel server not found: " + name)
	}
	return &srv, nil
}

// DefaultServer returns the default server and its profile name. Uses
// cfg.Default when set; otherwise picks the first configured server.
func DefaultServer() (*Server, string, error) {
	cfg, err := Load()
	if err != nil {
		return nil, "", err
	}
	name := cfg.Default
	if name == "" {
		for n := range cfg.Servers {
			name = n
			break
		}
	}
	if name == "" {
		return nil, "", errors.New("no tunnel server configured — run 'ffm tunnel server add'")
	}
	srv, ok := cfg.Servers[name]
	if !ok {
		return nil, "", errors.New("default tunnel server not found: " + name)
	}
	return &srv, name, nil
}
