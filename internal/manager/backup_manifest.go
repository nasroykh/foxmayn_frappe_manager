package manager

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

// Archive format identity.
//
// SchemaVersion is bumped whenever a field is added. MinReaderVersion is bumped
// only when an archive can no longer be restored correctly by an older ffm —
// the two are separate precisely so that additive changes stay restorable by
// binaries that predate them.
const (
	ArchiveKind      = "ffm-backup"
	SchemaVersion    = 1
	MinReaderVersion = 1
)

// Tiers recorded in Header.Tiers. A tier is present or absent; a reader must
// never infer one from the presence of a member path.
const (
	// TierCore is the database dump plus the site and bench metadata. Always present.
	TierCore = "core"
	// TierFiles is the site's public and private file attachments.
	TierFiles = "files"
)

// Header is the archive's first member: everything a restore needs in order to
// decide whether to touch the rest of the file. Kept deliberately small and
// self-contained so preflight on a multi-gigabyte archive costs one read.
type Header struct {
	Kind             string    `json:"kind"`
	SchemaVersion    int       `json:"schema_version"`
	MinReaderVersion int       `json:"min_reader_version"`
	FfmVersion       string    `json:"ffm_version"`
	CreatedAt        time.Time `json:"created_at"`
	BenchName        string    `json:"bench_name"`
	SiteName         string    `json:"site_name"`
	Mode             string    `json:"mode"`
	DBType           string    `json:"db_type"`
	FrappeVersion    string    `json:"frappe_version,omitempty"`
	Tiers            []string  `json:"tiers"`
	Label            string    `json:"label,omitempty"`
	// Trigger records what made the archive: TriggerManual or
	// TriggerScheduled. Additive and optional, so the schema version is
	// unchanged; archives without it (older ffm) are treated as manual, which
	// is what keeps them out of reach of scheduled pruning.
	Trigger string `json:"trigger,omitempty"`
}

// Archive triggers recorded in Header.Trigger.
const (
	TriggerManual    = "manual"
	TriggerScheduled = "scheduled"
)

// IsScheduled reports whether the archive was written by a scheduled run.
func (h Header) IsScheduled() bool { return h.Trigger == TriggerScheduled }

// AppInfo is an app's provenance at backup time.
//
// Commit comes from `git rev-parse HEAD` in apps/<name>, never from
// sites/apps.json: on a bench created by ffm that file records
// "commit_hash": null for frappe itself, because bench init runs in a temporary
// directory that is copied into place afterwards.
type AppInfo struct {
	Name   string `json:"name"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
	Remote string `json:"remote,omitempty"`
	// Dirty records uncommitted changes in the app's working tree that ffm did
	// not make itself. A restore rebuilds each app from its commit, so it cannot
	// reproduce these — it warns rather than pretending otherwise.
	//
	// ffm's own edits are excluded deliberately: it patches frappe's realtime
	// sources on every create and start, so counting them would mark every
	// single bench dirty and train the user to ignore the warning.
	Dirty bool `json:"dirty,omitempty"`
	// DirtyPaths lists the modified files, so the warning names them instead of
	// leaving the user to go looking. Capped; see maxDirtyPaths.
	DirtyPaths []string `json:"dirty_paths,omitempty"`
}

// SiteInfo captures the site's identity and configuration.
type SiteInfo struct {
	SiteName string `json:"site_name"`
	DBName   string `json:"db_name"`
	DBUser   string `json:"db_user,omitempty"`
	// InstalledApps is the authoritative app list, used to refuse a restore onto
	// a bench that lacks an app's code. Frappe performs no such check: a restore
	// of a dump referencing a missing app "succeeds" and then dies later in
	// `bench migrate` with Could not find app.
	InstalledApps []string `json:"installed_apps,omitempty"`
	// InstalledAppsSource records where InstalledApps came from, so a restore
	// can say how much it trusts the list. One of "list-apps", "apps.txt" or
	// "site_config".
	InstalledAppsSource string `json:"installed_apps_source,omitempty"`
	// SiteConfig and CommonSiteConfig are archived whole. Only a handful of keys
	// are written by ffm's create pipeline; the rest are set by bench init or by
	// the operator (maintenance_mode, mail_server, max_file_size, rate limits),
	// and dropping them silently would lose real configuration.
	SiteConfig       map[string]any `json:"site_config,omitempty"`
	CommonSiteConfig map[string]any `json:"common_site_config,omitempty"`
}

// Secrets holds the credentials an exact restore needs.
//
// These are stored in plaintext. The archive is created 0600 in a 0700
// directory, and ffm warns when it lands on a filesystem that cannot honour
// those modes. There is no redaction option because there is no flag that could
// supply the site's Fernet encryption_key back at restore time, and without it
// every Password-fieldtype value in the database — email account passwords,
// integration secrets, payment gateway keys — is permanently undecryptable.
type Secrets struct {
	AdminPassword  string `json:"admin_password,omitempty"`
	DBRootPassword string `json:"db_root_password,omitempty"`
	SiteDBPassword string `json:"site_db_password,omitempty"`
	// EncryptionKey is the site's Fernet key from site_config.json. It must be
	// written into the target site BEFORE anything reads the site: Frappe
	// generates the key lazily, so a single failed decrypt mints and persists a
	// new one, permanently orphaning the restored data.
	EncryptionKey string `json:"encryption_key,omitempty"`
	// BackupEncryptionKey decrypts a GPG-encrypted dump (System Settings →
	// encrypt_backup). Distinct from EncryptionKey, with a different job.
	BackupEncryptionKey string `json:"backup_encryption_key,omitempty"`
}

// Manifest is the archive's last member. Its presence is what makes an archive
// complete; see the archive package.
type Manifest struct {
	Header Header `json:"header"`
	// Bench is the state record verbatim, so a restore reproduces the bench's
	// ports, mode, TLS mode, domain aliases and prod tuning rather than
	// resetting every knob to a create-time default.
	Bench   state.Bench      `json:"bench"`
	Site    SiteInfo         `json:"site"`
	Apps    []AppInfo        `json:"apps,omitempty"`
	Secrets Secrets          `json:"secrets"`
	Members []archive.Member `json:"members"`
}

// NewHeader builds the header for a backup being taken now.
func NewHeader(b state.Bench, siteName, frappeVersion, label string, tiers []string, now time.Time) Header {
	return Header{
		Kind:             ArchiveKind,
		SchemaVersion:    SchemaVersion,
		MinReaderVersion: MinReaderVersion,
		FfmVersion:       version.Version,
		CreatedAt:        now.UTC(),
		BenchName:        b.Name,
		SiteName:         siteName,
		Mode:             benchMode(b),
		DBType:           b.DBEngine(),
		FrappeVersion:    frappeVersion,
		Tiers:            tiers,
		Label:            label,
	}
}

// benchMode returns the bench's mode, normalising the empty value that records
// written before Mode existed carry.
func benchMode(b state.Bench) string {
	if b.IsProd() {
		return "prod"
	}
	return "dev"
}

// HasTier reports whether the archive carries a tier.
func (h Header) HasTier(tier string) bool {
	for _, t := range h.Tiers {
		if t == tier {
			return true
		}
	}
	return false
}

// ParseHeader decodes and validates an archive header.
//
// Decoding is deliberately permissive about unknown fields: an archive written
// by a newer ffm must stay readable as long as it says it is, which is what
// MinReaderVersion is for.
func ParseHeader(data []byte) (Header, error) {
	var h Header
	if err := json.Unmarshal(data, &h); err != nil {
		return Header{}, fmt.Errorf("read archive header: %w", err)
	}
	if h.Kind != ArchiveKind {
		return Header{}, fmt.Errorf("not an ffm backup archive (kind %q)", h.Kind)
	}
	if h.MinReaderVersion > SchemaVersion {
		return Header{}, fmt.Errorf(
			"this archive needs a newer ffm: it requires archive schema %d and this build reads %d — "+
				"run 'ffm update'", h.MinReaderVersion, SchemaVersion)
	}
	if h.BenchName == "" || h.SiteName == "" {
		return Header{}, fmt.Errorf("archive header is incomplete (missing bench or site name)")
	}
	return h, nil
}

// ParseManifest decodes an archive manifest.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("read archive manifest: %w", err)
	}
	if m.Header.Kind != ArchiveKind {
		return Manifest{}, fmt.Errorf("archive manifest is not an ffm manifest (kind %q)", m.Header.Kind)
	}
	if len(m.Members) == 0 {
		return Manifest{}, fmt.Errorf("archive manifest lists no members")
	}
	return m, nil
}

// NewerSchema reports whether the archive was written by an ffm that knew more
// fields than this one. Restoring it is allowed — MinReaderVersion already
// gated that — but the caller warns once, because anything it does not know
// about is silently dropped.
func (h Header) NewerSchema() bool { return h.SchemaVersion > SchemaVersion }
