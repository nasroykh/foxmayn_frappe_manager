package cli

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newTestCmd() *cobra.Command {
	var in manager.TestInput
	cmd := &cobra.Command{
		Use:   "test <app> [bench]",
		Short: "Run an app's tests on a dev bench",
		Long: `Run 'bench --site <site> run-tests --app <app>' on a dev bench, streaming the
output. The exit code is non-zero when a test fails.

The tests run on the bench's own site and create and delete records there;
the first run sets allow_tests in its site_config.json. Production benches
are refused.`,
		Example: `  ffm test erpnext mybench --module erpnext.accounts.doctype.account.test_account
  ffm test myapp --doctype "Sales Invoice" --junit report.xml`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args[1:], "Select a bench to test on")
			if err != nil {
				return err
			}
			in.Name, in.App, in.Out = name, args[0], os.Stdout
			return manager.New(verbose).Test(in)
		},
	}
	cmd.Flags().StringVar(&in.Module, "module", "", "Only this module (dotted path)")
	cmd.Flags().StringVar(&in.Doctype, "doctype", "", "Only this DocType's tests")
	cmd.Flags().StringVar(&in.Test, "test", "", "Only this test case (needs --module or --doctype)")
	cmd.Flags().StringVar(&in.JUnit, "junit", "", "Write a JUnit XML report to this file")
	cmd.Flags().BoolVar(&in.Failfast, "failfast", false, "Stop at the first failure")
	return cmd
}
