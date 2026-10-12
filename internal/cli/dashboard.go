package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/dashboard"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/server"
)

func newDashboardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Web dashboard for managing Frappe benches",
		Long: `Optional admin UI at /admin (HTTP Basic auth). Default: http://127.0.0.1:8787

A non-loopback --listen address is refused unless the dashboard serves TLS
itself (--tls-cert and --tls-key) or you pass --insecure-http. The one-shot
command page is off unless started with --allow-exec. Every dashboard POST is
appended to dashboard-audit.jsonl in the config directory.`,
	}
	cmd.AddCommand(
		newDashboardStartCmd(),
		newDashboardStopCmd(),
		newDashboardStatusCmd(),
		newDashboardLogsCmd(),
	)
	return cmd
}

func newDashboardStartCmd() *cobra.Command {
	var (
		daemon        bool
		listen        string
		adminPassword string
		tlsCert       string
		tlsKey        string
		insecureHTTP  bool
		allowExec     bool
	)

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the web dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := dashboard.LoadConfig()
			if err != nil {
				return err
			}
			if listen != "" {
				cfg.ListenAddr = listen
			}
			if adminPassword != "" {
				cfg.AdminPassword = adminPassword
			}
			if cmd.Flags().Changed("tls-cert") {
				cfg.TLSCert = tlsCert
			}
			if cmd.Flags().Changed("tls-key") {
				cfg.TLSKey = tlsKey
			}
			if cmd.Flags().Changed("insecure-http") {
				cfg.InsecureHTTP = insecureHTTP
			}
			if cmd.Flags().Changed("allow-exec") {
				cfg.AllowExec = allowExec
			}
			if cfg.AdminPassword == "" {
				return usageError{errors.New("--admin-password is required to enable the /admin UI")}
			}
			if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
				return usageError{errors.New("--tls-cert and --tls-key go together")}
			}
			// Checked before anything is saved or spawned: the daemon would
			// otherwise fail into dashboard.log after "Dashboard started".
			if err := server.CheckListen(cfg.ListenAddr, cfg.TLSCert != "", cfg.InsecureHTTP); err != nil {
				return usageError{err}
			}
			if err := dashboard.SaveConfig(cfg); err != nil {
				return err
			}
			if daemon {
				return startDashboardDaemon(cfg)
			}
			return runDashboardForeground(cfg)
		},
	}
	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run in background")
	cmd.Flags().StringVar(&listen, "listen", "", "Listen address (default 127.0.0.1:8787)")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "", "HTTP Basic password for /admin")
	cmd.Flags().StringVar(&tlsCert, "tls-cert", "", "PEM certificate file: serve HTTPS (needs --tls-key); saved in dashboard.json")
	cmd.Flags().StringVar(&tlsKey, "tls-key", "", "PEM private key file for --tls-cert")
	cmd.Flags().BoolVar(&insecureHTTP, "insecure-http", false, "Allow a non-loopback --listen address over plain HTTP (the password crosses the network in clear)")
	cmd.Flags().BoolVar(&allowExec, "allow-exec", false, "Enable the one-shot command page (remote code execution in bench containers; off by default)")
	return cmd
}

func dashboardOptions(cfg dashboard.Config, addr string) []server.Option {
	return []server.Option{
		server.WithAddr(addr),
		server.WithAdminPassword(cfg.AdminPassword),
		server.WithTLS(cfg.TLSCert, cfg.TLSKey),
		server.WithInsecureHTTP(cfg.InsecureHTTP),
		server.WithAllowExec(cfg.AllowExec),
	}
}

func dashboardScheme(cfg dashboard.Config) string {
	if cfg.TLSCert != "" {
		return "https"
	}
	return "http"
}

func runDashboardForeground(cfg dashboard.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := server.New(dashboardOptions(cfg, cfg.ListenAddr)...)
	fmt.Printf("Dashboard: %s://%s/admin\n", dashboardScheme(cfg), cfg.ListenAddr)
	return s.Run(ctx)
}

func dashboardPIDPath() string { return config.DashboardPIDFile() }
func dashboardLogPath() string { return config.DashboardLogFile() }

func isDashboardRunning() (pid int, alive bool) {
	data, err := os.ReadFile(dashboardPIDPath())
	if err != nil {
		return 0, false
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, processAlive(pid)
}

// startDashboardDaemon re-executes ffm in the background. The admin password
// is not passed on the command line, where every local user could read it in
// `ps`: the daemon reads it from dashboard.json, saved just before.
func startDashboardDaemon(cfg dashboard.Config) error {
	addr := cfg.ListenAddr
	if pid, alive := isDashboardRunning(); alive {
		return fmt.Errorf("dashboard already running (PID %d)", pid)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.ConfigDir(), 0o755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(dashboardLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	_ = os.Chmod(dashboardLogPath(), 0o600)
	args := []string{"__dashboard-daemon", "--listen", addr}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{}
	configureSysProcAttr(cmd.SysProcAttr)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = logFile.Close()
	if err := os.WriteFile(dashboardPIDPath(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Printf("Dashboard started (PID %d). Log: %s\n", cmd.Process.Pid, dashboardLogPath())
	fmt.Printf("Open %s://%s/admin\n", dashboardScheme(cfg), addr)
	return nil
}

// RunDashboardDaemon is the hidden entrypoint for background mode.
func RunDashboardDaemon(cfg dashboard.Config, listen string) error {
	ctx := context.Background()
	return server.New(dashboardOptions(cfg, listen)...).Run(ctx)
}

func newDashboardStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the background dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, alive := isDashboardRunning()
			if !alive {
				fmt.Println("Dashboard is not running.")
				_ = os.Remove(dashboardPIDPath())
				return nil
			}
			if err := stopProcess(pid); err != nil {
				return err
			}
			_ = os.Remove(dashboardPIDPath())
			fmt.Printf("Dashboard stopped (PID %d).\n", pid)
			return nil
		},
	}
}

func newDashboardStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show dashboard daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, alive := isDashboardRunning()
			cfg, _ := dashboard.LoadConfig()
			if alive {
				fmt.Printf("Dashboard running (PID %d) at %s://%s/admin\n", pid, dashboardScheme(cfg), cfg.ListenAddr)
			} else {
				fmt.Println("Dashboard stopped.")
			}
			return nil
		},
	}
}

func newDashboardLogsCmd() *cobra.Command {
	var follow bool
	var lines int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Show dashboard daemon log",
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.Open(dashboardLogPath())
			if err != nil {
				return err
			}
			defer f.Close()
			if follow {
				return tailFollow(f, os.Stdout)
			}
			sc := bufio.NewScanner(f)
			var all []string
			for sc.Scan() {
				all = append(all, sc.Text())
			}
			start := 0
			if len(all) > lines {
				start = len(all) - lines
			}
			for _, l := range all[start:] {
				fmt.Println(l)
			}
			return sc.Err()
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().IntVar(&lines, "lines", 50, "Number of lines")
	return cmd
}

func tailFollow(f *os.File, w *os.File) error {
	if _, err := f.Seek(0, 0); err != nil {
		return err
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fmt.Fprintln(w, sc.Text())
	}
	for {
		time.Sleep(500 * time.Millisecond)
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fmt.Fprintln(w, sc.Text())
		}
		stat, _ := f.Stat()
		_, _ = f.Seek(stat.Size(), 0)
	}
}
