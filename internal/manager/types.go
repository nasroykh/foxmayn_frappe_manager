package manager

// CreateInput holds parameters for provisioning a new bench.
type CreateInput struct {
	// recreating is set by Recreate, which rebuilds an EXISTING bench and so
	// must not be refused a name that has since become reserved.
	recreating bool

	Name         string
	FrappeBranch string
	FrappeRepo   string
	// Python and Node override the toolchain bench.ToolchainFor picks for the
	// Frappe branch. Empty means the branch default.
	Python            string
	Node              string
	Apps              []string
	AdminPassword     string
	DBPassword        string
	DBType            string
	GithubToken       string
	ProxyPort         int
	ProxyHost         string
	Mode              string
	Domain            string
	NoSSL             bool
	AcmeEmail         string
	MariaDBBufferPool string
	GunicornWorkers   int
	WorkerLongCount   int
	WorkerShortCount  int
	RedisCacheMaxmem  string
	RedisQueueMaxmem  string
	SlowQueryLog      bool
	MariaDBFastCommit bool
	FixedWebPort      int
	FixedSocketIOPort int
	// DomainAliases are extra hostnames Traefik routes to this bench on top of
	// the mode's primary host. Same effect as `ffm domain add` after creation.
	DomainAliases []string
	// AliasTLS serves the aliases over HTTPS with Let's Encrypt. Prod + SSL only.
	AliasTLS bool
	// MatchHostUser rebuilds the image with the in-container `frappe` user
	// remapped onto the host user's uid/gid. Needed on hosts whose uid is not
	// 1000 (e.g. GitHub-hosted runners, uid 1001), where the ./workspace bind
	// mount is otherwise unwritable across the boundary.
	MatchHostUser bool
	// KeepOnFailure leaves containers and the bench directory in place when
	// Create fails, instead of rolling them back. Intended for unattended runs
	// where the rollback would otherwise destroy the only diagnostic evidence.
	KeepOnFailure bool
	// Bind is state.BindLoopback (the default when empty) or state.BindLAN,
	// and decides which host interfaces the published ports listen on.
	Bind string
	// SSHAgent forwards the host SSH agent into the dev frappe container, for
	// SSH-URL private repos. Opt-in; it requires SSH_AUTH_SOCK at create time.
	SSHAgent bool
	// SkipAppInstall clones each app but does not install it on the site.
	//
	// Set only by Restore: the restored database already lists its apps as
	// installed, so installing them first is minutes of work that the dump then
	// overwrites. The app code still has to be present, which is why the clone
	// is not skipped too.
	SkipAppInstall bool
	// Agent sets up the agent-ready profile: loopback ports, no SSH agent, a
	// random admin password when the default was left, and ffc/MCP access
	// through a dedicated user. AgentReadOnly also limits MCP to read tools.
	Agent         bool
	AgentReadOnly bool
	// NoSeed neither uses nor saves a seed: bench init, get-app and bench
	// build run from the branch heads.
	NoSeed bool
	// SkipAssetBuild omits `bench build`.
	//
	// Set only by Restore, which builds once after the data is in place rather
	// than once here and again after migrate.
	SkipAssetBuild bool
}

// RecreateInput holds parameters for recreating a bench from saved state.
type RecreateInput struct {
	Name              string
	Force             bool
	ReallocatePorts   bool
	GithubToken       string
	ProxyPortOverride *int
	ProxyHostOverride *string
	// NoBackup skips the automatic backup taken before the bench is torn down.
	NoBackup bool
}

// DeleteInput removes a bench.
type DeleteInput struct {
	Name string
	// NoBackup skips the automatic backup taken before the bench is deleted.
	NoBackup bool
}

// DomainInput adds or removes a domain alias on a bench.
type DomainInput struct {
	Name   string
	Domain string
	// TLS switches the bench's aliases to HTTPS with Let's Encrypt. Only
	// meaningful on DomainAdd, and only for a prod bench that already serves its
	// primary domain over HTTPS.
	TLS bool
}

// SetProxyInput configures reverse-proxy settings for a bench.
type SetProxyInput struct {
	Name       string
	Port       int
	Host       string
	NoSSL      bool
	Reset      bool
	PrintCaddy bool
	PrintNginx bool
}

// TunnelEnableInput enables a VPS tunnel for a bench.
type TunnelEnableInput struct {
	BenchName  string
	ServerName string
	Subdomain  string
	// AllowDefaultPassword lets a bench that still has the default admin
	// password be published. Off by default.
	AllowDefaultPassword bool
}

// ExecInput runs a one-shot command in a container.
type ExecInput struct {
	BenchName string
	Service   string
	Command   string
}

// CleanLogsInput purges old log table rows.
type CleanLogsInput struct {
	BenchName string
	Days      int
	DryRun    bool
}

// RestartInput restarts a bench, optionally rebuilding the image.
type RestartInput struct {
	Name    string
	Rebuild bool
	// Fresh makes the rebuild ignore the layer cache and re-pull the base
	// image, so tools installed during the build are updated too.
	Fresh bool
}

// BenchView is a safe list/detail DTO (no DB passwords in list views).
type BenchView struct {
	Name         string
	Mode         string
	DBEngine     string
	Status       string
	WebPort      int
	SocketIOPort int
	SiteName     string
	Domain       string
	ProxyHost    string
	FrappeBranch string
	TunnelOn     bool
	// URL is where the site is reached, as `ffm status` reports it.
	URL string
	// TemplatesOutdated: the compose file predates this build's templates.
	TemplatesOutdated bool
}

// BenchDetail includes credentials for the detail page.
type BenchDetail struct {
	BenchView
	Dir           string
	AdminPassword string
	DBPassword    string
	Apps          []string
	ContainersPS  string
	SiteURL       string
	TunnelServer  string
	TunnelSub     string
}

// DashboardStats aggregates overview counts.
type DashboardStats struct {
	TotalBenches   int
	RunningBenches int
	StoppedBenches int
	ProxyRunning   bool
	TunnelsActive  int
	FailedJobs     int
}

// ProxyStatusView describes the shared Traefik proxy.
type ProxyStatusView struct {
	Status    string
	Network   string
	Running   bool
	Dashboard string
}

// BackupInput holds parameters for archiving a bench.
type BackupInput struct {
	BenchName string
	// Out is either a directory to write the archive into or an explicit .tar
	// path. Empty means config.BenchBackupsDir(BenchName).
	Out string
	// NoFiles omits the site's public and private attachments, producing a
	// database-only archive.
	NoFiles bool
	// Label is a short human note recorded in the archive header and printed
	// by 'ffm restore --dry-run'.
	Label string
	// SkipSpaceCheck bypasses the free-space preflight.
	SkipSpaceCheck bool
	// Trigger is recorded in the archive header: TriggerManual (the default)
	// or TriggerScheduled. Only scheduled archives are ever pruned.
	Trigger string
	// SkipIfStopped returns ErrBenchStopped instead of starting a stopped
	// bench. Scheduled runs set it: a stopped bench has not changed since its
	// last backup, and starting a bench nobody asked for is a surprise.
	SkipIfStopped bool
	// VendorApps names apps whose source is archived even though they could
	// be cloned back — typically to keep uncommitted changes. "all" selects
	// every app. Apps that cannot be cloned are archived regardless.
	VendorApps []string
	// Encrypt encrypts the finished archive with age to the recipients in
	// config.BackupRecipientsFile and deletes the plaintext.
	Encrypt bool
	// To uploads the archive to these backup targets. It implies Encrypt:
	// nothing leaves the host in clear. A failed upload fails the backup.
	To []string
	// writtenTo, when set, receives the path of the archive once it is
	// complete. Used by the operations that back up before destroying data.
	writtenTo *string
}

// RestoreInput holds parameters for restoring an archive into a NEW bench.
//
// Restore never writes into an existing bench: the target name must be free.
// That is what lets the whole operation reuse Create's rollback defer, so a
// failed restore leaves the host exactly as it found it.
type RestoreInput struct {
	// Archive is the path to a .ffm.tar file.
	// Identity is the age identity file that decrypts an encrypted archive
	// (default $FFM_AGE_IDENTITY_FILE).
	Identity string
	Archive  string
	// TargetName is the bench to create. Empty means the name recorded in the
	// archive.
	TargetName string
	// WithFiles restores public and private attachments, when the archive
	// carries them. There is no defaulting: the zero value means a
	// database-only restore, so a caller building this struct directly must set
	// it. The CLI sets it from --no-files.
	WithFiles bool
	// DryRun validates the archive and prints the plan without touching Docker.
	DryRun bool
	// AllowMissingEncryptionKey proceeds when the archive has no site
	// encryption_key, accepting that Password fields become undecryptable.
	AllowMissingEncryptionKey bool
	// EncryptionKey decrypts a GPG-encrypted dump when the archive does not
	// carry the key itself.
	EncryptionKey string
	// Domain overrides the production domain recorded in the archive.
	Domain string
	// NoSSL serves the restored production bench over plain HTTP.
	NoSSL bool
	// AcmeEmail is the Let's Encrypt account address for a production restore.
	AcmeEmail string
	// ReallocatePorts assigns a fresh port pair instead of reusing the
	// archive's, which is required when the original bench is still running.
	ReallocatePorts bool
	WebPort         int
	SocketIOPort    int
	// AdminPassword sets a new Administrator password. Empty keeps the one from
	// the archive.
	AdminPassword string
	// GithubToken authenticates private app clones during provisioning.
	GithubToken string
	// SkipMigrate omits 'bench migrate' after the data is in place. Only useful
	// when restoring onto exactly the app versions the backup was taken from.
	SkipMigrate bool
	// PinApps checks each app out at the commit recorded in the archive instead
	// of leaving it at branch HEAD.
	PinApps bool
	// AppOverrides are "<app>=<git-url>[@branch]" values that replace where an
	// app is cloned from. "frappe=..." sets the framework's repo and branch.
	// This is what makes an archive restorable when it records no usable
	// source for an app — including every archive written before ffm recorded
	// bench's "upstream" remote.
	AppOverrides []string
	// KeepOnFailure leaves a failed restore's containers and directory in place
	// for diagnosis.
	KeepOnFailure bool
	// LAN publishes the restored bench's ports on all interfaces instead of
	// 127.0.0.1. SSHAgent forwards the host SSH agent (dev only).
	LAN      bool
	SSHAgent bool
	// MaxExtractBytes caps extraction. Zero means the archive package default.
	MaxExtractBytes int64
	// SkipSpaceCheck bypasses the free-space preflight.
	SkipSpaceCheck bool
}
