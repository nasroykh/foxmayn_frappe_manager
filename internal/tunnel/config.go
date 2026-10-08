// Package tunnel manages VPS tunnel server profiles and the per-bench frpc sidecar.
package tunnel

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/lock"
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
// The write is atomic (temp file, sync, rename): a crash used to be able to
// truncate the file that holds every server's token. Use Update for
// read-modify-write.
func Save(cfg Config) error {
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := config.TunnelConfigFile()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tunnel-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Update loads tunnel.json, applies fn and saves it, holding a lock file so
// a CLI command and the dashboard cannot lose each other's changes. Nothing
// interactive may run inside fn.
func Update(fn func(*Config) error) error {
	l, err := lock.Acquire(config.TunnelConfigFile()+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock tunnel.json: %w", err)
	}
	defer l.Release()
	cfg, err := Load()
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return Save(cfg)
}

// Validate checks a server profile before it is stored.
func (s Server) Validate() error {
	switch {
	case strings.TrimSpace(s.Name) == "":
		return errors.New("server name is required")
	case strings.TrimSpace(s.Host) == "":
		return errors.New("host is required")
	case s.Port < 1 || s.Port > 65535:
		return fmt.Errorf("port %d is out of range", s.Port)
	case strings.TrimSpace(s.Token) == "":
		return errors.New("token is required")
	case strings.TrimSpace(s.BaseDomain) == "":
		return errors.New("base domain is required")
	}
	return nil
}

// PutServer adds or replaces a server profile. makeDefault also makes it the
// default; the first profile always becomes the default.
func PutServer(srv Server, makeDefault bool) error {
	if err := srv.Validate(); err != nil {
		return err
	}
	return Update(func(cfg *Config) error {
		cfg.Servers[srv.Name] = srv
		if makeDefault || cfg.Default == "" {
			cfg.Default = srv.Name
		}
		return nil
	})
}

// RemoveServer deletes a profile and returns the default afterwards. When the
// removed profile was the default, another one (if any) takes its place.
func RemoveServer(name string) (newDefault string, err error) {
	err = Update(func(cfg *Config) error {
		if _, ok := cfg.Servers[name]; !ok {
			return fmt.Errorf("tunnel server %q not found", name)
		}
		delete(cfg.Servers, name)
		if cfg.Default == name {
			cfg.Default = ""
			names := make([]string, 0, len(cfg.Servers))
			for n := range cfg.Servers {
				names = append(names, n)
			}
			sort.Strings(names)
			if len(names) > 0 {
				cfg.Default = names[0]
			}
		}
		newDefault = cfg.Default
		return nil
	})
	return newDefault, err
}

// UseServer makes an existing profile the default.
func UseServer(name string) error {
	return Update(func(cfg *Config) error {
		if _, ok := cfg.Servers[name]; !ok {
			return fmt.Errorf("tunnel server %q not found", name)
		}
		cfg.Default = name
		return nil
	})
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
