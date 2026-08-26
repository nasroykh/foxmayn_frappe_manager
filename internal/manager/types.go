package manager

// CreateInput holds parameters for provisioning a new bench.
type CreateInput struct {
	Name              string
	FrappeBranch      string
	FrappeRepo        string
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
}

// RecreateInput holds parameters for recreating a bench from saved state.
type RecreateInput struct {
	Name              string
	Force             bool
	ReallocatePorts   bool
	GithubToken       string
	ProxyPortOverride *int
	ProxyHostOverride *string
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
}

// RestoreInput holds parameters for restoring an archive into a NEW bench.
//
// Restore never writes into an existing bench: the target name must be free.
// That is what lets the whole operation reuse Create's rollback defer, so a
// failed restore leaves the host exactly as it found it.
type RestoreInput struct {
	// Archive is the path to a .ffm.tar file.
	Archive string
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
	// KeepOnFailure leaves a failed restore's containers and directory in place
	// for diagnosis.
	KeepOnFailure bool
	// MaxExtractBytes caps extraction. Zero means the archive package default.
	MaxExtractBytes int64
	// SkipSpaceCheck bypasses the free-space preflight.
	SkipSpaceCheck bool
}
