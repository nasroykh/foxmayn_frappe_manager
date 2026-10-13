package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newStatusCmd() *cobra.Command {
	var asJSON, showSecrets bool
	cmd := &cobra.Command{
		Use:   "status [name]",
		Short: "Show per-container status for a bench",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to inspect")
			if err != nil {
				return err
			}
			if asJSON {
				return runStatusJSON(name, showSecrets)
			}
			return runStatus(name, showSecrets)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON (schema ffm.status/v1)")
	cmd.Flags().BoolVar(&showSecrets, "show-secrets", false,
		"Include the admin and database passwords in --json output, and print them in text output even when stdout is not a terminal (they are masked then)")
	return cmd
}

func runStatusJSON(name string, showSecrets bool) error {
	out, err := statusDoc(manager.New(verbose), name, showSecrets)
	if err != nil {
		return err
	}
	return writeJSON(out)
}

// statusDoc builds the ffm.status/v1 document (shared with ffm mcp).
func statusDoc(svc *manager.Service, name string, showSecrets bool) (jsonStatus, error) {
	b, err := svc.GetBench(name)
	if err != nil {
		return jsonStatus{}, err
	}
	views, err := svc.ListBenchViews()
	if err != nil {
		return jsonStatus{}, err
	}
	var view manager.BenchView
	for _, v := range views {
		if v.Name == name {
			view = v
		}
	}
	bind := state.BindLAN
	if b.PublishHost() != "" {
		bind = state.BindLoopback
	}
	out := jsonStatus{
		Schema:        "ffm.status/v1",
		Bench:         benchJSON(view),
		Dir:           b.Dir,
		FrappeRepo:    b.FrappeRepo,
		Python:        b.Python,
		Node:          b.Node,
		FfcVersion:    svc.FfcVersion(name),
		Apps:          append([]string{}, b.Apps...),
		Bind:          bind,
		SSHAgent:      b.SSHAgent,
		Agent:         b.Agent,
		AgentReadOnly: b.AgentReadOnly,
		DomainAliases: append([]string{}, b.DomainAliases...),
		CreatedAt:     jsonTime(b.CreatedAt),
		Containers:    []jsonContainer{},
	}
	if showSecrets {
		out.AdminPassword = b.AdminPassword
		out.DBPassword = b.DBPassword
	}
	raw, err := bench.NewRunner(b.Name, b.Dir, false).PS("json")
	if err != nil {
		return out, fmt.Errorf("docker compose ps: %w", err)
	}
	out.Containers = parseComposePS(raw)
	return out, nil
}

// ffcStatusValue is the ffc version, with a note when the image pins another
// release than the one the container runs.
func ffcStatusValue(v string) string {
	if pinned := strings.TrimPrefix(bench.FfcRef, "v"); v != pinned {
		return fmt.Sprintf("%s (image pins %s: ffm restart --rebuild)", v, bench.FfcRef)
	}
	return v
}

// parseComposePS reads `docker compose ps --format json`, which prints one
// JSON object per line (Compose 2.21+) or, on older versions, a JSON array.
func parseComposePS(raw string) []jsonContainer {
	type psRow struct {
		Service, Name, State, Status, Health string
	}
	var rows []psRow
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "[") {
		_ = json.Unmarshal([]byte(raw), &rows)
	} else {
		for _, line := range strings.Split(raw, "\n") {
			var r psRow
			if json.Unmarshal([]byte(line), &r) == nil && r.Name != "" {
				rows = append(rows, r)
			}
		}
	}
	out := []jsonContainer{}
	for _, r := range rows {
		out = append(out, jsonContainer{Service: r.Service, Name: r.Name, State: r.State, Status: r.Status, Health: r.Health})
	}
	return out
}

func runStatus(name string, showSecrets bool) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	valStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("7"))

	label := func(k, v string) {
		fmt.Printf("  %s  %s\n", labelStyle.Render(fmt.Sprintf("%-12s", k)), valStyle.Render(v))
	}

	fmt.Println(titleStyle.Render(b.Name))
	label("mode", b.Mode)
	label("site", b.SiteName)
	if b.IsProd() {
		if b.ProxyHost != "" {
			label("url", b.ProxyHost)
		} else if b.Domain != "" {
			label("url", fmt.Sprintf("https://%s", b.Domain))
		}
	} else {
		label("url (port)", fmt.Sprintf("http://localhost:%d", b.WebPort))
		if b.ProxyHost != "" {
			label("url (proxy)", b.ProxyHost)
		} else if serving, broken := proxy.Probe(); serving {
			label("url (domain)", fmt.Sprintf("http://%s", b.SiteName))
		} else {
			label("url (domain)", fmt.Sprintf("http://%s  (run 'ffm proxy start')", b.SiteName))
			if broken != "" {
				fmt.Fprintf(os.Stderr, "warning: the Traefik proxy is running but broken (%s); run 'ffm proxy start' to recreate it.\n", broken)
			}
		}
	}
	label("branch", b.FrappeBranch)
	if b.FrappeRepo != "" {
		label("frappe repo", b.FrappeRepo)
	}
	if b.Agent {
		ro := ""
		if b.AgentReadOnly {
			ro = ", MCP read-only"
		}
		label("agent", "ffc and MCP act as agent@"+b.SiteName+ro)
	}
	if b.Python != "" {
		label("toolchain", fmt.Sprintf("Python %s, Node %s", b.Python, b.Node))
	} else {
		label("toolchain", "image defaults (created before ffm chose one per branch)")
	}
	if v := manager.New(verbose).FfcVersion(name); v != "" {
		label("ffc", ffcStatusValue(v))
	}
	hint := "ffm status " + name + " --show-secrets"
	label("admin", fmt.Sprintf("administrator / %s", manager.Secret(b.AdminPassword, hint, showSecrets)))
	if b.DBPassword != "" {
		label("database", fmt.Sprintf("%s (root / %s)", b.DBEngine(), manager.Secret(b.DBPassword, hint, showSecrets)))
	}
	if len(b.Apps) > 0 {
		label("apps", strings.Join(b.Apps, ", "))
	}
	label("web port", fmt.Sprintf("%d", b.WebPort))
	label("socketio", fmt.Sprintf("%d", b.SocketIOPort))
	if !b.CreatedAt.IsZero() && time.Since(b.CreatedAt) > 7*24*time.Hour {
		hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
		fmt.Printf("  %s\n", hintStyle.Render("Tip: run 'ffm clean-logs "+name+"' to purge old log table rows (Error Log, Version, Access Log, …)"))
	}
	fmt.Println()

	runner := bench.NewRunner(b.Name, b.Dir, false)
	out, err := runner.PS("")
	if err != nil {
		return fmt.Errorf("docker compose ps: %w", err)
	}

	if out == "" {
		fmt.Println("  No containers found.")
		return nil
	}

	for _, line := range strings.Split(out, "\n") {
		fmt.Println("  " + line)
	}
	return nil
}
