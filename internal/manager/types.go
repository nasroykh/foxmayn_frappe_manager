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
