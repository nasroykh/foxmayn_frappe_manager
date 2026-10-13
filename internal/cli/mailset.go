package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// envSMTPPassword stands in for --password-stdin on 'ffm mail set'.
const envSMTPPassword = "FFM_SMTP_PASSWORD"

// jsonMail is the ffm.mail/v1 document of 'ffm mail show'. The SMTP password is
// never part of it; has_password says whether one is stored.
type jsonMail struct {
	Schema    string           `json:"schema"`
	Bench     string           `json:"bench"`
	Effective string           `json:"effective"`
	Managed   bool             `json:"managed"`
	Account   *jsonMailAccount `json:"account,omitempty"`
	// SiteConfig holds the mail_* keys Frappe falls back to when no default
	// outgoing Email Account is enabled.
	SiteConfig map[string]any `json:"site_config,omitempty"`
}

type jsonMailAccount struct {
	Name        string `json:"name"`
	From        string `json:"from"`
	SMTPHost    string `json:"smtp_host"`
	SMTPPort    int    `json:"smtp_port"`
	TLS         string `json:"tls"`
	User        string `json:"user,omitempty"`
	HasPassword bool   `json:"has_password"`
}

func mailAccountDoc(a *manager.MailAccount) *jsonMailAccount {
	if a == nil {
		return nil
	}
	return &jsonMailAccount{Name: a.Name, From: a.EmailID, SMTPHost: a.SMTPServer, SMTPPort: a.SMTPPort,
		TLS: a.Security(), User: a.Login(), HasPassword: a.HasPassword}
}

// addMailSubcommands hangs set, show and unset under 'ffm mail', whose bare
// form keeps opening the Mailpit inbox.
func addMailSubcommands(mailCmd *cobra.Command) {
	mailCmd.AddCommand(newMailSetCmd(), newMailShowCmd(), newMailUnsetCmd())
}

func newMailSetCmd() *cobra.Command {
	var in manager.MailSetInput
	var stdin bool
	cmd := &cobra.Command{
		Use:   "set [bench]",
		Short: "Send the site's mail through an SMTP server (creates the default outgoing Email Account)",
		Long: `Create or update the site's default outgoing Email Account, so the site sends
mail through your SMTP server, and print the SPF, DKIM and DMARC records to
publish for the From address's domain. The bench must be running.

Frappe tests the connection when it saves the account, so a wrong host, port or
login fails here; --no-verify skips the test (an SMTP server that is not
reachable from the bench yet).

The SMTP password comes from stdin (--password-stdin) or $FFM_SMTP_PASSWORD,
never from a flag. Without a password the account does not authenticate.

On a dev bench, Mailpit's settings are removed from site_config and are not put
back on start; 'ffm mail unset' restores them. Use a transactional provider
(Amazon SES, Postmark, Mailgun, Brevo, ...) for anything real.`,
		Example: `  echo "$SMTP_PASSWORD" | ffm mail set mybench --smtp-host smtp.postmarkapp.com \
      --from erp@example.com --user "$TOKEN" --password-stdin`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Check the flags before asking for a password.
			if _, err := manager.ValidateMailSet(in); err != nil {
				return usageError{err}
			}
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			if stdin || cmd.Flags().Changed("user") || os.Getenv(envSMTPPassword) != "" {
				pw, err := secretInput(stdin, []string{envSMTPPassword}, "the SMTP password", "--password-stdin")
				if err != nil {
					if errors.Is(err, errCancelled) {
						return nil
					}
					return err
				}
				in.Password = pw
			}
			in.Bench = name
			acct, err := manager.New(verbose).MailSet(in)
			if err != nil {
				return err
			}
			fmt.Printf("Outgoing mail of %q now goes through %s:%d (%s) as %s, Email Account %q.\n",
				name, acct.SMTPServer, acct.SMTPPort, acct.Security(), acct.EmailID, acct.Name)
			if in.AccountName != "" && in.AccountName != acct.Name {
				fmt.Printf("An outgoing Email Account for %s already existed, so %q was updated instead of making %q.\n",
					acct.EmailID, acct.Name, in.AccountName)
			}
			printMailRecords(acct.EmailID, acct.SMTPServer)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&in.SMTPHost, "smtp-host", "", "SMTP server host name (required)")
	f.IntVar(&in.SMTPPort, "smtp-port", 0, "SMTP port (default: 587 for starttls, 465 for ssl, 25 for none)")
	f.StringVar(&in.TLS, "tls", "starttls", "Transport security: starttls, ssl or none")
	f.StringVar(&in.From, "from", "", "The address mail is sent from (required)")
	f.StringVar(&in.User, "user", "", "SMTP login (default with a password: the --from address); needs a password")
	f.BoolVar(&stdin, "password-stdin", false, "Read the SMTP password from stdin (one line)")
	f.StringVar(&in.AccountName, "name", "", "Name of the Email Account, which recipients see as the sender name (default: Frappe names it after the --from address)")
	f.BoolVar(&in.NoVerify, "no-verify", false, "Do not let Frappe test the SMTP connection when saving")
	return cmd
}

func newMailShowCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show [bench]",
		Short: "Show where the site's outgoing mail goes",
		Long: `Show the default outgoing Email Account and the mail_* settings of
site_config.json. Frappe sends through the account when one is enabled and marked
default, and falls back to site_config otherwise. The SMTP password is never shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			st, err := manager.New(verbose).MailShow(name)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonMail{Schema: "ffm.mail/v1", Bench: name, Effective: st.Effective, Managed: st.Managed,
					Account: mailAccountDoc(st.Account), SiteConfig: st.SiteConfig})
			}
			switch st.Effective {
			case "account":
				a := st.Account
				who := "no authentication"
				if l := a.Login(); l != "" {
					who = "login " + l
					if !a.HasPassword {
						who += " (no password stored)"
					}
				}
				managed := ""
				if st.Managed {
					managed = ", managed by ffm"
				}
				fmt.Printf("Mail of %q goes through Email Account %q%s.\n  server  %s:%d (%s)\n  from    %s\n  auth    %s\n",
					name, a.Name, managed, a.SMTPServer, a.SMTPPort, a.Security(), a.EmailID, who)
			case "site_config":
				fmt.Printf("Mail of %q goes through the mail_server in site_config.json: %v\n", name, st.SiteConfig["mail_server"])
				if st.SiteConfig["mail_server"] == "mailpit" {
					fmt.Println("  (the bench's Mailpit inbox: 'ffm mail' opens it)")
				}
			default:
				fmt.Printf("Mail of %q is not configured: nothing can be sent. Set it up with 'ffm mail set'.\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.mail/v1)")
	return cmd
}

func newMailUnsetCmd() *cobra.Command {
	var account string
	cmd := &cobra.Command{
		Use:   "unset [bench]",
		Short: "Stop sending through the Email Account 'ffm mail set' made",
		Long: `Disable the Email Account and stop it being the default outgoing one. It is
kept in Frappe, so earlier mail still links to it. A dev bench sends to Mailpit again.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			changed, err := manager.New(verbose).MailUnset(name, account)
			if err != nil {
				return err
			}
			if !changed {
				fmt.Printf("Nothing to unset on %q: ffm has no outgoing Email Account there.\n", name)
				return nil
			}
			fmt.Printf("Outgoing mail of %q no longer goes through the Email Account.\n", name)
			return nil
		},
	}
	cmd.Flags().StringVar(&account, "name", "", "Name of the Email Account (default: the one 'ffm mail set' made)")
	return cmd
}

func printMailRecords(from, smtpHost string) {
	recs := manager.MailDNSRecords(from, smtpHost)
	if len(recs) == 0 {
		return
	}
	fmt.Println("\nPublish these DNS records for the From domain so receivers accept the mail:")
	for _, r := range recs {
		fmt.Printf("  %-5s %-12s %s\n        %s\n        (%s)\n", r.Kind, r.Type, r.Name, r.Value, r.Note)
	}
	fmt.Println("\n" + manager.MailRecommendation)
}
