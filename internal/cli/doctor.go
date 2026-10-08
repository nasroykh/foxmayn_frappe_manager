package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/notify"
)

type jsonDoctor struct {
	Schema  string                `json:"schema"`
	OK      bool                  `json:"ok"`
	Benches []manager.BenchHealth `json:"benches"`
}

// errDoctorFailed makes the exit code non-zero after the report printed.
var errDoctorFailed = errors.New("doctor found failures")

func newDoctorCmd() *cobra.Command {
	var asJSON, notifyFailures bool
	cmd := &cobra.Command{
		Use:   "doctor [bench]",
		Short: "Check that benches are healthy, not just running",
		Long: `Check each bench (or the one named) beyond "its containers are up":
- containers: frappe running, restart counts;
- site: /api/method/ping on the web port, and through Traefik for dev;
- database: the site connects;
- scheduler: enabled (a disabled one fails prod and warns on dev);
- workers: every RQ queue has a worker listening — jobs on a queue nobody
  consumes wait forever while the site looks fine;
- certificate (prod with Let's Encrypt): valid for the domain, days left;
- backups: the schedule produced an archive recently enough;
- disk: free space for the bench and the backups;
- templates: built from this ffm's templates.

The exit code is 1 when any check fails. --notify sends the failures through
'ffm notify' notifiers, for a cron job. Nothing is changed.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			svc := manager.New(verbose)
			var names []string
			if len(args) == 1 {
				names = args
			} else {
				benches, err := svc.LoadBenches()
				if err != nil {
					return err
				}
				for _, b := range benches {
					names = append(names, b.Name)
				}
			}
			if len(names) == 0 {
				return fmt.Errorf("no benches to check")
			}
			out := jsonDoctor{Schema: "ffm.doctor/v1", OK: true, Benches: []manager.BenchHealth{}}
			for _, n := range names {
				h, err := svc.Doctor(n)
				if err != nil {
					return err
				}
				if h.Worst() == manager.CheckFail {
					out.OK = false
					if notifyFailures {
						e := notify.Event{Kind: "doctor", Bench: n, OK: false, Message: strings.Join(h.Failures(), "\n")}
						if nerr := manager.Notify(e); nerr != nil {
							fmt.Fprintf(os.Stderr, "warning: %v\n", nerr)
						}
					}
				}
				out.Benches = append(out.Benches, h)
			}
			if asJSON {
				if err := writeJSON(out); err != nil {
					return err
				}
			} else {
				printDoctor(out.Benches)
			}
			if !out.OK {
				return errDoctorFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the report as JSON (schema ffm.doctor/v1)")
	cmd.Flags().BoolVar(&notifyFailures, "notify", false, "Send failures through 'ffm notify' notifiers")
	return cmd
}

func printDoctor(hs []manager.BenchHealth) {
	styles := map[string]lipgloss.Style{
		manager.CheckOK:   lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		manager.CheckWarn: lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		manager.CheckFail: lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		manager.CheckSkip: lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
	for i, h := range hs {
		if i > 0 {
			fmt.Println()
		}
		fmt.Printf("%s  %s\n", lipgloss.NewStyle().Bold(true).Render(h.Bench), styles[h.Worst()].Render(strings.ToUpper(h.Worst())))
		for _, c := range h.Checks {
			fmt.Printf("  %s  %-12s %s\n", styles[c.Status].Render(fmt.Sprintf("%-4s", c.Status)), c.Name, c.Detail)
		}
	}
}
