package cli

import (
	"bufio"
	"context"
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
		Long:  `Optional HTTP admin UI at /admin (HTTP Basic auth). Default: 127.0.0.1:8787`,
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
			if cfg.AdminPassword == "" {
				return fmt.Errorf("--admin-password is required to enable the /admin UI")
			}
			if err := dashboard.SaveConfig(cfg); err != nil {
				return err
			}
			if daemon {
				return startDashboardDaemon(cfg.ListenAddr, cfg.AdminPassword)
			}
			return runDashboardForeground(cfg.ListenAddr, cfg.AdminPassword)
		},
	}
	cmd.Flags().BoolVarP(&daemon, "daemon", "d", false, "Run in background")
	cmd.Flags().StringVar(&listen, "listen", "", "Listen address (default 127.0.0.1:8787)")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "", "HTTP Basic password for /admin")
	return cmd
}

func runDashboardForeground(addr, password string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s := server.New(
		server.WithAddr(addr),
		server.WithAdminPassword(password),
	)
	fmt.Printf("Dashboard: http://%s/admin\n", addr)
	return s.Run(ctx)
}

func dashboardPIDPath() string  { return config.DashboardPIDFile() }
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
	proc, err := os.FindProcess(pid)
	if err != nil {
		return pid, false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return pid, false
	}
	return pid, true
}

func startDashboardDaemon(addr, password string) error {
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
	logFile, err := os.OpenFile(dashboardLogPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	args := []string{"__dashboard-daemon", "--listen", addr, "--admin-password", password}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = logFile.Close()
	if err := os.WriteFile(dashboardPIDPath(), []byte(strconv.Itoa(cmd.Process.Pid)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("Dashboard started (PID %d). Log: %s\n", cmd.Process.Pid, dashboardLogPath())
	fmt.Printf("Open http://%s/admin\n", addr)
	return nil
}

// RunDashboardDaemon is the hidden entrypoint for background mode.
func RunDashboardDaemon(listen, adminPassword string) error {
	ctx := context.Background()
	return server.New(
		server.WithAddr(listen),
		server.WithAdminPassword(adminPassword),
	).Run(ctx)
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
			proc, err := os.FindProcess(pid)
			if err != nil {
				return err
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
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
				fmt.Printf("Dashboard running (PID %d) at http://%s/admin\n", pid, cfg.ListenAddr)
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
