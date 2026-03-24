package cli

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newCreateCmd() *cobra.Command {
	var (
		frappeBranch  string
		adminPassword string
		dbPassword    string
	)

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create and start a new Frappe bench",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCreate(args[0], frappeBranch, adminPassword, dbPassword)
		},
	}

	cmd.Flags().StringVar(&frappeBranch, "frappe-branch", "version-15", "Frappe branch to initialise")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "admin", "Frappe site admin password")
	cmd.Flags().StringVar(&dbPassword, "db-password", "123", "MariaDB root password")

	return cmd
}

func step(n int, msg string) {
	fmt.Printf("  [%d] %s\n", n, msg)
}

func runCreate(name, frappeBranch, adminPassword, dbPassword string) error {
	// 1. Validate name
	if err := bench.ValidateName(name); err != nil {
		return err
	}

	store := state.Default()

	// Check for duplicate
	if _, err := store.Get(name); err == nil {
		return fmt.Errorf("bench %q already exists", name)
	}

	fmt.Printf("Creating bench %q...\n", name)

	// 2 & 3. Allocate ports (includes host collision check)
	step(1, "Allocating ports")
	webPort, socketIOPort, err := bench.AllocatePorts(store)
	if err != nil {
		return fmt.Errorf("port allocation: %w", err)
	}

	benchDir := config.BenchDir(name)
	runner := bench.NewRunner(name, benchDir, verbose)
	siteName := name + ".localhost"

	// 4. Create bench directory
	step(2, "Creating bench directory")
	if err := os.MkdirAll(benchDir, 0o755); err != nil {
		return fmt.Errorf("create bench dir: %w", err)
	}

	// 5. Render compose file
	step(3, "Writing docker-compose.yml")
	data := bench.ComposeData{
		BenchDir:            benchDir,
		WebPort:             webPort,
		WebPortEnd:          webPort + 5,
		SocketIOPort:        socketIOPort,
		SocketIOPortEnd:     socketIOPort + 5,
		MariaDBRootPassword: dbPassword,
	}
	if err := bench.WriteCompose(benchDir, data); err != nil {
		return fmt.Errorf("render compose: %w", err)
	}

	// 6. docker compose up -d
	step(4, "Starting containers (docker compose up)")
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}

	// 7. Wait for MariaDB
	step(5, "Waiting for MariaDB to be ready...")
	if err := runner.WaitForMariaDB(dbPassword, 90*time.Second, os.Stderr); err != nil {
		return fmt.Errorf("wait for MariaDB: %w", err)
	}
	fmt.Println()

	// 8. bench init (interactive — streams to terminal)
	// Remove any pre-existing bench directory so that stale containers from a
	// previous failed or manually-deleted bench don't cause "already exists" errors.
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"rm -rf /home/frappe/frappe-bench"); err != nil {
		return fmt.Errorf("clean pre-existing bench dir: %w", err)
	}

	step(6, fmt.Sprintf("Initialising bench (frappe-branch: %s) — this takes a few minutes", frappeBranch))
	initCmd := fmt.Sprintf(
		"bench init --frappe-branch %s --skip-redis-config-generation --no-backups --verbose /home/frappe/frappe-bench",
		frappeBranch,
	)
	if err := runner.Exec("frappe", "bash", "-c", initCmd); err != nil {
		return fmt.Errorf("bench init: %w", err)
	}

	// 9. Configure common_site_config
	step(7, "Configuring site settings")
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

	// 10. bench new-site
	step(8, fmt.Sprintf("Creating site %q", siteName))
	newSiteCmd := fmt.Sprintf(
		"cd /home/frappe/frappe-bench && bench new-site %s --mariadb-root-password %s --admin-password %s --no-mariadb-socket",
		siteName, dbPassword, adminPassword,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", newSiteCmd); err != nil {
		return fmt.Errorf("bench new-site: %w", err)
	}

	// 11. Enable developer mode and set default site
	step(9, "Enabling developer mode")
	devModeCmd := fmt.Sprintf(
		"cd /home/frappe/frappe-bench && bench --site %s set-config developer_mode 1 && bench use %s",
		siteName, siteName,
	)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", devModeCmd); err != nil {
		return fmt.Errorf("enable developer mode: %w", err)
	}

	// 12. Start bench dev server in the background via nohup so it survives
	// after the exec session exits.
	step(10, "Starting dev server")
	if _, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /home/frappe/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
		return fmt.Errorf("bench start: %w", err)
	}

	// 13. Wait for HTTP
	step(11, "Waiting for web server to respond...")
	url := fmt.Sprintf("http://localhost:%d", webPort)
	if err := bench.WaitForHTTP(url, 60*time.Second); err != nil {
		fmt.Fprintf(os.Stderr, "\nwarning: %v\n", err)
	}

	// 14. Save to state
	rec := state.Bench{
		Name:          name,
		Dir:           benchDir,
		WebPort:       webPort,
		SocketIOPort:  socketIOPort,
		FrappeBranch:  frappeBranch,
		AdminPassword: adminPassword,
		SiteName:      siteName,
		CreatedAt:     time.Now(),
	}
	if err := store.Add(rec); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	// 15. Print success
	fmt.Printf("\nBench %q is ready.\n", name)
	fmt.Printf("  URL:    %s\n", url)
	fmt.Printf("  Site:   %s\n", siteName)
	fmt.Printf("  Admin:  administrator / %s\n", adminPassword)
	return nil
}
