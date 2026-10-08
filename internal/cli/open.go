package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
)

type jsonURL struct {
	Schema string `json:"schema"`
	Bench  string `json:"bench,omitempty"`
	URL    string `json:"url"`
}

func newOpenCmd() *cobra.Command {
	var mail, traefik, printOnly, asJSON bool
	cmd := &cobra.Command{
		Use:   "open [name]",
		Short: "Open a bench's site (or its Mailpit, or the Traefik dashboard) in the browser",
		Long: `Open the bench's site in the default browser. Without a desktop session
(SSH, a headless server) the URL is printed instead.

--mail opens the bench's Mailpit inbox (dev benches), --traefik the shared
proxy's dashboard.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if mail && traefik {
				return usageError{fmt.Errorf("--mail and --traefik are exclusive")}
			}
			if traefik {
				url := proxy.DashboardURL()
				if asJSON {
					return writeJSON(jsonURL{Schema: "ffm.url/v1", URL: url})
				}
				return showURL(url, printOnly)
			}
			name, err := resolveBenchName(args, "Select a bench to open")
			if err != nil {
				return err
			}
			svc := manager.New(verbose)
			var url string
			if mail {
				url, err = svc.MailURL(name)
			} else {
				url, err = svc.SiteURL(name)
			}
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonURL{Schema: "ffm.url/v1", Bench: name, URL: url})
			}
			return showURL(url, printOnly)
		},
	}
	cmd.Flags().BoolVar(&mail, "mail", false, "Open the bench's Mailpit inbox instead of the site (dev only)")
	cmd.Flags().BoolVar(&traefik, "traefik", false, "Open the shared Traefik proxy's dashboard")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the URL instead of opening it")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the URL as JSON (schema ffm.url/v1)")
	return cmd
}

func newMailCmd() *cobra.Command {
	var printOnly, asJSON bool
	cmd := &cobra.Command{
		Use:   "mail [name]",
		Short: "Open the Mailpit inbox that catches a dev bench's outgoing mail",
		Long: `Every dev bench runs Mailpit, and its site sends mail there unless the site
has its own default outgoing Email Account. This opens the inbox (the bench's
web port + 6); without a desktop session the URL is printed instead.

Benches created before ffm v0.11.0 get Mailpit from 'ffm reconcile <bench>'.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			url, err := manager.New(verbose).MailURL(name)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonURL{Schema: "ffm.url/v1", Bench: name, URL: url})
			}
			return showURL(url, printOnly)
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the URL instead of opening it")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the URL as JSON (schema ffm.url/v1)")
	return cmd
}

func newLoginCmd() *cobra.Command {
	var user string
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "login [name]",
		Short: "Open a dev bench's desk already logged in (Administrator by default)",
		Long: `Start a session on a dev bench's site and open the desk with it, without
typing a password. The URL carries the session id, so it works as a
password until the session ends: it is printed only with --print, or when
there is no desktop to open it on.

Dev benches only.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to log in to")
			if err != nil {
				return err
			}
			url, err := manager.New(verbose).LoginURL(name, user)
			if err != nil {
				return err
			}
			return showURL(url, printOnly)
		},
	}
	cmd.Flags().StringVar(&user, "user", "Administrator", "User to log in as")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the login URL instead of opening it")
	return cmd
}
