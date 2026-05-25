package manager

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// createOpts carries optional behavior for Create. When fixedWebPort and
// fixedSocketIOPort are both non-zero, that host port pair is used instead of
// AllocatePorts (used by recreate to keep stable URLs).
type createOpts struct {
	fixedWebPort      int
	fixedSocketIOPort int
}

func (s *Service) createOptsFromInput(in CreateInput) *createOpts {
	if in.FixedWebPort > 0 && in.FixedSocketIOPort > 0 {
		return &createOpts{fixedWebPort: in.FixedWebPort, fixedSocketIOPort: in.FixedSocketIOPort}
	}
	return nil
}

// ReadSavedAcmeEmail reads the stored ACME email from ~/.config/ffm/.acme_email.
// Returns empty string if the file does not exist.
func ReadSavedAcmeEmail() string {
	data, err := os.ReadFile(config.AcmeEmailFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// SaveAcmeEmail persists the ACME email to ~/.config/ffm/.acme_email.
func SaveAcmeEmail(email string) {
	_ = os.WriteFile(config.AcmeEmailFile(), []byte(email+"\n"), 0o600)
}

// Create provisions a new bench (same pipeline as ffm create).
func (s *Service) Create(in CreateInput, pw ProgressWriter) (createErr error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	name := in.Name
	frappeBranch := in.FrappeBranch
	apps := in.Apps
	adminPassword := in.AdminPassword
	dbPassword := in.DBPassword
	dbType := in.DBType
	githubToken := in.GithubToken
	proxyPort := in.ProxyPort
	proxyHost := in.ProxyHost
	mode := in.Mode
	domain := in.Domain
	noSSL := in.NoSSL
	acmeEmail := in.AcmeEmail
	mariadbBufferPool := in.MariaDBBufferPool
	gunicornWorkers := in.GunicornWorkers
	workerLongCount := in.WorkerLongCount
	workerShortCount := in.WorkerShortCount
	redisCacheMaxmem := in.RedisCacheMaxmem
	redisQueueMaxmem := in.RedisQueueMaxmem
	slowQueryLog := in.SlowQueryLog
	opts := s.createOptsFromInput(in)

	step := func(msg string) { pw.Step(msg) }
	// Validate mode
	if mode != "dev" && mode != "prod" {
		return fmt.Errorf("invalid --mode %q: must be 'dev' or 'prod'", mode)
	}

	// Normalise and validate db type
	if dbType == "" {
		dbType = "mariadb"
	}
	if dbType != "mariadb" && dbType != "postgres" {
		return fmt.Errorf("invalid --db-type %q: must be 'mariadb' or 'postgres'", dbType)
	}

	// Prod-specific validation
	if mode == "prod" {
		if domain == "" {
			return fmt.Errorf("--domain is required for production mode (e.g. --domain erp.example.com)")
		}
		if adminPassword == "admin" {
			return fmt.Errorf("default admin password is not allowed in production — set --admin-password to a strong password")
		}
		if !noSSL {
			if acmeEmail == "" {
				acmeEmail = ReadSavedAcmeEmail()
			}
			if acmeEmail == "" {
				return fmt.Errorf("--acme-email is required for the first production bench with SSL\n" +
					"  (use --no-ssl to skip Let's Encrypt and handle TLS externally)")
			}
		}
	}

	// 1. Validate name
	if err := bench.ValidateName(name); err != nil {
		return err
	}

	s.lock()
	_, existsErr := s.Store.Get(name)
	s.unlock()
	if existsErr == nil {
		return fmt.Errorf("bench %q already exists", name)
	}

	pw.Printf("Creating bench %q  (frappe: %s  mode: %s", name, frappeBranch, mode)
	if mode == "prod" {
		pw.Printf("  domain: %s", domain)
	}
	if len(apps) > 0 {
		pw.Printf("  apps: %v", apps)
	}
	pw.Println(")")

	// Allocate ports (or reuse a fixed pair for recreate)
	step("Allocating ports")
	var webPort, socketIOPort int
	if opts != nil && opts.fixedWebPort > 0 && opts.fixedSocketIOPort > 0 {
		if !bench.ValidBenchPortPair(opts.fixedWebPort, opts.fixedSocketIOPort) {
			return fmt.Errorf("invalid fixed port pair: web_port=%d socketio_port=%d (must match ffm bench pairing)",
				opts.fixedWebPort, opts.fixedSocketIOPort)
		}
		webPort = opts.fixedWebPort
		socketIOPort = opts.fixedSocketIOPort
		if err := bench.CheckTCPPortsFree(webPort, socketIOPort); err != nil {
			return fmt.Errorf("fixed host ports not available (another process may be using them): %w", err)
		}
	} else {
		var err error
		s.lock()
		webPort, socketIOPort, err = bench.AllocatePorts(s.Store)
		s.unlock()
		if err != nil {
			return fmt.Errorf("port allocation: %w", err)
		}
	}

	benchDir := config.BenchDir(name)
	runner := bench.NewRunner(name, benchDir, s.Verbose)

	// Site name: domain for prod, <name>.localhost for dev
	siteName := name + ".localhost"
	if mode == "prod" {
		siteName = domain
	}

	// Automatic cleanup on failure.
	defer func() {
		if createErr == nil {
			return
		}
		fmt.Fprintln(os.Stderr, "\nCreate failed — cleaning up...")
		composePath := filepath.Join(benchDir, "docker-compose.yml")
		if _, statErr := os.Stat(composePath); statErr == nil {
			// Dump DB container logs so the user can diagnose startup failures
			// (e.g. bad MariaDB flags) before the containers are torn down.
			dbService := "mariadb"
			if dbType == "postgres" {
				dbService = "postgres"
			}
			fmt.Fprintf(os.Stderr, "\n--- %s container logs ---\n", dbService)
			fmt.Fprintln(os.Stderr, runner.LogsString(dbService))
			fmt.Fprintln(os.Stderr, "--- end logs ---")

			if downErr := runner.Down(true); downErr != nil && s.Verbose {
				fmt.Fprintf(os.Stderr, "  cleanup: docker compose down: %v\n", downErr)
			}
		}
		if rmErr := os.RemoveAll(benchDir); rmErr != nil && s.Verbose {
			fmt.Fprintf(os.Stderr, "  cleanup: remove bench dir: %v\n", rmErr)
		}
	}()

	// Create bench directory
	step("Creating bench directory")
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return fmt.Errorf("create bench dir: %w", err)
	}

	// Ensure shared ffm-proxy Docker network (and HTTPS support for prod+SSL)
	if mode == "prod" && !noSSL {
		step("Ensuring HTTPS proxy (Let's Encrypt)")
		if err := proxy.EnsureHTTPS(acmeEmail); err != nil {
			return fmt.Errorf("ensure HTTPS proxy: %w", err)
		}
		SaveAcmeEmail(acmeEmail)
	} else {
		step("Ensuring ffm-proxy network")
		if err := proxy.EnsureNetwork(); err != nil {
			return fmt.Errorf("ensure proxy network: %w", err)
		}
	}

	// Write compose file, Dockerfile, and (dev only) devcontainer config
	step("Writing docker-compose.yml and Dockerfile")
	if mariadbBufferPool == "" {
		mariadbBufferPool = "1G"
	}
	if gunicornWorkers <= 0 {
		gunicornWorkers = 2
	}
	if workerLongCount <= 0 {
		workerLongCount = 1
	}
	if workerShortCount <= 0 {
		workerShortCount = 1
	}
	if redisCacheMaxmem == "" {
		redisCacheMaxmem = "512mb"
	}
	if redisQueueMaxmem == "" {
		redisQueueMaxmem = "512mb"
	}
	data := bench.ComposeData{
		Name:              name,
		Mode:              mode,
		BenchDir:          benchDir,
		WebPort:           webPort,
		WebPortEnd:        webPort + 5,
		SocketIOPort:      socketIOPort,
		SocketIOPortEnd:   socketIOPort + 5,
		DBType:            dbType,
		DBRootPassword:    dbPassword,
		ForwardSSHAgent:   mode == "dev" && os.Getenv("SSH_AUTH_SOCK") != "",
		Domain:            domain,
		SiteName:          siteName,
		NoSSL:             noSSL,
		MariaDBBufferPool: mariadbBufferPool,
		GunicornWorkers:   gunicornWorkers,
		WorkerLongCount:   workerLongCount,
		WorkerShortCount:  workerShortCount,
		RedisCacheMaxmem:  redisCacheMaxmem,
		RedisQueueMaxmem:  redisQueueMaxmem,
		SlowQueryLog:      slowQueryLog && mode == "prod" && dbType == "mariadb",
	}
	if data.SlowQueryLog {
		if err := os.MkdirAll(filepath.Join(benchDir, "mysql-logs"), 0o755); err != nil {
			return fmt.Errorf("create mysql-logs dir: %w", err)
		}
	}
	if err := bench.WriteCompose(benchDir, data); err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	if err := bench.WriteDockerfile(benchDir, data); err != nil {
		return fmt.Errorf("render dockerfile: %w", err)
	}
	if mode == "dev" {
		if err := bench.WriteDevcontainer(benchDir, data); err != nil {
			return fmt.Errorf("render devcontainer: %w", err)
		}
	}

	// Build the Docker image
	step("Building Docker image — first build takes a few minutes, cached after")
	if err := runner.Build(); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}

	// Create workspace directory
	step("Creating workspace directory")
	workspaceDir := filepath.Join(benchDir, "workspace")
	if err := os.RemoveAll(filepath.Join(workspaceDir, "frappe-bench")); err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	// Run bench init — dev mode also installs Claude/agent skills
	step(fmt.Sprintf("Initializing bench (frappe %s) — this takes several minutes on first run", frappeBranch))
	var benchInitCmd string
	baseInit := fmt.Sprintf(
		`bench init --frappe-branch %s --skip-redis-config-generation --no-backups --verbose /tmp/ffm-bench-init`+
			` && rm -rf /workspace/frappe-bench`+
			` && cp -a /tmp/ffm-bench-init /workspace/frappe-bench`+
			` && grep -rIl '/tmp/ffm-bench-init' /workspace/frappe-bench 2>/dev/null | xargs -r sed -i 's|/tmp/ffm-bench-init|/workspace/frappe-bench|g'`+
			` && rm -rf /tmp/ffm-bench-init`,
		frappeBranch,
	)
	if mode == "dev" {
		benchInitCmd = baseInit +
			` && mkdir -p /workspace/frappe-bench/.agents/skills /workspace/frappe-bench/.claude/skills` +
			` && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.agents/skills/` +
			` && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.claude/skills/` +
			` && mkdir -p /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli` +
			` && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli/` +
			` && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli/`
	} else {
		benchInitCmd = baseInit
	}
	if err := runner.Run("frappe", "bash", "-c", benchInitCmd); err != nil {
		return fmt.Errorf("bench init: %w", err)
	}
	if _, err := os.Stat(filepath.Join(workspaceDir, "frappe-bench", "apps")); err != nil {
		return fmt.Errorf("bench init failed silently — no apps/ directory found at %s/frappe-bench", workspaceDir)
	}

	// Write wsgi.py after bench init so sites/ exists. Lives under the workspace
	// bind mount — no extra volume entry needed in docker-compose.yml.
	if mode == "prod" {
		if err := bench.WriteWsgiWrapper(benchDir, siteName); err != nil {
			return fmt.Errorf("write wsgi.py: %w", err)
		}
	}
	if err := bench.PatchAuthenticateJs(benchDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not patch authenticate.js: %v\n", err)
	}
	if err := bench.PatchUtilsJs(benchDir); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not patch utils.js: %v\n", err)
	}

	if mode == "dev" {
		frappeBench := filepath.Join(workspaceDir, "frappe-bench")
		if err := writeClaudeMcpConfigHost(frappeBench, name); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write Claude Code .mcp.json (ffc MCP): %v\n", err)
		}
	}

	// Start containers.
	// Prod uses a two-phase start: bring up only the DB + redis + frappe tier
	// first so that the scheduler/worker containers don't start (and crash-loop
	// on missing app modules) before apps are installed.
	if mode == "prod" {
		dbService := "mariadb"
		if dbType == "postgres" {
			dbService = "postgres"
		}
		step("Starting core services (DB, redis, frappe)")
		if err := runner.UpServices(dbService, "redis-cache", "redis-queue", "frappe"); err != nil {
			return fmt.Errorf("docker compose up (core services): %w", err)
		}
	} else {
		step("Starting containers (docker compose up)")
		if err := runner.Up(); err != nil {
			return fmt.Errorf("docker compose up: %w", err)
		}
	}

	// Wait for database
	if dbType == "postgres" {
		step("Waiting for PostgreSQL to be ready...")
		if err := runner.WaitForPostgres(dbPassword, 90*time.Second, os.Stderr); err != nil {
			return fmt.Errorf("wait for PostgreSQL: %w", err)
		}
	} else {
		step("Waiting for MariaDB to be ready...")
		if err := runner.WaitForMariaDB(dbPassword, 90*time.Second, os.Stderr); err != nil {
			return fmt.Errorf("wait for MariaDB: %w", err)
		}
	}
	pw.Println()

	// Configure common_site_config
	step("Configuring site settings")
	var socketIOPortCfg int
	if mode == "prod" {
		if noSSL {
			socketIOPortCfg = 80 // Traefik public HTTP port
		} else {
			socketIOPortCfg = 443 // Traefik public HTTPS port
		}
	} else if proxyPort > 0 {
		socketIOPortCfg = proxyPort
	} else {
		// Dev: clients must use the host-published port (9000, 9010, …), not
		// container-internal 9000 — otherwise the 2nd+ bench breaks Socket.IO.
		socketIOPortCfg = socketIOPort
	}
	var dbConfigCmd string
	if dbType == "postgres" {
		dbConfigCmd = " && bench set-config -g db_host postgres" +
			" && bench set-config -gp db_port 5432" +
			" && bench set-config -g db_type postgres" +
			" && bench set-config -g db_schema public"
	} else {
		dbConfigCmd = " && bench set-config -g db_host mariadb" +
			" && bench set-config -gp db_port 3306"
	}
	configCmd := "cd /workspace/frappe-bench" +
		dbConfigCmd +
		" && bench set-config -g redis_cache redis://redis-cache:6379" +
		" && bench set-config -g redis_queue redis://redis-queue:6379" +
		" && bench set-config -g redis_socketio redis://redis-queue:6379" +
		fmt.Sprintf(" && bench set-config -gp socketio_port %d", socketIOPortCfg) +
		fmt.Sprintf(" && bench set-config -g socketio_frappe_url %s", socketioFrappeURL(mode))
	if (mode == "prod" && !noSSL) || proxyPort == 443 {
		configCmd += " && bench set-config -gp use_ssl 1"
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c", configCmd); err != nil {
		return fmt.Errorf("configure site settings: %w\n%s", err, out)
	}

	// bench new-site
	step(fmt.Sprintf("Creating site %q", siteName))
	var newSiteCmd string
	if dbType == "postgres" {
		newSiteCmd = fmt.Sprintf(
			"cd /workspace/frappe-bench && bench new-site %s --db-type postgres --db-root-username postgres --db-root-password %s --admin-password %s",
			siteName, dbPassword, adminPassword,
		)
	} else {
		newSiteCmd = fmt.Sprintf(
			"cd /workspace/frappe-bench && bench new-site %s --mariadb-root-password %s --admin-password %s --no-mariadb-socket",
			siteName, dbPassword, adminPassword,
		)
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c", newSiteCmd); err != nil {
		return fmt.Errorf("bench new-site: %w\n%s", err, out)
	}

	// Developer mode (dev only)
	if mode == "dev" {
		step("Enabling developer mode")
		devModeCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config developer_mode 1 && bench use %s",
			siteName, siteName,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", devModeCmd); err != nil {
			return fmt.Errorf("enable developer mode: %w\n%s", err, out)
		}
	}

	// Set host_name: always for prod, optional for dev (when --proxy-host provided)
	resolvedProxyHost := ""
	if mode == "prod" {
		scheme := "https"
		if noSSL {
			scheme = "http"
		}
		resolvedProxyHost = fmt.Sprintf("%s://%s", scheme, domain)
		step(fmt.Sprintf("Setting host_name to %s", resolvedProxyHost))
		hostCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
			siteName, resolvedProxyHost,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", hostCmd); err != nil {
			return fmt.Errorf("set host_name: %w\n%s", err, out)
		}
	} else if proxyHost != "" {
		scheme := "http"
		if proxyPort == 443 {
			scheme = "https"
		}
		cleanHost := strings.TrimPrefix(strings.TrimPrefix(proxyHost, "https://"), "http://")
		resolvedProxyHost = fmt.Sprintf("%s://%s", scheme, cleanHost)

		step(fmt.Sprintf("Setting host_name to %s", resolvedProxyHost))
		hostCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config host_name %s",
			siteName, resolvedProxyHost,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", hostCmd); err != nil {
			return fmt.Errorf("set host_name: %w\n%s", err, out)
		}
	}

	// Configure GitHub credentials if provided
	if githubToken != "" {
		step("Configuring GitHub credentials inside container")
		if err := runner.ConfigureGitHubToken(githubToken); err != nil {
			return fmt.Errorf("configure GitHub token: %w", err)
		}
		defer runner.CleanupGitHubToken()
	}

	// Install additional apps
	for _, raw := range apps {
		spec := bench.ParseAppSpec(raw, frappeBranch)
		displayName := spec.DisplayName()
		branchDesc := spec.Branch
		if branchDesc == "" {
			branchDesc = "default"
		}

		step(fmt.Sprintf("Getting app %q (branch: %s) — may take a few minutes", displayName, branchDesc))
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && "+spec.GetAppCmd()); err != nil {
			return fmt.Errorf("bench get-app %s: %w\n%s", displayName, err, out)
		}

		step(fmt.Sprintf("Installing app %q on site %q", displayName, siteName))
		installCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s install-app %s --force",
			siteName, displayName,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", installCmd); err != nil {
			return fmt.Errorf("bench install-app %s: %w\n%s", displayName, err, out)
		}
	}

	// Compile JS/CSS bundles. Prod has always needed this; dev needs it too after
	// bind-mounted runtime bench init (commit that moved bench init out of the image):
	// without it, Desk can load as unstyled / effectively raw HTML until bench build.
	if mode == "prod" {
		step("Building production assets (bench build) — this may take a few minutes")
	} else {
		step("Building web assets (bench build) — this may take a few minutes")
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && bench build"); err != nil {
		return fmt.Errorf("bench build: %w\n%s", err, out)
	}

	// Prod: restart the frappe container so gunicorn starts fresh and picks up
	// app modules installed after initial container start. SIGHUP is insufficient:
	// the compose command is "bash -c '... && gunicorn'", so PID 1 is bash (which
	// ignores SIGHUP), and even if it reached gunicorn, new workers would still
	// fork from the master with the pre-install sys.path.
	if mode == "prod" {
		step("Restarting frappe container (picking up installed apps)")
		if err := runner.RestartService("frappe"); err != nil {
			return fmt.Errorf("restart frappe: %w", err)
		}
		step("Waiting for web server to respond...")
		url := fmt.Sprintf("http://localhost:%d", webPort)
		if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: %v\n", err)
		}
	}

	// Prod phase 2: start the remaining services (socketio, workers, scheduler)
	// now that all apps are installed and assets are built. They can now import
	// app modules without crashing.
	if mode == "prod" {
		step("Starting remaining services (socketio, workers, scheduler)")
		if err := runner.Up(); err != nil {
			return fmt.Errorf("docker compose up (remaining services): %w", err)
		}
	}

	// Dev only: start dev server + wait for HTTP + setup ffc
	ffcConfigured := false
	if mode == "dev" {
		step("Starting dev server")
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
			return fmt.Errorf("bench start: %w", err)
		}

		step("Waiting for web server to respond...")
		url := fmt.Sprintf("http://localhost:%d", webPort)
		if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: %v\n", err)
		}

		step("Generating API keys and configuring ffc")
		ffcConfigured = true
		if err := setupFfcConfig(runner, name, siteName); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n  (run 'ffc init' inside the bench shell to configure manually)\n", err)
			ffcConfigured = false
		}
	}

	// Save state
	rec := state.Bench{
		Name:          name,
		Dir:           benchDir,
		WebPort:       webPort,
		SocketIOPort:  socketIOPort,
		FrappeBranch:  frappeBranch,
		AdminPassword: adminPassword,
		DBPassword:    dbPassword,
		DBType:        dbType,
		SiteName:      siteName,
		Apps:          apps,
		ProxyHost:     resolvedProxyHost,
		Mode:          mode,
		Domain:        domain,
		CreatedAt:     time.Now(),
	}
	if err := s.AddBench(rec); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	pw.Printf("\nBench %q is ready.\n", name)
	if mode == "prod" {
		scheme := "https"
		if noSSL {
			scheme = "http"
		}
		pw.Printf("  URL:           %s://%s\n", scheme, domain)
		pw.Printf("  Site:          %s\n", siteName)
	} else {
		pw.Printf("  URL (port):    http://localhost:%d\n", webPort)
		if resolvedProxyHost != "" {
			pw.Printf("  URL (proxy):   %s\n", resolvedProxyHost)
		} else if proxy.IsRunning() {
			pw.Printf("  URL (domain):  http://%s  ← proxy is running\n", siteName)
		} else {
			pw.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", siteName)
		}
		pw.Printf("  Site:          %s\n", siteName)
	}
	pw.Printf("  Admin:         administrator / %s\n", adminPassword)
	pw.Printf("  DB (%s): root / %s\n", dbType, dbPassword)
	if len(apps) > 0 {
		pw.Printf("  Apps:          %v\n", apps)
	}
	if mode == "dev" {
		if ffcConfigured {
			pw.Printf("  ffc:           configured (run 'ffc list-docs DocType' inside the bench)\n")
		} else {
			pw.Printf("  ffc:           run 'ffc init' inside the bench shell to configure\n")
		}
		pw.Printf("  Workspace:     %s/workspace\n", benchDir)
		pw.Printf("  VS Code:       code %s  (Reopen in Container for integrated terminal)\n", benchDir)
	} else {
		pw.Printf("  Workspace:     %s/workspace\n", benchDir)
	}
	return nil
}

// setupFfcConfig generates Frappe API keys via Python inside the container and
// writes ~/.config/ffc/config.yaml. No HTTP server needs to be running.
func socketioFrappeURL(mode string) string {
	if mode == "prod" {
		return "http://frappe:8000"
	}
	return "http://127.0.0.1:8000"
}

func setupFfcConfig(runner *bench.Runner, benchName, siteName string) error {
	keys, err := runner.GenerateAdminAPIKeys(siteName)
	if err != nil {
		return err
	}

	cfg := fmt.Sprintf(
		"default_site: %s\nnumber_format: french\ndate_format: yyyy-mm-dd\nsites:\n  %s:\n    url: \"http://localhost:8000\"\n    api_key: \"%s\"\n    api_secret: \"%s\"\n",
		benchName, benchName, keys.Key, keys.Secret,
	)
	encoded := base64.StdEncoding.EncodeToString([]byte(cfg))
	cmd := fmt.Sprintf(
		"mkdir -p /home/frappe/.config/ffc && echo '%s' | base64 -d > /home/frappe/.config/ffc/config.yaml",
		encoded,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
		return fmt.Errorf("write ffc config: %w", err)
	}
	return nil
}
