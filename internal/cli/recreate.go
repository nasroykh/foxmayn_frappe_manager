package cli

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func newRecreateCmd() *cobra.Command {
	var (
		force           bool
		reallocatePorts bool
		githubToken     string
		proxyPort       int
		proxyHost       string
	)

	cmd := &cobra.Command{
		Use:   "recreate [name]",
		Short: "Delete a bench and provision it again from saved settings",
		Long: `Tears down the bench (containers, volumes, directory) then runs the same
pipeline as ffm create using parameters stored in ~/.config/ffm/benches.json.

By default the previous web and Socket.IO host ports are reused so local URLs
stay the same. Use --reallocate-ports to pick a fresh port pair like a new bench.

Production SSL: ACME email is read from the saved file (same as create) when needed.
Private app repos: pass --github-token if required for bench get-app.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to recreate")
			if err != nil {
				return err
			}
			var proxyPortPtr *int
			if cmd.Flags().Changed("proxy-port") {
				proxyPortPtr = &proxyPort
			}
			var proxyHostPtr *string
			if cmd.Flags().Changed("proxy-host") {
				proxyHostPtr = &proxyHost
			}
			return runRecreate(name, force, reallocatePorts, githubToken, proxyPortPtr, proxyHostPtr)
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation prompt")
	cmd.Flags().BoolVar(&reallocatePorts, "reallocate-ports", false, "Allocate a new web/socketio port pair instead of reusing stored ports")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token for private app repos (not stored in state)")
	cmd.Flags().IntVar(&proxyPort, "proxy-port", 0, "Override derived reverse-proxy public port for dev (socketio_port)")
	cmd.Flags().StringVar(&proxyHost, "proxy-host", "", "Override derived reverse-proxy hostname for dev (without scheme)")
	return cmd
}

func inferProdNoSSL(b state.Bench) bool {
	return b.IsProd() && strings.HasPrefix(strings.ToLower(strings.TrimSpace(b.ProxyHost)), "http://")
}

// devProxyArgsForRecreate maps stored ProxyHost to create's proxyPort/proxyHost.
// Optional overrides replace derived values when set.
func devProxyArgsForRecreate(b state.Bench, portOverride *int, hostOverride *string) (proxyPort int, proxyHost string, err error) {
	raw := strings.TrimSpace(b.ProxyHost)
	var derivedPort int
	var derivedHost string
	if raw != "" {
		u, perr := url.Parse(raw)
		if perr != nil {
			if hostOverride == nil && portOverride == nil {
				return 0, "", fmt.Errorf("parse stored proxy_host %q: %w", raw, perr)
			}
			// Stored value is unusable; rely on --proxy-host / --proxy-port.
		} else {
			derivedHost = u.Hostname()
			if derivedHost == "" && hostOverride == nil {
				return 0, "", fmt.Errorf("stored proxy_host %q has no hostname; pass --proxy-host", raw)
			}
			switch strings.ToLower(u.Scheme) {
			case "https":
				derivedPort = 443
			case "http":
				derivedPort = 80
			default:
				derivedPort = 80
			}
		}
	}
	proxyPort = derivedPort
	proxyHost = derivedHost
	if portOverride != nil {
		proxyPort = *portOverride
	}
	if hostOverride != nil {
		proxyHost = strings.TrimPrefix(strings.TrimPrefix(*hostOverride, "https://"), "http://")
	}
	return proxyPort, proxyHost, nil
}

func runRecreate(name string, force, reallocatePorts bool, githubToken string, proxyPortOverride *int, proxyHostOverride *string) error {
	store := state.Default()
	b, err := store.Get(name)
	if err != nil {
		return err
	}

	mode := b.Mode
	if mode == "" {
		mode = "dev"
	}
	apps := append([]string(nil), b.Apps...)

	proxyPortInt := 0
	proxyHostStr := ""
	if b.IsDev() {
		var derr error
		proxyPortInt, proxyHostStr, derr = devProxyArgsForRecreate(b, proxyPortOverride, proxyHostOverride)
		if derr != nil {
			return derr
		}
	}

	noSSL := inferProdNoSSL(b)
	acmeEmail := ""

	var copts *createOpts
	if !reallocatePorts && bench.ValidBenchPortPair(b.WebPort, b.SocketIOPort) {
		copts = &createOpts{fixedWebPort: b.WebPort, fixedSocketIOPort: b.SocketIOPort}
	}

	if !force {
		confirmed := false
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title(fmt.Sprintf("Recreate bench %q?", name)).
					Description("This removes all containers, volumes, and the bench directory, then provisions again from saved settings. This cannot be undone.").
					Affirmative("Yes, recreate").
					Negative("Cancel").
					Value(&confirmed),
			),
		)
		if err := form.Run(); err != nil {
			return err
		}
		if !confirmed {
			fmt.Println("Cancelled.")
			return nil
		}
	}

	fmt.Printf("Recreating bench %q...\n", name)

	teardownBenchFiles(b)

	if err := store.Remove(name); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	return runCreate(
		b.Name,
		b.FrappeBranch,
		apps,
		b.AdminPassword,
		b.DBPassword,
		b.DBEngine(),
		githubToken,
		proxyPortInt,
		proxyHostStr,
		mode,
		b.Domain,
		noSSL,
		acmeEmail,
		copts,
	)
}
