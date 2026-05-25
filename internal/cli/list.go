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
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List all managed benches",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList()
		},
	}
}

func runList() error {
	svc := manager.New(verbose)
	views, err := svc.ListBenchViews()
	if err != nil {
		return err
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
		if strings.ToLower(v.Status) != "running" {
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
	return nil
}

