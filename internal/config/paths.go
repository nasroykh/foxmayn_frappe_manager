package config

import (
	"os"
	"path/filepath"
)

// BenchesDir returns ~/frappe — where all bench workdirs live.
func BenchesDir() string {
	if d := os.Getenv("FFM_BENCHES_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "frappe")
}

// ConfigDir returns ~/.config/ffm.
func ConfigDir() string {
	if d := os.Getenv("FFM_CONFIG_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ffm")
}

// BenchDir returns the directory for a specific bench: ~/frappe/<name>.
func BenchDir(name string) string {
	return filepath.Join(BenchesDir(), name)
}

// StateFile returns the path to benches.json, stored in the config dir to
// keep it separate from bench directories (avoids naming collisions).
func StateFile() string {
	return filepath.Join(ConfigDir(), "benches.json")
}

// AcmeEmailFile returns the path to the file storing the Let's Encrypt ACME
// email, so users only need to supply it once across all production benches.
func AcmeEmailFile() string {
	return filepath.Join(ConfigDir(), ".acme_email")
}

// EnsureDataDir creates both the benches dir and config dir if they don't exist.
func EnsureDataDir() error {
	if err := os.MkdirAll(BenchesDir(), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(ConfigDir(), 0o755)
}
