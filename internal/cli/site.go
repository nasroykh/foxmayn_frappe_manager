package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type jsonSite struct {
	Schema string `json:"schema"`
	Bench  string `json:"bench"`
	manager.SiteState
}

func newSiteCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "site [bench]",
		Short: "Site operations: migrate, maintenance mode, Frappe's scheduler (bare: show state)",
		Long: `Operate on a bench's Frappe site:

  ffm site migrate [bench]                    bench migrate (DocTypes, patches, fixtures)
  ffm site maintenance on|off [bench]         visitors get a maintenance page
  ffm site scheduler on|off|pause|resume [bench]   Frappe's scheduled jobs

Bare 'ffm site' shows maintenance mode and the scheduler's state. Frappe's
scheduler is not 'ffm backup scheduler', which runs ffm's own backups.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			st, err := manager.New(verbose).SiteStatus(name)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonSite{Schema: "ffm.site/v1", Bench: name, SiteState: st})
			}
			sched := st.Scheduler
			if sched == "" {
				sched = "unknown (bench not running)"
			}
			fmt.Printf("Site %s\n  maintenance: %v\n  scheduler:   %s\n", st.Site, onOff(st.Maintenance), sched)
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.site/v1)")

	migrate := &cobra.Command{
		Use:   "migrate [bench]",
		Short: "Run bench migrate on the site (output streamed)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench to migrate")
			if err != nil {
				return err
			}
			return manager.New(verbose).SiteMigrate(name, os.Stdout)
		},
	}
	maintenance := &cobra.Command{
		Use:       "maintenance on|off [bench]",
		Short:     "Turn maintenance mode on or off",
		Args:      cobra.RangeArgs(1, 2),
		ValidArgs: []string{"on", "off"},
		RunE: func(cmd *cobra.Command, args []string) error {
			if args[0] != "on" && args[0] != "off" {
				return usageError{fmt.Errorf("maintenance on or off, not %q", args[0])}
			}
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			if err := manager.New(verbose).SiteMaintenance(name, args[0] == "on"); err != nil {
				return err
			}
			fmt.Printf("Maintenance mode %s for %q.\n", args[0], name)
			return nil
		},
	}
	scheduler := &cobra.Command{
		Use:   "scheduler on|off|pause|resume [bench]",
		Short: "Enable, disable, pause or resume Frappe's scheduler",
		Long: `on/off enable or disable Frappe's scheduler (System Settings). pause/resume
stop scheduled jobs without changing that setting, for a maintenance window.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "on", "off", "pause", "resume":
			default:
				return usageError{fmt.Errorf("scheduler on, off, pause or resume, not %q", args[0])}
			}
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			if err := manager.New(verbose).SiteScheduler(name, args[0]); err != nil {
				return err
			}
			fmt.Printf("Scheduler %s for %q.\n", args[0], name)
			return nil
		},
	}
	cmd.AddCommand(migrate, maintenance, scheduler)
	return cmd
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
