package cli

import (
	"fmt"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
)

func newListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all managed benches",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(asJSON)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print machine-readable JSON (schema ffm.list/v1)")
	return cmd
}

// benchJSON converts a bench view to its JSON form.
func benchJSON(v manager.BenchView) jsonBench {
	db := "mariadb"
	if v.DBEngine == "pg" {
		db = "postgres"
	}
	return jsonBench{
		Name: v.Name, Mode: v.Mode, DB: db, Status: v.Status, Site: v.SiteName, URL: v.URL,
		WebPort: v.WebPort, SocketIOPort: v.SocketIOPort, Domain: v.Domain, ProxyHost: v.ProxyHost,
		FrappeBranch: v.FrappeBranch, Tunnel: v.TunnelOn, TemplatesOutdated: v.TemplatesOutdated,
	}
}

// listDoc builds the ffm.list/v1 document (shared with ffm mcp).
func listDoc(views []manager.BenchView) jsonList {
	out := jsonList{Schema: "ffm.list/v1", Benches: []jsonBench{}}
	for _, v := range views {
		out.Benches = append(out.Benches, benchJSON(v))
	}
	return out
}

func runList(asJSON bool) error {
	svc := manager.New(verbose)
	views, err := svc.ListBenchViews()
	if err != nil {
		return err
	}
	if asJSON {
		return writeJSON(listDoc(views))
	}

	if len(views) == 0 {
		fmt.Println("No benches found. Run `ffm create <name>` to create one.")
		return nil
	}

	proxyUp := proxy.IsRunning()

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	nameStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	runningStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	stoppedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	partialStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	domainStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	mutedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Faint(true)

	header := fmt.Sprintf("%-20s  %-5s  %-7s  %-10s  %-8s  %-30s  %s",
		headerStyle.Render("NAME"),
		headerStyle.Render("MODE"),
		headerStyle.Render("DB"),
		headerStyle.Render("STATUS"),
		headerStyle.Render("PORT"),
		headerStyle.Render("DOMAIN"),
		headerStyle.Render("BRANCH"),
	)
	fmt.Println(header)
	fmt.Println(strings.Repeat("─", 104))

	devBenchExists := false
	for _, v := range views {
		if v.Mode == "dev" {
			devBenchExists = true
		}

		statusRendered := runningStyle.Render(v.Status)
		switch strings.ToLower(v.Status) {
		case "running":
		case "partial":
			statusRendered = partialStyle.Render(v.Status)
		default:
			statusRendered = stoppedStyle.Render(v.Status)
		}

		var domainRendered string
		if v.Mode == "prod" {
			d := v.ProxyHost
			if d == "" && v.Domain != "" {
				d = "https://" + v.Domain
			}
			domainRendered = domainStyle.Render(d)
		} else {
			domain := fmt.Sprintf("http://%s", v.SiteName)
			if proxyUp {
				domainRendered = domainStyle.Render(domain)
			} else {
				domainRendered = mutedStyle.Render(domain + " (proxy off)")
			}
		}

		fmt.Printf("%-20s  %-5s  %-7s  %-10s  %-8d  %-30s  %s\n",
			nameStyle.Render(v.Name),
			dimStyle.Render(v.Mode),
			dimStyle.Render(v.DBEngine),
			statusRendered,
			v.WebPort,
			domainRendered,
			dimStyle.Render(v.FrappeBranch),
		)
	}

	if !proxyUp && devBenchExists {
		fmt.Printf("\n  %s\n", mutedStyle.Render("Run 'ffm proxy start' to enable sitename.localhost routing."))
	}
	var outdated, partial []string
	for _, v := range views {
		if v.TemplatesOutdated {
			outdated = append(outdated, v.Name)
		}
		if v.Status == "partial" {
			partial = append(partial, v.Name)
		}
	}
	if len(partial) > 0 {
		fmt.Printf("\n  %s\n", partialStyle.Render(fmt.Sprintf(
			"Partly running (the frappe container is down): %s. Run 'ffm restart <bench>'; 'ffm logs <bench> frappe' shows why it stopped.",
			strings.Join(partial, ", "))))
	}
	if len(outdated) > 0 {
		fmt.Printf("\n  %s\n", stoppedStyle.Render(fmt.Sprintf(
			"Built from older templates: %s. Run 'ffm reconcile <bench> --dry-run' to see the changes.",
			strings.Join(outdated, ", "))))
	}
	return nil
}
