package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// site_config.json holds the site's Fernet encryption key and its database
// password, and it always EXISTS before a restore rewrites it — Create made it.
// os.WriteFile applies its mode argument only when it creates a file, so
// without an explicit Chmod the 0600 is decorative and the file keeps whatever
// mode Frappe gave it.
func TestWriteSiteConfigEnforcesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site_config.json")
	if err := os.WriteFile(path, []byte(`{"db_name":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 {
		t.Fatalf("fixture setup: mode = %o, want 644", info.Mode().Perm())
	}

	cfg := map[string]any{
		"db_name":        "_87957c9cd6c6c9c0",
		"encryption_key": "3_hb1VNx0G3_rWI7r_W8rvKDt_Ib1Nb2ULzGYpthyAs=",
	}
	if err := writeSiteConfig(path, cfg); err != nil {
		t.Fatalf("writeSiteConfig: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 600 — the encryption key is readable by other users", got)
	}

	var back map[string]any
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the rewritten config is not valid JSON: %v", err)
	}
	if back["encryption_key"] != cfg["encryption_key"] || back["db_name"] != cfg["db_name"] {
		t.Errorf("config did not round-trip: %v", back)
	}
}

// The merge must keep the archived site's custom keys while leaving the keys
// that belong to the NEW site alone.
func TestSiteConfigOwnedKeys(t *testing.T) {
	for _, k := range []string{"db_name", "db_password", "db_user", "db_type", "host_name", "installed_apps"} {
		if !siteConfigOwnedKeys[k] {
			t.Errorf("%s must be owned by the target site, not copied from the archive", k)
		}
	}
	for _, k := range []string{"encryption_key", "maintenance_mode", "mail_server", "developer_mode"} {
		if siteConfigOwnedKeys[k] {
			t.Errorf("%s belongs to the archived site and must survive the merge", k)
		}
	}
}
