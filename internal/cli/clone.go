package cli

import (
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newCloneCmd() *cobra.Command {
	var in manager.CloneInput
	cmd := &cobra.Command{
		Use:   "clone <source> <new-name>",
		Short: "Copy a bench, its site's data and app commits, to a new bench",
		Long: `Back the source bench up and restore the archive as a new bench: same apps at
the same commits, same database and (unless --no-files) attachments, new
name, new ports, site <new-name>.localhost for dev.

Uncommitted app changes are not carried over unless --vendor-apps names the
app (or 'all'). A production bench needs --domain for the copy.`,
		Example: `  ffm clone mybench mybench-try
  ffm clone mybench scratch --vendor-apps myapp --no-files`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Source, in.Target = args[0], args[1]
			return manager.New(verbose).Clone(in, manager.CLIProgress{})
		},
	}
	cmd.Flags().BoolVar(&in.NoFiles, "no-files", false, "Leave the site's attachments behind")
	cmd.Flags().StringSliceVar(&in.VendorApps, "vendor-apps", nil, "Carry these apps' working trees, uncommitted changes included ('all' for every app)")
	cmd.Flags().StringVar(&in.Domain, "domain", "", "Domain of the copy (required when cloning a production bench)")
	cmd.Flags().BoolVar(&in.NoSSL, "no-ssl", false, "Production copy without Let's Encrypt")
	cmd.Flags().BoolVar(&in.LAN, "lan", false, "Publish the copy's ports on all interfaces")
	cmd.Flags().BoolVar(&in.KeepArchive, "keep-archive", false, "Keep the intermediate backup archive")
	return cmd
}
