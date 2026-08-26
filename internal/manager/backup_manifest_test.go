package manager

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// fullBench sets every field on state.Bench.
//
// The point of the round-trip test below is to catch a field being added to
// state.Bench and silently NOT surviving a backup: a restore would then rebuild
// the bench with that knob reset to its create-time default, which is exactly
// the class of bug TLSMode and the prod tuning fields were persisted to fix.
func fullBench() state.Bench {
	return state.Bench{
		Name:              "bkptest",
		Dir:               "/home/nas/frappe/bkptest",
		WebPort:           8010,
		SocketIOPort:      9010,
		FrappeBranch:      "version-16",
		FrappeRepo:        "https://github.com/acme/frappe",
		AdminPassword:     "s3cret-admin",
		DBPassword:        "s3cret-db",
		DBType:            "postgres",
		SiteName:          "erp.example.com",
		Apps:              []string{"erpnext", "https://github.com/acme/custom@develop"},
		ProxyHost:         "https://erp.example.com",
		Mode:              "prod",
		Domain:            "erp.example.com",
		DomainAliases:     []string{"erp.internal"},
		AliasTLS:          true,
		TLSMode:           state.TLSLetsEncrypt,
		MariaDBBufferPool: "4G",
		GunicornWorkers:   8,
		WorkerLongCount:   3,
		WorkerShortCount:  4,
		RedisCacheMaxmem:  "1gb",
		RedisQueueMaxmem:  "2gb",
		SlowQueryLog:      true,
		MatchHostUser:     true,
		Tunnel:            &state.TunnelState{Server: "vps1", Subdomain: "demo", Enabled: true},
		CreatedAt:         time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC),
	}
}

func TestManifestRoundTrip(t *testing.T) {
	want := Manifest{
		Header: NewHeader(fullBench(), "erp.example.com", "16.32.0", "nightly",
			[]string{TierCore, TierFiles}, time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)),
		Bench: fullBench(),
		Site: SiteInfo{
			SiteName:            "erp.example.com",
			DBName:              "_7daf46aa41f50b37",
			DBUser:              "_7daf46aa41f50b37",
			InstalledApps:       []string{"frappe", "erpnext"},
			InstalledAppsSource: "list-apps",
			SiteConfig:          map[string]any{"encryption_key": "k", "maintenance_mode": float64(1)},
			CommonSiteConfig:    map[string]any{"background_workers": float64(1)},
		},
		Apps: []AppInfo{{
			Name: "erpnext", Commit: "b24c9eba", Branch: "version-16",
			Remote: "https://github.com/frappe/erpnext", Dirty: true,
			DirtyPaths: []string{"banking/yarn.lock"},
		}},
		Secrets: Secrets{
			AdminPassword: "s3cret-admin", DBRootPassword: "s3cret-db",
			SiteDBPassword: "site-db", EncryptionKey: "fernet", BackupEncryptionKey: "gpg",
		},
		Members: []archive.Member{{
			Path: archive.Prefix + "/db/database.sql.gz", Size: 877447,
			SHA256: "deadbeef", Encoding: archive.EncodingGzip,
		}},
	}

	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	if !reflect.DeepEqual(got.Bench, want.Bench) {
		t.Errorf("bench record did not survive the round trip:\n got %+v\nwant %+v", got.Bench, want.Bench)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("manifest did not survive the round trip")
	}
}

// Every exported state.Bench field must be non-zero in fullBench, or the round
// trip above proves nothing about it.
func TestFullBenchCoversEveryField(t *testing.T) {
	v := reflect.ValueOf(fullBench())
	for i := 0; i < v.NumField(); i++ {
		f := v.Type().Field(i)
		if !f.IsExported() {
			continue
		}
		if v.Field(i).IsZero() {
			t.Errorf("state.Bench.%s is zero in fullBench — add it, or a backup could drop it "+
				"without any test noticing", f.Name)
		}
	}
}

func TestParseManifestRejectsIncomplete(t *testing.T) {
	if _, err := ParseManifest([]byte(`{"header":{"kind":"ffm-backup"},"members":[]}`)); err == nil {
		t.Error("a manifest listing no members was accepted")
	}
	if _, err := ParseManifest([]byte(`{"header":{"kind":"borg"}}`)); err == nil {
		t.Error("a foreign manifest was accepted")
	}
}

func TestHeaderTiers(t *testing.T) {
	h := NewHeader(fullBench(), "erp.example.com", "16.32.0", "", []string{TierCore}, time.Now())
	if !h.HasTier(TierCore) {
		t.Error("core tier not reported")
	}
	if h.HasTier(TierFiles) {
		t.Error("files tier reported when absent")
	}
	if h.Mode != "prod" {
		t.Errorf("mode = %q, want prod", h.Mode)
	}
}
