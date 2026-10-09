package project

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// The trust record maps an ffm.yaml path to the SHA-256 of the content the
// user approved, like direnv's allow list. Any edit to the file, including
// one pulled from git, makes its hooks and tooling refuse to run until the
// user trusts it again.

// TrustFile is where the record lives.
func TrustFile() string { return filepath.Join(config.ConfigDir(), "trust.json") }

type trustEntry struct {
	Hash      string `json:"sha256"`
	TrustedAt string `json:"trusted_at"`
}

func loadTrust() (map[string]trustEntry, error) {
	m := map[string]trustEntry{}
	raw, err := os.ReadFile(TrustFile())
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", TrustFile(), err)
	}
	return m, nil
}

func saveTrust(m map[string]trustEntry) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(TrustFile()), 0o700); err != nil {
		return err
	}
	tmp := TrustFile() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, TrustFile())
}

// Trusted reports whether the user trusted exactly this content of the file.
func Trusted(f File) (bool, error) {
	m, err := loadTrust()
	if err != nil {
		return false, err
	}
	e, ok := m[f.Path]
	return ok && e.Hash == f.Hash(), nil
}

// Trust records the file's current content as trusted.
func Trust(f File) error {
	m, err := loadTrust()
	if err != nil {
		return err
	}
	m[f.Path] = trustEntry{Hash: f.Hash(), TrustedAt: time.Now().UTC().Format(time.RFC3339)}
	return saveTrust(m)
}

// Revoke forgets the file. It reports whether there was anything to forget.
func Revoke(path string) (bool, error) {
	m, err := loadTrust()
	if err != nil {
		return false, err
	}
	if _, ok := m[path]; !ok {
		return false, nil
	}
	delete(m, path)
	return true, saveTrust(m)
}
