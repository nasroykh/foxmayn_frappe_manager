package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// knownApps lists apps that follow Frappe's branch naming convention.
// When installing these, ffm uses the same branch as the Frappe installation.
var knownApps = map[string]bool{
	"erpnext":  true,
	"hrms":     true,
	"payments": true,
	"lending":  true,
}

func appBranch(app, frappeBranch string) string {
	if knownApps[app] {
		return frappeBranch
	}
	return frappeBranch
}

func newCreateCmd() *cobra.Command {
	var (
		frappeBranch   string
		apps           []string
		adminPassword  string
		dbPassword     string
		starshipPreset string
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
				if err := runCreateForm(&frappeBranch, &apps, &starshipPreset); err != nil {
					return err
				}
			}
			return runCreate(args[0], frappeBranch, apps, adminPassword, dbPassword, starshipPreset)
		},
	}

	cmd.Flags().StringVar(&frappeBranch, "frappe-branch", "version-15", "Frappe branch (version-15 or version-16)")
	cmd.Flags().StringArrayVar(&apps, "apps", nil, "Additional apps to install (e.g. --apps erpnext)")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "admin", "Frappe site admin password")
	cmd.Flags().StringVar(&dbPassword, "db-password", "123", "MariaDB root password")
	cmd.Flags().StringVar(&starshipPreset, "starship-preset", "", "Starship prompt preset (e.g. tokyo-night, pastel-powerline)")

	return cmd
}

// runCreateForm shows an interactive TUI to choose Frappe version, apps, and starship preset.
func runCreateForm(branch *string, apps *[]string, starshipPreset *string) error {
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

	return huh.NewForm(
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
			huh.NewSelect[string]().
				Title("Starship prompt preset").
				Description("Shell prompt theme inside the bench container.").
				Options(starshipPresets...).
				Value(starshipPreset),
		),
	).Run()
}

// counter provides auto-incrementing step numbers for create output.
type counter struct{ n int }

func (c *counter) step(msg string) {
	c.n++
	fmt.Printf("  [%d] %s\n", c.n, msg)
}

func runCreate(name, frappeBranch string, apps []string, adminPassword, dbPassword, starshipPreset string) error {
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

	// Create bench directory
	s.step("Creating bench directory")
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return fmt.Errorf("create bench dir: %w", err)
	}

	// Write compose file and Dockerfile
	s.step("Writing docker-compose.yml and Dockerfile")
	data := bench.ComposeData{
		BenchDir:            benchDir,
		WebPort:             webPort,
		WebPortEnd:          webPort + 5,
		SocketIOPort:        socketIOPort,
		SocketIOPortEnd:     socketIOPort + 5,
		MariaDBRootPassword: dbPassword,
		StarshipPreset:      starshipPreset,
	}
	if err := bench.WriteCompose(benchDir, data); err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	if err := bench.WriteDockerfile(benchDir, data); err != nil {
		return fmt.Errorf("render dockerfile: %w", err)
	}

	// Build the Docker image (installs zsh, zinit, starship)
	s.step("Building Docker image (zsh + zinit + starship) — this takes a few minutes")
	if err := runner.Build(); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
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

	// Clean any pre-existing bench dir (stale container from a previous run)
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"rm -rf /home/frappe/frappe-bench"); err != nil {
		return fmt.Errorf("clean pre-existing bench dir: %w", err)
	}

	// bench init
	s.step(fmt.Sprintf("Initialising bench (frappe-branch: %s) — this takes a few minutes", frappeBranch))
	initCmd := fmt.Sprintf(
		"bench init --frappe-branch %s --skip-redis-config-generation --no-backups --verbose /home/frappe/frappe-bench",
		frappeBranch,
	)
	if err := runner.Exec("frappe", "bash", "-c", initCmd); err != nil {
		return fmt.Errorf("bench init: %w", err)
	}

	// Configure common_site_config
	s.step("Configuring site settings")
	setConfigs := []string{
		"bench set-config -g db_host mariadb",
		"bench set-config -gp db_port 3306",
		"bench set-config -g redis_cache redis://redis-cache:6379",
		"bench set-config -g redis_queue redis://redis-queue:6379",
		"bench set-config -g redis_socketio redis://redis-queue:6379",
		"bench set-config -gp socketio_port 9000",
	}
	for _, c := range setConfigs {
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /home/frappe/frappe-bench && "+c); err != nil {
			return fmt.Errorf("set-config (%s): %w", c, err)
		}
	}

	// bench new-site
	s.step(fmt.Sprintf("Creating site %q", siteName))
	newSiteCmd := fmt.Sprintf(
		"cd /home/frappe/frappe-bench && bench new-site %s --mariadb-root-password %s --admin-password %s --no-mariadb-socket",
		siteName, dbPassword, adminPassword,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", newSiteCmd); err != nil {
		return fmt.Errorf("bench new-site: %w", err)
	}

	// Enable developer mode
	s.step("Enabling developer mode")
	devModeCmd := fmt.Sprintf(
		"cd /home/frappe/frappe-bench && bench --site %s set-config developer_mode 1 && bench use %s",
		siteName, siteName,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", devModeCmd); err != nil {
		return fmt.Errorf("enable developer mode: %w", err)
	}

	// Install additional apps
	for _, app := range apps {
		ab := appBranch(app, frappeBranch)

		s.step(fmt.Sprintf("Getting app %q (branch: %s) — may take a few minutes", app, ab))
		getCmd := fmt.Sprintf(
			"bench get-app --branch %s %s",
			ab, app,
		)
		if err := runner.Exec("frappe", "bash", "-c",
			"cd /home/frappe/frappe-bench && "+getCmd); err != nil {
			return fmt.Errorf("bench get-app %s: %w", app, err)
		}

		s.step(fmt.Sprintf("Installing app %q on site %q", app, siteName))
		installCmd := fmt.Sprintf(
			"cd /home/frappe/frappe-bench && bench --site %s install-app %s --force",
			siteName, app,
		)
		if _, err := runner.ExecSilent("frappe", "bash", "-c", installCmd); err != nil {
			return fmt.Errorf("bench install-app %s: %w", app, err)
		}
	}

	// Start dev server
	s.step("Starting dev server")
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /home/frappe/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
		return fmt.Errorf("bench start: %w", err)
	}

	// Wait for HTTP
	s.step("Waiting for web server to respond...")
	url := fmt.Sprintf("http://localhost:%d", webPort)
	if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "\nwarning: %v\n", err)
	}

	// Save state
	rec := state.Bench{
		Name:           name,
		Dir:            benchDir,
		WebPort:        webPort,
		SocketIOPort:   socketIOPort,
		FrappeBranch:   frappeBranch,
		AdminPassword:  adminPassword,
		DBPassword:     dbPassword,
		SiteName:       siteName,
		Apps:           apps,
		StarshipPreset: starshipPreset,
		CreatedAt:      time.Now(),
	}
	if err := store.Add(rec); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	fmt.Printf("\nBench %q is ready.\n", name)
	fmt.Printf("  URL:      http://localhost:%d\n", webPort)
	fmt.Printf("  Site:     %s\n", siteName)
	fmt.Printf("  Admin:    administrator / %s\n", adminPassword)
	fmt.Printf("  DB root:  %s\n", dbPassword)
	if len(apps) > 0 {
		fmt.Printf("  Apps:     %v\n", apps)
	}
	return nil
}
