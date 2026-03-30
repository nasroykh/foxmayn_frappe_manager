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
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create and start a new Frappe bench",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Show interactive form only when the user didn't pass
			// --frappe-branch or --apps explicitly.
			branchSet := cmd.Flags().Changed("frappe-branch")
			appsSet := cmd.Flags().Changed("apps")
			if !branchSet && !appsSet {
				if err := runCreateForm(&frappeBranch, &apps); err != nil {
					return err
				}
			}
			return runCreate(args[0], frappeBranch, apps, adminPassword, dbPassword, githubToken, proxyPort, proxyHost)
		},
	}

	cmd.Flags().StringVar(&frappeBranch, "frappe-branch", "version-15", "Frappe branch (version-15 or version-16)")
	cmd.Flags().StringArrayVar(&apps, "apps", nil, "Apps to install: short name (erpnext), URL (git@github.com:org/app.git), or URL@branch")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "admin", "Frappe site admin password")
	cmd.Flags().StringVar(&dbPassword, "db-password", "ffm123456", "MariaDB root password")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub personal access token for private HTTPS repos")
	cmd.Flags().IntVar(&proxyPort, "proxy-port", 0, "Configure for reverse proxy: set socketio_port to this value (e.g. 443 for HTTPS, 80 for HTTP)")
	cmd.Flags().StringVar(&proxyHost, "proxy-host", "", "Public domain for reverse proxy, e.g. frappe.example.com (sets per-site host_name)")

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

// counter provides auto-incrementing step numbers for create output.
type counter struct{ n int }

func (c *counter) step(msg string) {
	c.n++
	fmt.Printf("  [%d] %s\n", c.n, msg)
}

func runCreate(name, frappeBranch string, apps []string, adminPassword, dbPassword, githubToken string, proxyPort int, proxyHost string) (createErr error) {
	// 1. Validate name
	if err := bench.ValidateName(name); err != nil {
		return err
	}

	store := state.Default()
	if _, err := store.Get(name); err == nil {
		return fmt.Errorf("bench %q already exists", name)
	}

	fmt.Printf("Creating bench %q  (frappe: %s", name, frappeBranch)
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
	siteName := name + ".localhost"

	// Automatic cleanup on failure. Named return (createErr) means the defer
	// sees whatever error the function returns without any extra assignment at
	// each return site. It tears down containers/volumes and removes the bench
	// directory so the next retry starts from a clean state. The compose file
	// is checked before calling Down because docker compose requires it to
	// exist; if failure happened before templates were written, there is nothing
	// to bring down.
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

	// Ensure the shared ffm-proxy Docker network exists before rendering the
	// compose file. The template declares it as external:true, so docker
	// compose up would fail if the network is absent.
	s.step("Ensuring ffm-proxy network")
	if err := proxy.EnsureNetwork(); err != nil {
		return fmt.Errorf("ensure proxy network: %w", err)
	}

	// Write compose file, Dockerfile, and devcontainer config
	s.step("Writing docker-compose.yml, Dockerfile, and .devcontainer/devcontainer.json")
	data := bench.ComposeData{
		Name:                name,
		BenchDir:            benchDir,
		WebPort:             webPort,
		WebPortEnd:          webPort + 5,
		SocketIOPort:        socketIOPort,
		SocketIOPortEnd:     socketIOPort + 5,
		MariaDBRootPassword: dbPassword,
		ForwardSSHAgent:     os.Getenv("SSH_AUTH_SOCK") != "",
	}
	if err := bench.WriteCompose(benchDir, data); err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	if err := bench.WriteDockerfile(benchDir, data); err != nil {
		return fmt.Errorf("render dockerfile: %w", err)
	}
	if err := bench.WriteDevcontainer(benchDir, data); err != nil {
		return fmt.Errorf("render devcontainer: %w", err)
	}

	// Build the Docker image (tools only: zsh, starship, Go, ffc — no bench init)
	s.step("Building Docker image (tools only) — first build takes a few minutes, cached after")
	if err := runner.Build(); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}

	// Create workspace directory on host (bind-mounted into the container).
	// Remove any leftover frappe-bench from a previous partial run so bench init
	// doesn't fail with "Bench instance already exists".
	s.step("Creating workspace directory")
	workspaceDir := filepath.Join(benchDir, "workspace")
	if err := os.RemoveAll(filepath.Join(workspaceDir, "frappe-bench")); err != nil {
		return fmt.Errorf("clean workspace: %w", err)
	}
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return fmt.Errorf("create workspace dir: %w", err)
	}

	// Run bench init at runtime — populates the bind-mounted workspace on host.
	// pip/yarn download caches are persisted in named Docker volumes for speed.
	//
	// bench init is run to /tmp/ffm-bench-init (a path that never exists) to
	// avoid "Bench instance already exists" errors: the image may carry content
	// at /workspace/frappe-bench (via VOLUME or image layers) which makes that
	// path appear non-empty even when the host bind-mount directory is empty.
	// After init succeeds we copy the result into the bind-mounted location.
	//
	// bench init also exits 0 on failure, so we verify apps/ was created.
	s.step(fmt.Sprintf("Initializing bench (frappe %s) — this takes several minutes on first run", frappeBranch))
	// After copying the bench from /tmp to /workspace/frappe-bench we must
	// patch every text file in the virtualenv that still contains the old
	// absolute path. The venv was created with egg-links, direct_url.json,
	// and activate scripts that all reference /tmp/ffm-bench-init. Leaving
	// them intact causes "No module named 'frappe'" at runtime.
	// grep -rIl skips binary files; xargs -r is a no-op when grep finds nothing.
	benchInitCmd := fmt.Sprintf(
		`bench init --frappe-branch %s --skip-redis-config-generation --no-backups --verbose /tmp/ffm-bench-init`+
			` && rm -rf /workspace/frappe-bench`+
			` && cp -a /tmp/ffm-bench-init /workspace/frappe-bench`+
			` && grep -rIl '/tmp/ffm-bench-init' /workspace/frappe-bench 2>/dev/null | xargs -r sed -i 's|/tmp/ffm-bench-init|/workspace/frappe-bench|g'`+
			` && rm -rf /tmp/ffm-bench-init`+
			` && mkdir -p /workspace/frappe-bench/.agents/skills /workspace/frappe-bench/.claude/skills`+
			` && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.agents/skills/`+
			` && cp -r /opt/frappe-skills/skills/source/. /workspace/frappe-bench/.claude/skills/`+
			` && mkdir -p /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli`+
			` && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.agents/skills/foxmayn-frappe-cli/`+
			` && cp /opt/ffc-skill/SKILL.md /workspace/frappe-bench/.claude/skills/foxmayn-frappe-cli/`,
		frappeBranch,
	)
	if err := runner.Run("frappe", "bash", "-c", benchInitCmd); err != nil {
		return fmt.Errorf("bench init: %w", err)
	}
	// bench init exits 0 even on failure; verify the bench was actually created.
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

	// Configure common_site_config (single exec to avoid 6 roundtrips)
	s.step("Configuring site settings")
	socketIOPortCfg := 9000
	if proxyPort > 0 {
		socketIOPortCfg = proxyPort
	}
	configCmd := "cd /workspace/frappe-bench" +
		" && bench set-config -g db_host mariadb" +
		" && bench set-config -gp db_port 3306" +
		" && bench set-config -g redis_cache redis://redis-cache:6379" +
		" && bench set-config -g redis_queue redis://redis-queue:6379" +
		" && bench set-config -g redis_socketio redis://redis-queue:6379" +
		fmt.Sprintf(" && bench set-config -gp socketio_port %d", socketIOPortCfg)
	if proxyPort == 443 {
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

	// Enable developer mode
	s.step("Enabling developer mode")
	devModeCmd := fmt.Sprintf(
		"cd /workspace/frappe-bench && bench --site %s set-config developer_mode 1 && bench use %s",
		siteName, siteName,
	)
	if out, err := runner.ExecSilent("frappe", "bash", "-c", devModeCmd); err != nil {
		return fmt.Errorf("enable developer mode: %w\n%s", err, out)
	}

	// Set per-site host_name when a public domain is provided.
	resolvedProxyHost := ""
	if proxyHost != "" {
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

	// Configure GitHub credentials if a token was provided (used for private HTTPS repos).
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

	// Start dev server
	s.step("Starting dev server")
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
		return fmt.Errorf("bench start: %w", err)
	}

	// Wait for HTTP
	s.step("Waiting for web server to respond...")
	url := fmt.Sprintf("http://localhost:%d", webPort)
	if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "\nwarning: %v\n", err)
	}

	// Generate API keys and write ffc config inside the container
	s.step("Generating API keys and configuring ffc")
	ffcConfigured := true
	if err := setupFfcConfig(runner, name, siteName); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: %v\n  (run 'ffc init' inside the bench shell to configure manually)\n", err)
		ffcConfigured = false
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
		CreatedAt:     time.Now(),
	}
	if err := store.Add(rec); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Printf("\nBench %q is ready.\n", name)
	fmt.Printf("  URL (port):    http://localhost:%d\n", webPort)
	if resolvedProxyHost != "" {
		fmt.Printf("  URL (proxy):   %s\n", resolvedProxyHost)
	} else if proxy.IsRunning() {
		fmt.Printf("  URL (domain):  http://%s  ← proxy is running\n", siteName)
	} else {
		fmt.Printf("  URL (domain):  http://%s  ← run 'ffm proxy start' to enable\n", siteName)
	}
	fmt.Printf("  Site:          %s\n", siteName)
	fmt.Printf("  Admin:         administrator / %s\n", adminPassword)
	fmt.Printf("  DB root:       %s\n", dbPassword)
	if len(apps) > 0 {
		fmt.Printf("  Apps:          %v\n", apps)
	}
	if ffcConfigured {
		fmt.Printf("  ffc:      configured (run 'ffc list-docs DocType' inside the bench)\n")
	} else {
		fmt.Printf("  ffc:      run 'ffc init' inside the bench shell to configure\n")
	}
	fmt.Printf("  Workspace: %s/workspace\n", benchDir)
	fmt.Printf("  VS Code:   code %s  (Reopen in Container for integrated terminal)\n", benchDir)
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
