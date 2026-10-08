package cli

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func newDBCmd() *cobra.Command {
	var importPath, exportPath string
	var yes, force, migrate bool
	cmd := &cobra.Command{
		Use:   "db [name]",
		Short: "Open the site's database console, or import/export its database",
		Long: `Without flags, open the site's database console ('bench --site <site>
db-console': mariadb or psql, logged in as the site's own database user).

--export FILE dumps the site's database to FILE: gzip as Frappe writes it
when FILE ends in .gz, plain SQL otherwise. The dump holds every password
hash and API secret of the site.

--import FILE replaces the site's database with a .sql or .sql.gz dump
through 'bench restore', which drops and recreates the database first.
Files and site_config.json are kept: a dump from another site keeps this
site's encryption key, so its Password fields will not decrypt. Pass
--migrate for a dump from other app versions.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if importPath != "" && exportPath != "" {
				return usageError{errors.New("--import and --export are exclusive")}
			}
			if importPath != "" && yes && len(args) == 0 {
				// With --yes nothing confirms the target, so it must be named:
				// the picker auto-selects a lone bench, and the working
				// directory can select one implicitly.
				return usageError{errors.New("--yes requires the bench name: ffm db <name> --import FILE --yes")}
			}
			if importPath == "" && exportPath == "" && !isInteractive() {
				return usageError{errors.New("the database console needs an interactive terminal; pass --export or --import")}
			}
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			svc := manager.New(verbose)
			switch {
			case exportPath != "":
				return svc.DBExport(manager.DBExportInput{Name: name, Path: exportPath, Force: force}, manager.CLIProgress{})
			case importPath != "":
				if !yes {
					if !isInteractive() {
						return mustNotPrompt("import confirmation", "pass --yes")
					}
					ok := false
					if err := huh.NewForm(huh.NewGroup(huh.NewConfirm().
						Title(fmt.Sprintf("Replace the database of %q with %s?", name, importPath)).
						Description("The current database is dropped. Take a backup first if you need it (ffm backup).").
						Affirmative("Yes, replace").Negative("Cancel").Value(&ok))).
						WithKeyMap(benchPickKeyMap()).Run(); err != nil {
						if cancelled(err) {
							return nil
						}
						return err
					}
					if !ok {
						fmt.Println("Cancelled.")
						return nil
					}
				}
				return svc.DBImport(manager.DBImportInput{Name: name, Path: importPath, Migrate: migrate}, manager.CLIProgress{})
			}
			argv, err := svc.SiteCommand(name, "db-console")
			if err != nil {
				return err
			}
			return svc.Interactive(name, argv...)
		},
	}
	cmd.Flags().StringVar(&exportPath, "export", "", "Dump the site's database to this file (.sql or .sql.gz)")
	cmd.Flags().StringVar(&importPath, "import", "", "Replace the site's database with this .sql or .sql.gz dump")
	cmd.Flags().BoolVar(&yes, "yes", false, "Do not ask before --import replaces the database")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite the --export file if it exists")
	cmd.Flags().BoolVar(&migrate, "migrate", false, "Run bench migrate after --import")
	return cmd
}
