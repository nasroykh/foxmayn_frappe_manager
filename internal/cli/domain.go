package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newDomainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "domain",
		Short: "Manage extra hostnames that route to a bench",
		Long: `Route additional hostnames to a bench through the shared Traefik proxy.

A bench always answers on its primary host — <name>.localhost for dev, the
public domain for prod. An alias is any other hostname you want it to answer on,
typically a LAN name such as erp.internal that your router or DNS server points
at this machine so every device on the network can reach the site.

ffm configures the routing; pointing DNS at this host is up to you. 'domain add'
prints the exact records to create.

Adding or removing an alias replaces the bench's containers so Traefik picks up
the new labels. Databases and the workspace are untouched.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDomainList(args)
		},
	}

	cmd.AddCommand(newDomainListCmd(), newDomainAddCmd(), newDomainRemoveCmd())
	return cmd
}

func newDomainListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [bench]",
		Short: "List the hostnames routed to a bench",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDomainList(args)
		},
	}
}

func runDomainList(args []string) error {
	name, err := resolveBenchName(args, "Select a bench")
	if err != nil {
		return err
	}
	primary, aliases, err := manager.New(verbose).DomainList(name)
	if err != nil {
		return err
	}
	fmt.Printf("Hostnames routed to bench %q:\n", name)
	fmt.Printf("  %s  (primary)\n", primary)
	for _, a := range aliases {
		fmt.Printf("  %s  (alias)\n", a)
	}
	if len(aliases) == 0 {
		fmt.Printf("\nNo aliases. Add one with 'ffm domain add <domain> %s'.\n", name)
	}
	return nil
}

func newDomainAddCmd() *cobra.Command {
	var tls bool

	cmd := &cobra.Command{
		Use:   "add <domain> [bench]",
		Short: "Route an extra hostname to a bench",
		Long: `Route an extra hostname to a bench.

The bench keeps answering on its primary host; the alias is served in addition.
Aliases are served over plain HTTP unless --tls is passed.

Socket.IO is handled per mode. Dev benches connect straight to the published
Socket.IO port, so that port has to be reachable from the other machines too —
the exact port is printed after the alias is added. Prod benches route
/socket.io through Traefik on the alias, so nothing extra needs opening.`,
		Example: `  ffm domain add erp.internal
  ffm domain add shop.test mybench
  ffm domain add erp.example.org prodbench --tls`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args[1:], "Select a bench to add the domain to")
			if err != nil {
				return err
			}
			return manager.New(verbose).DomainAdd(manager.DomainInput{
				Name: name, Domain: args[0], TLS: tls,
			}, manager.CLIProgress{})
		},
	}

	cmd.Flags().BoolVar(&tls, "tls", false,
		"Serve the aliases over HTTPS with Let's Encrypt instead of plain HTTP. "+
			"Production benches with SSL only, and every alias must be publicly resolvable — "+
			"Traefik puts them in one certificate request, so a name it cannot reach fails the whole order.")

	return cmd
}

func newDomainRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <domain> [bench]",
		Aliases: []string{"rm"},
		Short:   "Stop routing a hostname to a bench",
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args[1:], "Select a bench to remove the domain from")
			if err != nil {
				return err
			}
			return manager.New(verbose).DomainRemove(manager.DomainInput{
				Name: name, Domain: args[0],
			}, manager.CLIProgress{})
		},
	}
}
