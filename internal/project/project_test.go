package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `version: 1
name: shop
frappe:
  branch: version-15
python: "3.12"
db: postgres
apps: [erpnext, "https://github.com/acme/shop_theme@main"]
hooks:
  post_create: ["bench --site $SITE set-config developer_mode 1"]
  post_update: ["bench --site $SITE clear-cache"]
tooling:
  lint: ruff check apps/shop
  tests:
    cmd: bench --site $SITE run-tests --app shop
    description: App tests
`

func TestParse(t *testing.T) {
	f, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "shop" || f.Frappe.Branch != "version-15" || f.Python != "3.12" || f.DB != "postgres" || len(f.Apps) != 2 {
		t.Errorf("parsed %+v", f)
	}
	if f.Tooling["lint"].Cmd != "ruff check apps/shop" || f.Tooling["tests"].Description != "App tests" {
		t.Errorf("tooling %+v", f.Tooling)
	}
	if !f.HasCommands() || len(f.Commands()) != 4 || strings.Join(f.ToolNames(), ",") != "lint,tests" {
		t.Errorf("commands %v", f.Commands())
	}
	plain, _ := Parse([]byte("version: 1\napps: [erpnext]\n"))
	if plain.HasCommands() {
		t.Error("a file without hooks or tooling has commands")
	}
}

func TestParseRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":         "",
		"no version":    "apps: [erpnext]\n",
		"version 2":     "version: 2\n",
		"typo":          "version: 1\nap: [erpnext]\n",
		"tool typo":     "version: 1\ntooling:\n  lint: {command: x}\n",
		"bad tool name": "version: 1\ntooling:\n  Lint: x\n",
		"empty tool":    "version: 1\ntooling:\n  lint: \"\"\n",
		"bad python":    "version: 1\npython: \"3.9\"\n",
		"bad db":        "version: 1\ndb: sqlite\n",
		"bad branch":    "version: 1\nfrappe:\n  branch: \"v16; rm -rf /\"\n",
		"bad repo":      "version: 1\nfrappe:\n  repo: file:///etc\n",
		"shell in app":  "version: 1\napps: [\"erpnext; curl x\"]\n",
		"empty hook":    "version: 1\nhooks:\n  post_create: [\"\"]\n",
		"unknown hook":  "version: 1\nhooks:\n  pre_create: [x]\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestFindStopsAtRepositoryRoot(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "a", "b")
	os.MkdirAll(sub, 0o755)
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	// A file above the repository must not be picked up.
	os.WriteFile(filepath.Join(root, FileName), []byte("version: 1\n"), 0o644)
	if _, err := Find(sub); err == nil {
		t.Fatal("found an ffm.yaml outside the repository")
	}
	os.WriteFile(filepath.Join(repo, FileName), []byte("version: 1\n"), 0o644)
	got, err := Find(sub)
	if err != nil || got != filepath.Join(repo, FileName) {
		t.Fatalf("Find = %q, %v", got, err)
	}
}

func TestLoadNamesFromDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "My_Shop.App")
	os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte("version: 1\n"), 0o644)
	f, err := Load(p)
	if err != nil || f.Name != "my-shop-app" || f.Path != p {
		t.Fatalf("Load = %+v, %v", f, err)
	}
	os.WriteFile(p, []byte("version: 1\nname: list\n"), 0o644)
	if _, err := Load(p); err == nil {
		t.Error("a reserved bench name was accepted")
	}
}

func TestTrustFollowsContent(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	dir := t.TempDir()
	p := filepath.Join(dir, FileName)
	os.WriteFile(p, []byte(sample), 0o644)
	f, _ := Load(p)
	if ok, _ := Trusted(f); ok {
		t.Fatal("trusted before Trust")
	}
	if err := Trust(f); err != nil {
		t.Fatal(err)
	}
	if ok, _ := Trusted(f); !ok {
		t.Fatal("not trusted after Trust")
	}
	if st, _ := os.Stat(TrustFile()); st.Mode().Perm() != 0o600 {
		t.Errorf("trust file mode %v", st.Mode().Perm())
	}
	// Any change, even one byte, needs trust again.
	os.WriteFile(p, []byte(sample+"# x\n"), 0o644)
	changed, _ := Load(p)
	if ok, _ := Trusted(changed); ok {
		t.Error("a changed file is still trusted")
	}
	if had, err := Revoke(p); !had || err != nil {
		t.Errorf("Revoke = %v, %v", had, err)
	}
	if ok, _ := Trusted(f); ok {
		t.Error("trusted after Revoke")
	}
}
