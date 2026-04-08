package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newCreateCmd() *cobra.Command {
	var (
		frappeBranch  string
		apps          []string
		adminPassword string
		dbPassword    string
		githubToken   string
		proxyPort     int
		proxyHost     string
		mode          string
		domain        string
		noSSL         bool
		acmeEmail     string
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create and start a new Frappe bench",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modeSet := cmd.Flags().Changed("mode")
			branchSet := cmd.Flags().Changed("frappe-branch")
			appsSet := cmd.Flags().Changed("apps")

			if !modeSet {
				// No --mode flag: show full interactive form (asks dev or prod first).
				if err := runCreateFormFull(&mode, &frappeBranch, &apps, &domain, &acmeEmail, &noSSL, &adminPassword); err != nil {
					return err
				}
			} else if mode == "dev" && !branchSet && !appsSet {
				// Explicit --mode dev but no branch/apps: show dev-only form.
				if err := runCreateForm(&frappeBranch, &apps); err != nil {
					return err
				}
			}
			return runCreate(args[0], frappeBranch, apps, adminPassword, dbPassword, githubToken, proxyPort, proxyHost, mode, domain, noSSL, acmeEmail)
		},
	}

	cmd.Flags().StringVar(&frappeBranch, "frappe-branch", "version-15", "Frappe branch (version-15 or version-16)")
	cmd.Flags().StringArrayVar(&apps, "apps", nil, "Apps to install: short name (erpnext), URL (git@github.com:org/app.git), or URL@branch")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "admin", "Frappe site admin password")
	cmd.Flags().StringVar(&dbPassword, "db-password", "ffm123456", "MariaDB root password")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub personal access token for private HTTPS repos")
	cmd.Flags().IntVar(&proxyPort, "proxy-port", 0, "Configure for reverse proxy: set socketio_port to this value (e.g. 443 for HTTPS, 80 for HTTP)")
	cmd.Flags().StringVar(&proxyHost, "proxy-host", "", "Public domain for reverse proxy, e.g. frappe.example.com (sets per-site host_name)")
	cmd.Flags().StringVar(&mode, "mode", "dev", "Bench mode: dev or prod")
	cmd.Flags().StringVar(&domain, "domain", "", "Public domain for production (required for --mode prod), e.g. erp.example.com")
	cmd.Flags().BoolVar(&noSSL, "no-ssl", false, "Skip Let's Encrypt SSL in production (handle TLS externally via Caddy/Nginx)")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "Email for Let's Encrypt certificates (required on first --mode prod bench with SSL)")

	return cmd
}

// runCreateForm shows an interactive TUI to choose Frappe version and apps.
func runCreateForm(branch *string, apps *[]string) error {
	versionOptions := []huh.Option[string]{
		huh.NewOption("Frappe v15 (stable)", "version-15"),
		huh.NewOption("Frappe v16 (latest)", "version-16"),
	}

	appOptions := []huh.Option[string]{
		huh.NewOption("ERPNext", "erpnext"),
		huh.NewOption("HRMS", "hrms"),
	}

	// Defaults
	*branch = "version-15"

	var customAppsRaw string

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Frappe version").
				Options(versionOptions...).
				Value(branch),
			huh.NewMultiSelect[string]().
				Title("Additional apps to install").
				Description("Space to toggle, enter to confirm. Leave empty for a bare Frappe bench.").
				Options(appOptions...).
				Value(apps),
			huh.NewInput().
				Title("Custom app (optional)").
				Description("Short name, git URL, or url@branch. Comma-separated for multiple.\nExamples: mypkg, git@github.com:org/app.git@main, https://github.com/org/app").
				Value(&customAppsRaw),
		),
	).Run()
	if err != nil {
		return err
	}

	for raw := range strings.SplitSeq(customAppsRaw, ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			*apps = append(*apps, raw)
		}
	}
	return nil
}

// runCreateFormFull is the interactive form shown when --mode is not passed.
// It asks dev or prod first, then shows the relevant follow-up fields.
func runCreateFormFull(mode, branch *string, apps *[]string, domain, acmeEmail *string, noSSL *bool, adminPassword *string) error {
	*mode = "dev"
	*branch = "version-15"

	// Step 1: choose mode
	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Bench mode").
				Options(
					huh.NewOption("Development  (local bench, dev server, Claude skills)", "dev"),
					huh.NewOption("Production   (gunicorn + workers, Traefik + Let's Encrypt)", "prod"),
				).
				Value(mode),
		),
	).Run()
	if err != nil {
		return err
	}

	if *mode == "dev" {
		return runCreateForm(branch, apps)
	}

	// Step 2 (prod): domain, SSL, branch, apps
	versionOptions := []huh.Option[string]{
		huh.NewOption("Frappe v15 (stable)", "version-15"),
		huh.NewOption("Frappe v16 (latest)", "version-16"),
	}
	appOptions := []huh.Option[string]{
		huh.NewOption("ERPNext", "erpnext"),
		huh.NewOption("HRMS", "hrms"),
	}
	var customAppsRaw string
	savedEmail := readSavedAcmeEmail()
	if savedEmail != "" {
		*acmeEmail = savedEmail
	}

	err = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Public domain").
				Description("The domain this site will be served on, e.g. erp.example.com").
				Value(domain),
			huh.NewInput().
				Title("Admin password").
				Description("Strong password for the Administrator account. Cannot be 'admin'.").
				EchoMode(huh.EchoModePassword).
				Value(adminPassword),
			huh.NewInput().
				Title("Let's Encrypt email").
				Description("Required for automatic SSL certificates. Leave empty to skip SSL (--no-ssl).").
				Value(acmeEmail),
			huh.NewSelect[string]().
				Title("Frappe version").
				Options(versionOptions...).
				Value(branch),
			huh.NewMultiSelect[string]().
				Title("Additional apps to install").
				Description("Space to toggle, enter to confirm. Leave empty for a bare Frappe bench.").
				Options(appOptions...).
				Value(apps),
			huh.NewInput().
				Title("Custom app (optional)").
				Description("Short name, git URL, or url@branch. Comma-separated for multiple.").
				Value(&customAppsRaw),
		),
	).Run()
	if err != nil {
		return err
	}

	// Empty acmeEmail means --no-ssl
	if *acmeEmail == "" {
		*noSSL = true
	}

	for raw := range strings.SplitSeq(customAppsRaw, ",") {
		if raw = strings.TrimSpace(raw); raw != "" {
			*apps = append(*apps, raw)
		}
	}
	return nil
}

// counter provides auto-incrementing step numbers for create output.
type counter struct{ n int }

func (c *counter) step(msg string) {
	c.n++
	fmt.Printf("  [%d] %s\n", c.n, msg)
}

// readSavedAcmeEmail reads the stored ACME email from ~/.config/ffm/.acme_email.
// Returns empty string if the file does not exist.
func readSavedAcmeEmail() string {
	data, err := os.ReadFile(config.AcmeEmailFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// saveAcmeEmail persists the ACME email to ~/.config/ffm/.acme_email.
func saveAcmeEmail(email string) {
	_ = os.WriteFile(config.AcmeEmailFile(), []byte(email+"\n"), 0o600)
}

func runCreate(name, frappeBranch string, apps []string, adminPassword, dbPassword, githubToken string, proxyPort int, proxyHost string, mode, domain string, noSSL bool, acmeEmail string) (createErr error) {
	// Validate mode
	if mode != "dev" && mode != "prod" {
		return fmt.Errorf("invalid --mode %q: must be 'dev' or 'prod'", mode)
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
				acmeEmail = readSavedAcmeEmail()
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

	store := state.Default()
	if _, err := store.Get(name); err == nil {
		return fmt.Errorf("bench %q already exists", name)
	}

	fmt.Printf("Creating bench %q  (frappe: %s  mode: %s", name, frappeBranch, mode)
	if mode == "prod" {
		fmt.Printf("  domain: %s", domain)
	}
	if len(apps) > 0 {
		fmt.Printf("  apps: %v", apps)
	}
	fmt.Println(")")

	s := &counter{}

	// Allocate ports
	s.step("Allocating ports")
	webPort, socketIOPort, err := bench.AllocatePorts(store)
	if err != nil {
		return fmt.Errorf("port allocation: %w", err)
	}

	benchDir := config.BenchDir(name)
	runner := bench.NewRunner(name, benchDir, verbose)

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
			if downErr := runner.Down(true); downErr != nil && verbose {
				fmt.Fprintf(os.Stderr, "  cleanup: docker compose down: %v\n", downErr)
			}
		}
		if rmErr := os.RemoveAll(benchDir); rmErr != nil && verbose {
			fmt.Fprintf(os.Stderr, "  cleanup: remove bench dir: %v\n", rmErr)
		}
	}()

	// Create bench directory
	s.step("Creating bench directory")
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return fmt.Errorf("create bench dir: %w", err)
	}

	// Ensure shared ffm-proxy Docker network (and HTTPS support for prod+SSL)
	if mode == "prod" && !noSSL {
		s.step("Ensuring HTTPS proxy (Let's Encrypt)")
		if err := proxy.EnsureHTTPS(acmeEmail); err != nil {
			return fmt.Errorf("ensure HTTPS proxy: %w", err)
		}
		saveAcmeEmail(acmeEmail)
	} else {
		s.step("Ensuring ffm-proxy network")
		if err := proxy.EnsureNetwork(); err != nil {
			return fmt.Errorf("ensure proxy network: %w", err)
		}
	}

	// Write compose file, Dockerfile, and (dev only) devcontainer config
	s.step("Writing docker-compose.yml and Dockerfile")
	data := bench.ComposeData{
		Name:                name,
		Mode:                mode,
		BenchDir:            benchDir,
		WebPort:             webPort,
		WebPortEnd:          webPort + 5,
		SocketIOPort:        socketIOPort,
		SocketIOPortEnd:     socketIOPort + 5,
		MariaDBRootPassword: dbPassword,
		ForwardSSHAgent:     mode == "dev" && os.Getenv("SSH_AUTH_SOCK") != "",
		Domain:              domain,
		NoSSL:               noSSL,
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
	s.step("Building Docker image — first build takes a few minutes, cached after")
	if err := runner.Build(); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}

	// Create workspace directory
	s.step("Creating workspace directory")
	workspaceDir := filepath.Join(benchDir, "workspace")
	if err := os.RemoveAll(filepath.Join(workspaceDir, "frappe-bench")); err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	// Run bench init — dev mode also installs Claude/agent skills
	s.step(fmt.Sprintf("Initializing bench (frappe %s) — this takes several minutes on first run", frappeBranch))
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

	// Start containers
	s.step("Starting containers (docker compose up)")
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// Wait for MariaDB
	s.step("Waiting for MariaDB to be ready...")
	if err := runner.WaitForMariaDB(dbPassword, 90*time.Second, os.Stderr); err != nil {
		return fmt.Errorf("wait for MariaDB: %w", err)
	}
	fmt.Println()

	// Configure common_site_config
	s.step("Configuring site settings")
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
	configCmd := "cd /workspace/frappe-bench" +
		" && bench set-config -g db_host mariadb" +
		" && bench set-config -gp db_port 3306" +
		" && bench set-config -g redis_cache redis://redis-cache:6379" +
		" && bench set-config -g redis_queue redis://redis-queue:6379" +
		" && bench set-config -g redis_socketio redis://redis-queue:6379" +
		fmt.Sprintf(" && bench set-config -gp socketio_port %d", socketIOPortCfg)
	if (mode == "prod" && !noSSL) || proxyPort == 443 {
		configCmd += " && bench set-config -gp use_ssl 1"
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c", configCmd); err != nil {
		return fmt.Errorf("configure site settings: %w\n%s", err, out)
	}

	// bench new-site
	s.step(fmt.Sprintf("Creating site %q", siteName))
	newSiteCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench && bench new-site %s --mariadb-root-password %s --admin-password %s --no-mariadb-socket",
		siteName, dbPassword, adminPassword,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", newSiteCmd); err != nil {
		return fmt.Errorf("bench new-site: %w\n%s", err, out)
	}

	// Developer mode (dev only)
	if mode == "dev" {
		s.step("Enabling developer mode")
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
		s.step(fmt.Sprintf("Setting host_name to %s", resolvedProxyHost))
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

		s.step(fmt.Sprintf("Setting host_name to %s", resolvedProxyHost))
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
		s.step("Configuring GitHub credentials inside container")
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

		s.step(fmt.Sprintf("Getting app %q (branch: %s) — may take a few minutes", displayName, branchDesc))
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && "+spec.GetAppCmd()); err != nil {
			return fmt.Errorf("bench get-app %s: %w\n%s", displayName, err, out)
		}

		s.step(fmt.Sprintf("Installing app %q on site %q", displayName, siteName))
		installCmd := fmt.Sprintf(
			"cd /workspace/frappe-bench && bench --site %s install-app %s --force",
			siteName, displayName,
		)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", installCmd); err != nil {
			return fmt.Errorf("bench install-app %s: %w\n%s", displayName, err, out)
		}
	}

	// Prod: build production assets
	if mode == "prod" {
		s.step("Building production assets (bench build) — this may take a few minutes")
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && bench build"); err != nil {
			return fmt.Errorf("bench build: %w\n%s", err, out)
		}
	}

	// Dev only: start dev server + wait for HTTP + setup ffc
	ffcConfigured := false
	if mode == "dev" {
		s.step("Starting dev server")
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
			return fmt.Errorf("bench start: %w", err)
		}

		s.step("Waiting for web server to respond...")
		url := fmt.Sprintf("http://localhost:%d", webPort)
		if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
			fmt.Fprintf(os.Stderr, "\nwarning: %v\n", err)
		}

		s.step("Generating API keys and configuring ffc")
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
		SiteName:      siteName,
		Apps:          apps,
		ProxyHost:     resolvedProxyHost,
		Mode:          mode,
		Domain:        domain,
		CreatedAt:     time.Now(),
	}
	if err := store.Add(rec); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Printf("\nBench %q is ready.\n", name)
	if mode == "prod" {
		scheme := "https"
		if noSSL {
			scheme = "http"
		}
		fmt.Printf("  URL:           %s://%s\n", scheme, domain)
		fmt.Printf("  Site:          %s\n", siteName)
	} else {
		fmt.Printf("  URL (port):    http://localhost:%d\n", webPort)
		if resolvedProxyHost != "" {
			fmt.Printf("  URL (proxy):   %s\n", resolvedProxyHost)
		} else if proxy.IsRunning() {
			fmt.Printf("  URL (domain):  http://%s  ← proxy is running\n", siteName)
		} else {
			fmt.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", siteName)
		}
		fmt.Printf("  Site:          %s\n", siteName)
	}
	fmt.Printf("  Admin:         administrator / %s\n", adminPassword)
	fmt.Printf("  DB root:       %s\n", dbPassword)
	if len(apps) > 0 {
		fmt.Printf("  Apps:          %v\n", apps)
	}
	if mode == "dev" {
		if ffcConfigured {
			fmt.Printf("  ffc:           configured (run 'ffc list-docs DocType' inside the bench)\n")
		} else {
			fmt.Printf("  ffc:           run 'ffc init' inside the bench shell to configure\n")
		}
		fmt.Printf("  Workspace:     %s/workspace\n", benchDir)
		fmt.Printf("  VS Code:       code %s  (Reopen in Container for integrated terminal)\n", benchDir)
	} else {
		fmt.Printf("  Workspace:     %s/workspace\n", benchDir)
	}
	return nil
}

// setupFfcConfig generates Frappe API keys via Python inside the container and
// writes ~/.config/ffc/config.yaml. No HTTP server needs to be running.
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
