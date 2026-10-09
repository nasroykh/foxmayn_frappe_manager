package manager

import (
	"crypto/rand"
	"encoding/hex"
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
	if frappeBranch == "" {
		frappeBranch = bench.DefaultFrappeBranch
	}
	frappeRepo := in.FrappeRepo
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
	// Recreate replays an existing bench's own passwords, which already
	// worked; only new ones are checked.
	if !in.recreating {
		if err := bench.ValidateDBPassword(dbPassword); err != nil {
			return err
		}
		if err := bench.ValidateAdminPassword(adminPassword); err != nil {
			return err
		}
	}

	bind := in.Bind
	if bind == "" {
		bind = state.BindLoopback
	}
	if bind != state.BindLoopback && bind != state.BindLAN {
		return fmt.Errorf("invalid bind %q: must be %q or %q", bind, state.BindLoopback, state.BindLAN)
	}
	// New benches only: recreate replays a bench that already exists, and
	// restore carries an archived password the user may not be able to change
	// until the bench is up.
	newBench := !in.recreating && !in.SkipAppInstall
	if newBench && bind == state.BindLAN && adminPassword == defaultAdminPassword {
		return fmt.Errorf("--lan publishes the bench to other machines; the default admin password is not allowed — set --admin-password")
	}
	if mode == "dev" && len(in.DomainAliases) > 0 && bind != state.BindLAN {
		return fmt.Errorf("a dev bench with domain aliases needs --lan: the browser reaches socket.io on the published port, which is otherwise bound to 127.0.0.1")
	}
	sshAgent := in.SSHAgent && mode == "dev"
	agent := in.Agent || in.AgentReadOnly
	agentReadOnly := in.AgentReadOnly
	if agent {
		switch {
		case mode != "dev":
			return fmt.Errorf("--agent is for dev benches")
		case bind == state.BindLAN:
			return fmt.Errorf("--agent keeps the bench's ports on 127.0.0.1; drop --lan")
		case in.SSHAgent:
			return fmt.Errorf("--agent does not forward your SSH agent into the bench; drop --ssh-agent")
		}
		if newBench && adminPassword == defaultAdminPassword {
			generated, err := randomPassword()
			if err != nil {
				return err
			}
			adminPassword = generated
		}
	}
	if sshAgent && os.Getenv("SSH_AUTH_SOCK") == "" {
		return fmt.Errorf("--ssh-agent needs a running SSH agent (SSH_AUTH_SOCK is not set)")
	}
	if newBench && mode == "prod" && dbPassword == defaultDBPassword {
		generated, err := randomPassword()
		if err != nil {
			return err
		}
		dbPassword = generated
	}

	// Prod-specific validation
	if mode == "prod" {
		if domain == "" {
			return fmt.Errorf("--domain is required for production mode (e.g. --domain erp.example.com)")
		}
		// The domain becomes the site name, a directory name, shell arguments
		// and a Traefik rule between backticks, so it must be a hostname. Restore
		// already applied this to archived domains; create did not.
		// The NORMALISED form is kept: validating a trimmed, lower-cased copy
		// and then using the raw value let "erp.example.com " through, with a
		// trailing space that no Traefik Host() rule ever matches.
		if !in.recreating {
			normalized, err := bench.NormalizeDomain(domain)
			if err != nil {
				return fmt.Errorf("--domain: %w", err)
			}
			domain = normalized
		}
		if adminPassword == defaultAdminPassword {
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
	if !in.recreating {
		if err := bench.ValidateNewName(name); err != nil {
			return err
		}
	}

	// Domain aliases. Normalised up front so a bad hostname fails before any
	// container is built, and so the values written into Traefik labels are
	// always the validated form.
	primaryHost := name + ".localhost"
	if mode == "prod" {
		primaryHost = domain
	}
	aliases, err := normalizeAliases(in.DomainAliases, primaryHost)
	if err != nil {
		return err
	}
	aliasTLS := in.AliasTLS
	if aliasTLS {
		if mode != "prod" {
			return fmt.Errorf("--alias-tls requires --mode prod (dev benches are served over plain HTTP)")
		}
		if noSSL {
			return fmt.Errorf("--alias-tls cannot be combined with --no-ssl")
		}
	}
	if len(aliases) > 0 {
		if err := s.checkAliasesFree(aliases, name); err != nil {
			return err
		}
	}

	// A new bench holds its name's lock for the whole build, so a second
	// create of the same name (another terminal, the dashboard) is refused
	// instead of both passing the existence check below and building into
	// the same directory. Recreate and restore already hold it, and the lock
	// is not re-entrant.
	if newBench {
		release, err := s.lockBench(name)
		if err != nil {
			return err
		}
		defer release()
	}

	s.lock()
	_, existsErr := s.Store.Get(name)
	s.unlock()
	if existsErr == nil {
		return fmt.Errorf("bench %q already exists", name)
	}

	// Parse frappeRepo for optional @branch suffix (same syntax as --apps).
	frappeRepoURL := frappeRepo
	frappeInitBranch := frappeBranch
	if frappeRepo != "" {
		spec := bench.ParseAppSpec(frappeRepo, "")
		frappeRepoURL = spec.Source
		if spec.Branch != "" {
			frappeInitBranch = spec.Branch
		}
	}

	toolchain := bench.ToolchainFor(frappeInitBranch)
	if in.Python != "" {
		toolchain.Python = in.Python
	}
	if in.Node != "" {
		toolchain.Node = in.Node
	}
	if err := toolchain.ValidateFor(frappeInitBranch); err != nil {
		return err
	}

	frappeSrc := frappeInitBranch
	if frappeRepoURL != "" {
		frappeSrc = frappeRepoURL + "@" + frappeInitBranch
	}
	pw.Printf("Creating bench %q  (frappe: %s  python: %s  node: %s  mode: %s", name, frappeSrc, toolchain.Python, toolchain.Node, mode)
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
	runner.Redact = []string{adminPassword, dbPassword, githubToken}

	// Site name: domain for prod, <name>.localhost for dev
	siteName := name + ".localhost"
	if mode == "prod" {
		siteName = domain
	}

	// Automatic cleanup on failure, unless the caller asked to keep the wreckage
	// for diagnosis (unattended runs, where the rollback would otherwise delete
	// the only evidence of why create failed).
	keepOnFailure := in.KeepOnFailure || os.Getenv("FFM_KEEP_ON_FAILURE") != ""
	defer func() {
		if createErr == nil {
			return
		}
		composePath := filepath.Join(benchDir, "docker-compose.yml")
		if _, statErr := os.Stat(composePath); statErr == nil {
			// Dump container logs so the user can diagnose startup failures
			// (e.g. bad MariaDB flags) before the containers are torn down.
			dbService := "mariadb"
			if dbType == "postgres" {
				dbService = "postgres"
			}
			for _, svc := range []string{dbService, "frappe"} {
				fmt.Fprintf(os.Stderr, "\n--- %s container logs ---\n", svc)
				fmt.Fprintln(os.Stderr, runner.LogsString(svc))
				fmt.Fprintln(os.Stderr, "--- end logs ---")
			}
		}

		if keepOnFailure {
			// State is only saved on success, so this bench is not tracked and
			// `ffm delete` cannot find it. Print the literal teardown command.
			fmt.Fprintf(os.Stderr, "\nCreate failed — leaving containers and %s in place (--keep-on-failure).\n", benchDir)
			fmt.Fprintf(os.Stderr, "Clean up with:\n  docker compose -p %s -f %s/docker-compose.yml down -v && rm -rf %s\n",
				bench.ProjectName(name), benchDir, benchDir)
			return
		}

		fmt.Fprintln(os.Stderr, "\nCreate failed — cleaning up...")
		if _, statErr := os.Stat(composePath); statErr == nil {
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
	hostUID, hostGID, err := composeUserIDs(in.MatchHostUser)
	if err != nil {
		return err
	}
	data := bench.ComposeData{
		Name:              name,
		Mode:              mode,
		BenchDir:          benchDir,
		HostUID:           hostUID,
		HostGID:           hostGID,
		NodeMajor:         toolchain.Node,
		WebPort:           webPort,
		WebPortEnd:        webPort + 5,
		SocketIOPort:      socketIOPort,
		SocketIOPortEnd:   socketIOPort + 5,
		DBType:            dbType,
		DBRootPassword:    dbPassword,
		ForwardSSHAgent:   sshAgent,
		PublishHost:       state.Bench{Mode: mode, Bind: bind}.PublishHost(),
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
		MariaDBFastCommit: in.MariaDBFastCommit && mode == "prod",
		DomainAliases:     aliases,
		AliasTLS:          aliasTLS,
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

	// A seed replaces bench init, get-app and bench build. Restore pins
	// commits and unpacks archived source afterwards, so it may use a seed
	// but never takes one.
	seeds := seedsEnabled() && !in.NoSeed
	key := seedKeyFor(frappeRepoURL, frappeInitBranch, toolchain, hostUID, hostGID, apps, frappeBranch)
	seedTree, seedInfo := "", (*seedMeta)(nil)
	if seeds {
		seedTree, seedInfo = freshSeed(key, s.clock())
	}
	frappeBench := filepath.Join(workspaceDir, "frappe-bench")
	if seedInfo != nil {
		step(fmt.Sprintf("Copying the bench from a seed taken %s from %q (--no-seed for the branch heads)",
			seedInfo.CreatedAt.Local().Format("2006-01-02 15:04"), seedInfo.From))
		if err := copyTree(seedTree, frappeBench); err != nil {
			return fmt.Errorf("copy the seed: %w", err)
		}
		if mode == "dev" {
			if err := runner.Run("frappe", "bash", "-c", bench.InstallSkillsCmd); err != nil {
				return fmt.Errorf("install skills: %w", err)
			}
		}
	} else if err := s.benchInit(runner, mode, frappeInitBranch, frappeRepoURL, frappeSrc, toolchain, githubToken, workspaceDir, pw); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(frappeBench, "apps")); err != nil {
		return fmt.Errorf("bench init failed silently — no apps/ directory found at %s", frappeBench)
	}
	if seedInfo == nil && seeds {
		// Keep common_site_config.json as bench init wrote it, for the seed.
		if raw, err := os.ReadFile(filepath.Join(frappeBench, "sites", "common_site_config.json")); err == nil {
			_ = os.WriteFile(filepath.Join(frappeBench, seedCommonConfig), raw, 0o644)
		}
	}
	defer os.Remove(filepath.Join(frappeBench, seedCommonConfig))

	if err := s.afterBenchInit(benchDir, workspaceDir, name, siteName, mode); err != nil {
		return err
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

	// Wait for the database — both from its own container and from the frappe
	// container, which is the one that actually has to reach it.
	if dbType == "postgres" {
		step("Waiting for PostgreSQL to be ready...")
	} else {
		step("Waiting for MariaDB to be ready...")
	}
	if err := waitForDBReady(runner, dbType == "postgres", dbPassword, os.Stderr); err != nil {
		return err
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
			bench.ShellQuote(siteName), bench.ShellQuote(dbPassword), bench.ShellQuote(adminPassword),
		)
	} else {
		newSiteCmd = fmt.Sprintf(
			"cd /workspace/frappe-bench && bench new-site %s --mariadb-root-password %s --admin-password %s --no-mariadb-socket",
			bench.ShellQuote(siteName), bench.ShellQuote(dbPassword), bench.ShellQuote(adminPassword),
		)
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c", newSiteCmd); err != nil {
		return fmt.Errorf("bench new-site: %w\n%s", err, out)
	}

	step("Setting default site")
	useSiteCmd := "cd /workspace/frappe-bench && bench use " + bench.ShellQuote(siteName)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", useSiteCmd); err != nil {
		return fmt.Errorf("bench use: %w\n%s", err, out)
	}
	// Frappe creates encryption_key the first time a process needs it. On
	// v16 a request process keeps site_config cached for 60 s, so the dev
	// server, holding a copy without the key, minted its own and overwrote
	// the one the API-key script had just used: every fresh v16 bench failed
	// ffc setup with "Encryption key is invalid". Minting it here, before any
	// server runs, gives every process the same key.
	if out, err := runSiteScript(runner, siteName, mintEncryptionKeyScript); err != nil {
		return fmt.Errorf("create the site's encryption key: %w\n%s", err, lastLines(out, 5))
	}

	// Developer mode (dev only)
	if mode == "dev" {
		step("Enabling developer mode")
		devModeCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s set-config developer_mode 1",
			bench.ShellQuote(siteName),
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", devModeCmd); err != nil {
			return fmt.Errorf("enable developer mode: %w\n%s", err, out)
		}
		if err := ensureDevMail(state.Bench{Mode: mode, Dir: benchDir, SiteName: siteName,
			TemplateVersion: bench.TemplateVersion}); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not point outgoing mail at Mailpit: %v\n", err)
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
			bench.ShellQuote(siteName), bench.ShellQuote(resolvedProxyHost),
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", hostCmd); err != nil {
			return fmt.Errorf("set host_name: %w\n%s", err, out)
		}
		// A new site keeps its scheduler off until the setup wizard runs, so a
		// production bench would run no scheduled jobs (backups, emails, ...).
		step("Enabling the scheduler")
		schedCmd := fmt.Sprintf("cd /workspace/frappe-bench && bench --site %s enable-scheduler",
			bench.ShellQuote(siteName))
		if out, err := runner.ExecSilent("frappe", "bash", "-c", schedCmd); err != nil {
			return fmt.Errorf("enable scheduler: %w\n%s", err, out)
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
			bench.ShellQuote(siteName), bench.ShellQuote(resolvedProxyHost),
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

		if seedInfo == nil {
			step(fmt.Sprintf("Getting app %q (branch: %s) — may take a few minutes", displayName, branchDesc))
			if out, err := runner.ExecSilent("frappe", "bash", "-c",
				"cd /workspace/frappe-bench && "+spec.GetAppCmd()); err != nil {
				return fmt.Errorf("bench get-app %s: %w\n%s", displayName, err, out)
			}
		}

		if in.SkipAppInstall {
			// Restore path: the dump already has the app installed, so this
			// would be undone moments later.
			step(fmt.Sprintf("Skipping install of %q (it comes from the archive)", displayName))
			continue
		}

		step(fmt.Sprintf("Installing app %q on site %q", displayName, siteName))
		installCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s install-app %s --force",
			bench.ShellQuote(siteName), bench.ShellQuote(displayName),
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", installCmd); err != nil {
			return fmt.Errorf("bench install-app %s: %w\n%s", displayName, err, out)
		}
	}

	// Compile JS/CSS bundles. Prod has always needed this; dev needs it too after
	// bind-mounted runtime bench init (commit that moved bench init out of the image):
	// without it, Desk can load as unstyled / effectively raw HTML until bench build.
	if in.SkipAssetBuild {
		// Restore path: the assets are built once after the data lands, rather
		// than here and then again after bench migrate.
		step("Skipping asset build (the restore builds once, after migrating)")
	} else if seedInfo != nil {
		step("Using the seed's built assets")
	} else {
		if mode == "prod" {
			step("Building production assets (bench build) — this may take a few minutes")
		} else {
			step("Building web assets (bench build) — this may take a few minutes")
		}
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && bench build"); err != nil {
			return fmt.Errorf("bench build: %w\n%s", err, out)
		}
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
			// Not fatal: the bench itself is built and the rollback defer would
			// destroy it over a web server that may still be coming up. Say why
			// rather than leaving the user to go looking.
			fmt.Fprintf(os.Stderr, "\nwarning: %v\n", webServerUnreachable(runner, false, err))
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
			bench.DevServerStartCmd); err != nil {
			return fmt.Errorf("bench start: %w", err)
		}

		step("Waiting for web server to respond...")
		url := fmt.Sprintf("http://localhost:%d", webPort)
		if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: %v\n", webServerUnreachable(runner, true, err))
		}

		step("Generating API keys and configuring ffc")
		ffcConfigured = true
		access := state.Bench{Name: name, Dir: benchDir, Mode: mode, SiteName: siteName, WebPort: webPort,
			FrappeBranch: frappeBranch, Python: toolchain.Python, Node: toolchain.Node,
			Agent: agent, AgentReadOnly: agentReadOnly}
		if _, err := setupBenchAccess(runner, access); err != nil {
			fmt.Fprintf(os.Stderr, "  warning: %v\n  (run 'ffm ffc %s' once the bench is up)\n", err, name)
			ffcConfigured = false
		}
	}

	// Save state
	rec := state.Bench{
		ProjectFile:   in.ProjectFile,
		Name:          name,
		Dir:           benchDir,
		WebPort:       webPort,
		SocketIOPort:  socketIOPort,
		FrappeBranch:  frappeBranch,
		FrappeRepo:    frappeRepo,
		Python:        toolchain.Python,
		Node:          toolchain.Node,
		AdminPassword: adminPassword,
		DBPassword:    dbPassword,
		DBType:        dbType,
		SiteName:      siteName,
		Apps:          apps,
		ProxyHost:     resolvedProxyHost,
		Mode:          mode,
		Domain:        domain,
		DomainAliases: aliases,
		AliasTLS:      aliasTLS,
		MatchHostUser: in.MatchHostUser,
		Bind:          bind,
		SSHAgent:      sshAgent,
		Agent:         agent,
		AgentReadOnly: agentReadOnly,
		// The compose file was rendered from this build's templates.
		TemplateVersion: bench.TemplateVersion,
		CreatedAt:       time.Now(),
		// Everything below is what a later compose re-render needs in order to
		// reproduce this exact bench. Without it, recreate and `ffm domain`
		// would silently reset the prod tuning knobs to their defaults.
		TLSMode:           tlsModeFor(mode, noSSL),
		MariaDBBufferPool: mariadbBufferPool,
		GunicornWorkers:   gunicornWorkers,
		WorkerLongCount:   workerLongCount,
		WorkerShortCount:  workerShortCount,
		RedisCacheMaxmem:  redisCacheMaxmem,
		RedisQueueMaxmem:  redisQueueMaxmem,
		SlowQueryLog:      data.SlowQueryLog,
		MariaDBFastCommit: data.MariaDBFastCommit,
	}
	if err := s.AddBench(rec); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	if seeds && seedInfo == nil && !in.SkipAppInstall {
		step("Saving this bench's tree as a seed for the next bench with the same inputs")
		if err := captureSeed(key, frappeBench, name, s.clock()); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: could not save a seed: %v\n", err)
		}
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

func socketioFrappeURL(mode string) string {
	if mode == "prod" {
		return "http://frappe:8000"
	}
	return "http://127.0.0.1:8000"
}

// Defaults the CLI and dashboard offer. Prod refuses the admin one and replaces
// the DB one with a random password.
const (
	defaultAdminPassword = "admin"
	defaultDBPassword    = "ffm123456"
)

// randomPassword returns 24 hex characters: safe for ValidateDBPassword, for
// double-quoted YAML and for shell arguments.
func randomPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// benchInitArgs returns the source and toolchain arguments of bench init.
// --python picks the virtualenv's interpreter; Node comes from the image.
func benchInitArgs(branch, repoURL string, tc bench.Toolchain) string {
	args := "--frappe-branch " + bench.ShellQuote(branch) + " --python " + bench.ShellQuote(tc.PythonBin())
	if repoURL != "" {
		args += " --frappe-path " + bench.ShellQuote(repoURL)
	}
	return args
}

// benchInit runs bench init in a one-off frappe container, into /tmp and then
// copied to the workspace with its paths rewritten. Dev benches also get the
// agent skills.
func (s *Service) benchInit(runner *bench.Runner, mode, branch, repoURL, src string, tc bench.Toolchain,
	githubToken, workspaceDir string, pw ProgressWriter) error {
	pw.Step(fmt.Sprintf("Initializing bench (frappe %s) — this takes several minutes on first run", src))
	baseInit := fmt.Sprintf(
		`bench init %s --skip-redis-config-generation --no-backups --verbose /tmp/ffm-bench-init`+
			` && rm -rf /workspace/frappe-bench`+
			` && cp -a /tmp/ffm-bench-init /workspace/frappe-bench`+
			` && grep -rIl '/tmp/ffm-bench-init' /workspace/frappe-bench 2>/dev/null | xargs -r sed -i 's|/tmp/ffm-bench-init|/workspace/frappe-bench|g'`+
			` && rm -rf /tmp/ffm-bench-init`,
		benchInitArgs(branch, repoURL, tc),
	)
	benchInitCmd := baseInit
	if mode == "dev" {
		benchInitCmd = baseInit + " && " + bench.InstallSkillsCmd
	}
	// When a GitHub token is provided and bench init must clone a private HTTPS repo,
	// inject credentials directly into the one-off run container. ConfigureGitHubToken
	// uses docker compose exec (needs a running container) so it cannot cover this step.
	if githubToken != "" {
		benchInitCmd = bench.GitCredentialsCmd(githubToken) + " && " + benchInitCmd
	}
	if err := runner.Run("frappe", "bash", "-c", benchInitCmd); err != nil {
		return fmt.Errorf("bench init: %w", err)
	}
	return nil
}

// afterBenchInit writes the files and patches a new bench tree needs, whether
// it came from bench init or from a seed.
func (s *Service) afterBenchInit(benchDir, workspaceDir, name, siteName, mode string) error {
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
		if err := writeClaudeMcpConfigHost(frappeBench, name, false); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write Claude Code .mcp.json (ffc MCP): %v\n", err)
		}
		// Make the RQ worker self-restart so an idle Redis timeout (which exits
		// the worker rc=0) doesn't make honcho tear down the whole dev stack.
		if err := bench.PatchProcfileWorker(benchDir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not patch Procfile worker: %v\n", err)
		}
	}
	return nil
}
