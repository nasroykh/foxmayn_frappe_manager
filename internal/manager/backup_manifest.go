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
	SchemaVersion    = 2
	MinReaderVersion = 1
	// MinReaderVersionAppSource is required of a reader when the archive
	// carries app source. An older ffm would ignore those members and try to
	// clone the app by its bare name — the exact failure they exist to prevent —
	// so it must refuse the archive instead. Archives without app source keep
	// MinReaderVersion and stay restorable by older binaries.
	MinReaderVersionAppSource = 2
)

// Tiers recorded in Header.Tiers. A tier is present or absent; a reader must
// never infer one from the presence of a member path.
const (
	// TierCore is the database dump plus the site and bench metadata. Always present.
	TierCore = "core"
	// TierFiles is the site's public and private file attachments.
	TierFiles = "files"
	// TierAppSource is the source of apps that cannot be cloned back: no git
	// repository, no remote, or a commit that exists only on the backed-up
	// bench. Present only when at least one app was archived this way.
	TierAppSource = "app-source"
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
	// Remote is the URL the app can be cloned from. bench clones with
	// `--origin upstream`, so this is read from "upstream" first; archives
	// written before that was fixed looked only at "origin" and recorded
	// nothing for any app bench had cloned.
	Remote string `json:"remote,omitempty"`
	// RemoteBranch is the branch on Remote that contains the backed-up commit,
	// which is what a restore clones. It differs from Branch when the local
	// branch was renamed or never pushed under its own name.
	RemoteBranch string `json:"remote_branch,omitempty"`
	// Tag is set when HEAD sits exactly on a tag — typically an app fetched
	// with `bench get-app --branch v15.2.0`, whose Branch is just "HEAD".
	Tag string `json:"tag,omitempty"`
	// NoGit records an app directory that is not a git repository at all, such
	// as one made with `bench new-app --no-git` or copied in by hand.
	NoGit bool `json:"no_git,omitempty"`
	// Unpushed records a commit that no remote-tracking branch or tag in the
	// clone contains, so a clone would silently produce different code. It is
	// judged from the clone's own refs, never by asking the remote: a shallow
	// clone that never fetched the branch holding the commit reads as
	// unpushed, which errs toward archiving the source.
	Unpushed bool `json:"unpushed,omitempty"`
	// Source says how a restore gets the app back: AppSourceGit (clone
	// Remote) or AppSourceArchive (unpack Member). Empty in archives written
	// before app source existed, which only ever meant git.
	Source string `json:"source,omitempty"`
	// SourceReason explains why an app's source was archived; see the
	// VendorReason constants.
	SourceReason string `json:"source_reason,omitempty"`
	// Member is the archive member holding the app's source when Source is
	// AppSourceArchive.
	Member string `json:"member,omitempty"`
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

// How a restore gets an app back, recorded in AppInfo.Source.
const (
	AppSourceGit     = "git"
	AppSourceArchive = "archive"
)

// Why an app's source went into the archive, recorded in AppInfo.SourceReason.
const (
	VendorNoGit     = "no-git"
	VendorNoRemote  = "no-remote"
	VendorNoCommit  = "no-commit"
	VendorUnpushed  = "unpushed"
	VendorRequested = "requested"
)

// Archived reports whether the app's source travels inside the archive.
func (a AppInfo) Archived() bool { return a.Source == AppSourceArchive && a.Member != "" }

// CloneBranch is the ref a restore clones the app at: the remote branch that
// holds the commit, else the tag HEAD sits on. Empty means the remote's
// default branch.
//
// Archives from before Source existed recorded only the local branch, which is
// the best they have. A newer archive never falls back to it: a local branch
// with no remote counterpart makes `git clone --branch` fail outright.
func (a AppInfo) CloneBranch() string {
	switch {
	case a.RemoteBranch != "":
		return a.RemoteBranch
	case a.Tag != "":
		return a.Tag
	case a.Source == "" && a.Branch != "HEAD":
		return a.Branch
	}
	return ""
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
	minReader := MinReaderVersion
	for _, t := range tiers {
		if t == TierAppSource {
			minReader = MinReaderVersionAppSource
		}
	}
	return Header{
		Kind:             ArchiveKind,
		SchemaVersion:    SchemaVersion,
		MinReaderVersion: minReader,
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
