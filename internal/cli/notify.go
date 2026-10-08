package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/notify"
)

type jsonNotifier struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type jsonNotifiers struct {
	Schema    string         `json:"schema"`
	Notifiers []jsonNotifier `json:"notifiers"`
}

func newNotifyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "notify",
		Short: "Where ffm reports failed scheduled backups, verifies and checks (add / list / remove / test)",
		Long: `Notifiers are told when a scheduled backup fails ('ffm backup run-due'), and
when 'ffm backup verify --notify' runs. By default only failures are sent;
--on always sends successes too.

A healthchecks notifier (healthchecks.io or any compatible service) gets a
start ping before every scheduled run, then success or /fail: the service
alerts when the pings stop, which catches a scheduler that never runs at all.

URLs and tokens are kept in ~/.config/ffm/notify.json (0600). A Slack webhook
URL or a Telegram bot token is read from stdin, never taken as a flag value;
other URLs may come from stdin too (--url-stdin), which keeps them out of
your shell history.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return printNotifiers(false) },
	}
	var n notify.Notifier
	var urlStdin, tokenStdin, replace bool
	add := &cobra.Command{
		Use:   "add <name>",
		Short: "Add a notifier",
		Example: `  ffm notify add phone --type ntfy --url https://ntfy.sh/my-secret-topic
  echo "$SLACK_WEBHOOK" | ffm notify add team --type slack --url-stdin
  echo "$BOT_TOKEN" | ffm notify add tg --type telegram --chat-id 123456 --token-stdin
  ffm notify add hc --type healthchecks --url https://hc-ping.com/<uuid>
  ffm notify add ops --type webhook --url https://example.com/hooks/ffm --on always`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			n.Name = args[0]
			if urlStdin && tokenStdin {
				return usageError{errors.New("--url-stdin and --token-stdin are exclusive")}
			}
			if (n.Type == notify.TypeSlack && !urlStdin) || (n.Type == notify.TypeTelegram && !tokenStdin) {
				return usageError{errors.New("a Slack webhook URL comes from --url-stdin and a Telegram bot token from --token-stdin: they are secrets")}
			}
			if urlStdin || tokenStdin {
				line, err := bufio.NewReader(os.Stdin).ReadString('\n')
				if err != nil && line == "" {
					return fmt.Errorf("read from stdin: %w", err)
				}
				if urlStdin {
					n.URL = strings.TrimSpace(line)
				} else {
					n.Token = strings.TrimSpace(line)
				}
			}
			if err := manager.AddNotifier(n, replace); err != nil {
				return err
			}
			fmt.Printf("Saved notifier %q: %s. Try it: ffm notify test %s\n", n.Name, n.Describe(), n.Name)
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&n.Type, "type", "", "webhook, ntfy, telegram, slack or healthchecks")
	f.StringVar(&n.URL, "url", "", "Endpoint URL (webhook, ntfy topic, healthchecks ping URL)")
	f.BoolVar(&urlStdin, "url-stdin", false, "Read the URL from stdin (required for slack)")
	f.BoolVar(&tokenStdin, "token-stdin", false, "Read the Telegram bot token or an ntfy access token from stdin")
	f.StringVar(&n.ChatID, "chat-id", "", "telegram: chat id")
	f.StringVar(&n.On, "on", "", "failure (default) or always; healthchecks pings every run regardless")
	f.BoolVar(&replace, "replace", false, "Replace a notifier with the same name")

	var asJSON bool
	list := &cobra.Command{
		Use:   "list",
		Short: "List notifiers (no URLs or tokens)",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, args []string) error { return printNotifiers(asJSON) },
	}
	list.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.notifiers/v1)")
	remove := &cobra.Command{
		Use:   "remove <name>",
		Short: "Forget a notifier",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := manager.RemoveNotifier(args[0]); err != nil {
				return err
			}
			fmt.Printf("Removed notifier %q.\n", args[0])
			return nil
		},
	}
	test := &cobra.Command{
		Use:   "test <name>",
		Short: "Send a test notification",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := manager.TestNotifier(args[0]); err != nil {
				return err
			}
			fmt.Printf("Sent a test notification through %q.\n", args[0])
			return nil
		},
	}
	cmd.AddCommand(add, list, remove, test)
	return cmd
}

func printNotifiers(asJSON bool) error {
	ns, err := manager.LoadNotifiers()
	if err != nil {
		return err
	}
	if asJSON {
		out := jsonNotifiers{Schema: "ffm.notifiers/v1", Notifiers: []jsonNotifier{}}
		for _, n := range ns {
			out.Notifiers = append(out.Notifiers, jsonNotifier{Name: n.Name, Type: n.Type, Description: n.Describe()})
		}
		return writeJSON(out)
	}
	if len(ns) == 0 {
		fmt.Println("No notifiers. Add one with: ffm notify add <name> --type …")
		return nil
	}
	for _, n := range ns {
		fmt.Printf("  %-16s %s\n", n.Name, n.Describe())
	}
	return nil
}
