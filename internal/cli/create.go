package cli

import (
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newCreateCmd() *cobra.Command {
	var (
		frappeBranch      string
		frappeRepo        string
		apps              []string
		adminPassword     string
		dbPassword        string
		dbType            string
		githubToken       string
		proxyPort         int
		proxyHost         string
		mode              string
		domain            string
		noSSL             bool
		acmeEmail         string
		mariadbBufferPool string
		gunicornWorkers   int
		workerLongCount   int
		workerShortCount  int
		redisCacheMaxmem  string
		redisQueueMaxmem  string
		slowQueryLog      bool
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
				if err := runCreateFormFull(&mode, &frappeBranch, &frappeRepo, &apps, &domain, &acmeEmail, &noSSL, &adminPassword, &dbType, &mariadbBufferPool, &gunicornWorkers, &githubToken); err != nil {
					return err
				}
			} else if mode == "dev" && !branchSet && !appsSet {
				// Explicit --mode dev but no branch/apps: show dev-only form.
				if err := runCreateForm(&frappeBranch, &frappeRepo, &apps, &dbType, &githubToken); err != nil {
					return err
				}
			}
			return manager.New(verbose).Create(manager.CreateInput{
				Name: args[0], FrappeBranch: frappeBranch, FrappeRepo: frappeRepo, Apps: apps,
				AdminPassword: adminPassword, DBPassword: dbPassword, DBType: dbType,
				GithubToken: githubToken, ProxyPort: proxyPort, ProxyHost: proxyHost,
				Mode: mode, Domain: domain, NoSSL: noSSL, AcmeEmail: acmeEmail,
				MariaDBBufferPool: mariadbBufferPool, GunicornWorkers: gunicornWorkers,
				WorkerLongCount: workerLongCount, WorkerShortCount: workerShortCount,
				RedisCacheMaxmem: redisCacheMaxmem, RedisQueueMaxmem: redisQueueMaxmem,
				SlowQueryLog: slowQueryLog,
			}, manager.CLIProgress{})
		},
	}

	cmd.Flags().StringVar(&frappeBranch, "frappe-branch", "version-15", "Frappe branch (version-15 or version-16)")
	cmd.Flags().StringVar(&frappeRepo, "frappe-repo", "", "Custom Frappe repository URL with optional @branch suffix (e.g. https://github.com/your-org/frappe.git@main). Defaults to the official frappe/frappe repo.")
	cmd.Flags().StringArrayVar(&apps, "apps", nil, "Apps to install: short name (erpnext), URL (git@github.com:org/app.git), or URL@branch")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "admin", "Frappe site admin password")
	cmd.Flags().StringVar(&dbPassword, "db-password", "ffm123456", "Database root password")
	cmd.Flags().StringVar(&dbType, "db-type", "mariadb", "Database engine: mariadb or postgres")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub personal access token for private HTTPS repos")
	cmd.Flags().IntVar(&proxyPort, "proxy-port", 0, "Configure for reverse proxy: set socketio_port to this value (e.g. 443 for HTTPS, 80 for HTTP)")
	cmd.Flags().StringVar(&proxyHost, "proxy-host", "", "Public domain for reverse proxy, e.g. frappe.example.com (sets per-site host_name)")
	cmd.Flags().StringVar(&mode, "mode", "dev", "Bench mode: dev or prod")
	cmd.Flags().StringVar(&domain, "domain", "", "Public domain for production (required for --mode prod), e.g. erp.example.com")
	cmd.Flags().BoolVar(&noSSL, "no-ssl", false, "Skip Let's Encrypt SSL in production (handle TLS externally via Caddy/Nginx)")
	cmd.Flags().StringVar(&acmeEmail, "acme-email", "", "Email for Let's Encrypt certificates (required on first --mode prod bench with SSL)")
	cmd.Flags().StringVar(&mariadbBufferPool, "mariadb-buffer-pool", "1G", "InnoDB buffer pool size for MariaDB (e.g. 512M, 1G, 2G, 4G). Prod only; dev uses 256M.")
	cmd.Flags().IntVar(&gunicornWorkers, "gunicorn-workers", 2, "Number of gunicorn worker processes (prod only). Rule of thumb: 2*CPU+1.")
	cmd.Flags().IntVar(&workerLongCount, "worker-long-replicas", 1, "Number of long-queue background worker replicas (prod only).")
	cmd.Flags().IntVar(&workerShortCount, "worker-short-replicas", 1, "Number of short-queue background worker replicas (prod only).")
	cmd.Flags().StringVar(&redisCacheMaxmem, "redis-cache-maxmem", "512mb", "Redis cache maxmemory limit (e.g. 256mb, 512mb, 1gb). Uses allkeys-lru eviction.")
	cmd.Flags().StringVar(&redisQueueMaxmem, "redis-queue-maxmem", "512mb", "Redis queue maxmemory limit. Uses noeviction so jobs are never silently dropped.")
	cmd.Flags().BoolVar(&slowQueryLog, "slow-query-log", false, "Enable MariaDB slow query log (threshold: 2s). Writes to <bench>/mysql-logs/. Prod + MariaDB only.")

	return cmd
}

// runCreateForm shows an interactive TUI to choose Frappe version, apps, and DB engine.
func runCreateForm(branch *string, frappeRepo *string, apps *[]string, dbType *string, githubToken *string) error {
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
	if *dbType == "" {
		*dbType = "mariadb"
	}

	var customAppsRaw string

	err := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Frappe version").
				Options(versionOptions...).
				Value(branch),
			huh.NewSelect[string]().
				Title("Database engine").
				Options(
					huh.NewOption("MariaDB 11.8  (stable, recommended)", "mariadb"),
					huh.NewOption("PostgreSQL 18  (experimental)", "postgres"),
				).
				Value(dbType),
			huh.NewMultiSelect[string]().
				Title("Additional apps to install").
				Description("Space to toggle, enter to confirm. Leave empty for a bare Frappe bench.").
				Options(appOptions...).
				Value(apps),
			huh.NewInput().
				Title("Custom app (optional)").
				Description("Short name, git URL, or url@branch. Comma-separated for multiple.\nExamples: mypkg, git@github.com:org/app.git@main, https://github.com/org/app").
				Value(&customAppsRaw),
			huh.NewInput().
				Title("Custom Frappe repo (optional)").
				Description("Use a fork or mirror instead of github.com/frappe/frappe. Append @branch to override the selected version.\nExamples: https://github.com/your-org/frappe.git@main, git@github.com:your-org/frappe.git@main").
				Value(frappeRepo),
			huh.NewInput().
				Title("GitHub token (optional)").
				Description("Personal access token for private HTTPS repos (Frappe fork or custom apps).").
				EchoMode(huh.EchoModePassword).
				Value(githubToken),
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
func runCreateFormFull(mode, branch *string, frappeRepo *string, apps *[]string, domain, acmeEmail *string, noSSL *bool, adminPassword, dbType, mariadbBufferPool *string, gunicornWorkers *int, githubToken *string) error {
	*mode = "dev"
	*branch = "version-15"
	if *dbType == "" {
		*dbType = "mariadb"
	}

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
		return runCreateForm(branch, frappeRepo, apps, dbType, githubToken)
	}

	// Step 2 (prod): domain, SSL, branch, apps, DB tuning
	versionOptions := []huh.Option[string]{
		huh.NewOption("Frappe v15 (stable)", "version-15"),
		huh.NewOption("Frappe v16 (latest)", "version-16"),
	}
	appOptions := []huh.Option[string]{
		huh.NewOption("ERPNext", "erpnext"),
		huh.NewOption("HRMS", "hrms"),
	}
	bufferPoolOptions := []huh.Option[string]{
		huh.NewOption("512M  (4 GB RAM server)", "512M"),
		huh.NewOption("1G    (8 GB RAM server)", "1G"),
		huh.NewOption("2G    (16 GB RAM server)", "2G"),
		huh.NewOption("4G    (32 GB RAM server)", "4G"),
		huh.NewOption("8G    (64 GB+ RAM server)", "8G"),
	}
	if *mariadbBufferPool == "" {
		*mariadbBufferPool = "1G"
	}
	gunicornOptions := []huh.Option[int]{
		huh.NewOption("1  (minimal / testing)", 1),
		huh.NewOption("2  (2–4 CPU — default)", 2),
		huh.NewOption("4  (8 CPU)", 4),
		huh.NewOption("8  (16+ CPU)", 8),
	}
	if *gunicornWorkers == 0 {
		*gunicornWorkers = 2
	}
	var customAppsRaw string
	savedEmail := manager.ReadSavedAcmeEmail()
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
			huh.NewSelect[string]().
				Title("Database engine").
				Options(
					huh.NewOption("MariaDB 11.8  (stable, recommended)", "mariadb"),
					huh.NewOption("PostgreSQL 18  (experimental)", "postgres"),
				).
				Value(dbType),
			huh.NewSelect[int]().
				Title("Gunicorn worker processes").
				Description("Rule of thumb: 2×CPU+1. More workers = more RAM usage.").
				Options(gunicornOptions...).
				Value(gunicornWorkers),
			huh.NewSelect[string]().
				Title("MariaDB buffer pool size").
				Description("InnoDB buffer pool: set to ~50–70% of server RAM. Skipped for PostgreSQL.").
				Options(bufferPoolOptions...).
				Value(mariadbBufferPool),
			huh.NewMultiSelect[string]().
				Title("Additional apps to install").
				Description("Space to toggle, enter to confirm. Leave empty for a bare Frappe bench.").
				Options(appOptions...).
				Value(apps),
			huh.NewInput().
				Title("Custom app (optional)").
				Description("Short name, git URL, or url@branch. Comma-separated for multiple.").
				Value(&customAppsRaw),
			huh.NewInput().
				Title("Custom Frappe repo (optional)").
				Description("Use a fork or mirror instead of github.com/frappe/frappe. Append @branch to override the selected version.\nExamples: https://github.com/your-org/frappe.git@main, git@github.com:your-org/frappe.git@main").
				Value(frappeRepo),
			huh.NewInput().
				Title("GitHub token (optional)").
				Description("Personal access token for private HTTPS repos (Frappe fork or custom apps).").
				EchoMode(huh.EchoModePassword).
				Value(githubToken),
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
