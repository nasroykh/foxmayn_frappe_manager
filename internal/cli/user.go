package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// envUserPassword stands in for --password-stdin on 'ffm user add|password'.
const envUserPassword = "FFM_USER_PASSWORD"

// jsonUsers is the ffm.users/v1 document of 'ffm user list'.
type jsonUsers struct {
	Schema string             `json:"schema"`
	Bench  string             `json:"bench"`
	Users  []manager.UserInfo `json:"users"`
}

func newUserCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage a site's users: list, add, disable, enable, password",
		Long: `Manage the users of a bench's Frappe site. The bench must be running.

  ffm user list [bench]
  ffm user add <email> [bench]          (password: --password-stdin, $FFM_USER_PASSWORD or a prompt)
  ffm user disable <user> [bench]
  ffm user enable <user> [bench]
  ffm user password <user> [bench]      (same sources; signs the user out everywhere)

A password is never taken as a flag value or an argument, and never reaches the
command line of a process on the host or in the container. Frappe's own
password policy applies to every user except Administrator. Changing the
Administrator password also updates ffm's record of it ('ffm status').`,
	}
	cmd.AddCommand(newUserListCmd(), newUserAddCmd(), newUserSetEnabledCmd(false), newUserSetEnabledCmd(true), newUserPasswordCmd())
	return cmd
}

func newUserListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list [bench]",
		Short: "List the site's users (Guest excluded)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args, "Select a bench")
			if err != nil {
				return err
			}
			users, err := manager.New(verbose).UserList(name)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(jsonUsers{Schema: "ffm.users/v1", Bench: name, Users: users})
			}
			w := 4
			for _, u := range users {
				if len(u.Name) > w {
					w = len(u.Name)
				}
			}
			fmt.Printf("%-*s  %-8s  %-12s  %s\n", w, "USER", "STATE", "TYPE", "ROLES")
			for _, u := range users {
				state := "enabled"
				if !u.Enabled {
					state = "disabled"
				}
				roles := "-"
				if u.SystemManager {
					roles = "System Manager"
					if n := len(u.Roles) - 1; n > 0 {
						roles += fmt.Sprintf(" +%d", n)
					}
				} else if len(u.Roles) > 0 {
					roles = fmt.Sprintf("%d role(s)", len(u.Roles))
				}
				fmt.Printf("%-*s  %-8s  %-12s  %s\n", w, u.Name, state, u.UserType, roles)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print as JSON (schema ffm.users/v1)")
	return cmd
}

func newUserAddCmd() *cobra.Command {
	var in manager.UserAddInput
	var stdin bool
	cmd := &cobra.Command{
		Use:   "add <email> [bench]",
		Short: "Create an enabled user with a password",
		Long: `Create a System User. The password comes from stdin (--password-stdin), from
$FFM_USER_PASSWORD, or from a hidden prompt when you are at a terminal; never
from a flag. No welcome email is sent. --system-manager adds the System Manager
role; other roles are assigned in the desk.`,
		Example: `  echo "$PW" | ffm user add ana@example.com mybench --first-name Ana --system-manager --password-stdin`,
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Email = args[0]
			if err := manager.ValidateUserEmail(in.Email); err != nil {
				return usageError{err}
			}
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			pw, err := secretInput(stdin, []string{envUserPassword}, "the new user's password", "--password-stdin")
			if err != nil {
				if errors.Is(err, errCancelled) {
					return nil
				}
				return err
			}
			in.Bench, in.Password = name, pw
			if err := manager.New(verbose).UserAdd(in); err != nil {
				return err
			}
			fmt.Printf("Added %s to %q.\n", in.Email, name)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.FirstName, "first-name", "", "First name (default: the part of the address before the @)")
	cmd.Flags().StringVar(&in.LastName, "last-name", "", "Last name")
	cmd.Flags().BoolVar(&in.SystemManager, "system-manager", false, "Give the user the System Manager role")
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "Read the password from stdin (one line)")
	return cmd
}

func newUserSetEnabledCmd(enable bool) *cobra.Command {
	verb, past := "disable", "Disabled"
	short := "Disable a user (Administrator cannot be disabled)"
	if enable {
		verb, past, short = "enable", "Enabled", "Enable a user"
	}
	return &cobra.Command{
		Use:   verb + " <user> [bench]",
		Short: short,
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			if err := manager.New(verbose).UserSetEnabled(name, args[0], enable); err != nil {
				return err
			}
			fmt.Printf("%s %s on %q.\n", past, args[0], name)
			return nil
		},
	}
}

func newUserPasswordCmd() *cobra.Command {
	var stdin bool
	cmd := &cobra.Command{
		Use:   "password <user> [bench]",
		Short: "Change a user's password and sign them out everywhere",
		Long: `Change a user's password. It comes from stdin (--password-stdin), from
$FFM_USER_PASSWORD, or from a hidden prompt; never from a flag.

For Administrator, ffm's record of the password is updated too, so
'ffm status --show-secrets' stays right.`,
		Example: `  echo "$PW" | ffm user password Administrator mybench --password-stdin`,
		Args:    cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(args[0]) == "" {
				return usageError{errors.New("the user cannot be empty")}
			}
			name, err := resolveBenchName(args[1:], "Select a bench")
			if err != nil {
				return err
			}
			pw, err := secretInput(stdin, []string{envUserPassword}, "the new password", "--password-stdin")
			if err != nil {
				if errors.Is(err, errCancelled) {
					return nil
				}
				return err
			}
			if err := manager.New(verbose).UserSetPassword(name, args[0], pw); err != nil {
				return err
			}
			fmt.Printf("Changed the password of %s on %q.\n", args[0], name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&stdin, "password-stdin", false, "Read the password from stdin (one line)")
	return cmd
}
