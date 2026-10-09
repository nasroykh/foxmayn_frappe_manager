package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/mcpinstall"
)

// Seams for tests: the client environment (home and config dirs, the claude
// CLI) and the path of the running binary.
var (
	mcpInstallEnv        = mcpinstall.DefaultEnv
	mcpInstallExecutable = installedExecutable
)

const mcpClientsHelp = `  claude-code     runs: claude mcp add-json --scope user <name> '<json>'
                  (Claude Code owns ~/.claude.json; ffm never edits it)
  claude-desktop  <config dir>/Claude/claude_desktop_config.json
  cursor          ~/.cursor/mcp.json
  vscode          <config dir>/Code/User/mcp.json (default profile)
  codex           ~/.codex/config.toml ($CODEX_HOME/config.toml when set)

<config dir> is %APPDATA% on Windows, ~/Library/Application Support on macOS
and $XDG_CONFIG_HOME or ~/.config on Linux.`

func newMCPInstallCmd() *cobra.Command {
	var (
		client, name          string
		allowWrite, allowProd bool
		printOnly, yes        bool
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Add ffm mcp to Claude Code, Claude Desktop, Cursor, VS Code or Codex",
		Long: `Add (or replace) the stdio MCP server entry that runs ffm in an AI client's
user-level config, so every project sees it.

The entry runs this ffm binary by its absolute path with "mcp", plus
"--allow-write" and "--allow-prod" when given.

` + mcpClientsHelp + `

The change is shown as a diff (the command, for claude-code) and confirmed
before anything is written. The old file is kept as
<file>.ffm-<YYYYMMDD-HHMMSS>.bak. Comments and formatting are kept; a file
that does not parse is refused and left untouched.`,
		Example: `  ffm mcp install --client claude-code
  ffm mcp install --client cursor --allow-write
  ffm mcp install --client codex --print`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkMCPClient(client, name); err != nil {
				return err
			}
			if allowProd && !allowWrite {
				return usageError{errors.New("--allow-prod needs --allow-write")}
			}
			exe, err := mcpInstallExecutable()
			if err != nil {
				return err
			}
			srvArgs := []string{"mcp"}
			if allowWrite {
				srvArgs = append(srvArgs, "--allow-write")
			}
			if allowProd {
				srvArgs = append(srvArgs, "--allow-prod")
			}
			env, err := mcpInstallEnv()
			if err != nil {
				return err
			}
			ch, err := mcpinstall.Plan(client, mcpinstall.Server{Name: name, Command: exe, Args: srvArgs}, env)
			if err != nil {
				if errors.Is(err, mcpinstall.ErrInvalid) {
					return usageError{err}
				}
				return fmt.Errorf("mcp install: %w", err)
			}
			if !ch.Changed() {
				fmt.Fprintf(os.Stderr, "The %q entry in %s is already up to date.\n", ch.Name, ch.Path)
				return nil
			}
			warnFFMEnv()
			return applyMCPChange(ch, printOnly, yes, printInstallPlan, "Added %q to Claude Code's user config", "Wrote the %q entry to %s")
		},
	}
	cmd.Flags().StringVar(&client, "client", "", "AI client: "+strings.Join(mcpinstall.Clients, ", ")+" (required)")
	cmd.Flags().StringVar(&name, "name", mcpinstall.DefaultName, "Name of the server entry")
	cmd.Flags().BoolVar(&allowWrite, "allow-write", false, "Install the server with --allow-write (create, change, start, stop, delete)")
	cmd.Flags().BoolVar(&allowProd, "allow-prod", false, "Install the server with --allow-prod (write tools may touch production benches)")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the diff (or the claude command) and change nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Write without asking")
	cmd.MarkFlagsMutuallyExclusive("print", "yes")
	return cmd
}

func newMCPUninstallCmd() *cobra.Command {
	var (
		client, name   string
		printOnly, yes bool
	)
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the ffm MCP server entry from an AI client's config",
		Long: `Remove the MCP server entry named --name (default ffm) from an AI client's
user-level config: the reverse of 'ffm mcp install'.

` + mcpClientsHelp + `

Only the entry goes: other servers, comments and formatting are kept, and the
old file is kept as <file>.ffm-<YYYYMMDD-HHMMSS>.bak. When there is no such
entry, nothing changes.`,
		Example: `  ffm mcp uninstall --client claude-desktop
  ffm mcp uninstall --client codex --print`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := checkMCPClient(client, name); err != nil {
				return err
			}
			env, err := mcpInstallEnv()
			if err != nil {
				return err
			}
			ch, err := mcpinstall.PlanRemove(client, name, env)
			if err != nil {
				if errors.Is(err, mcpinstall.ErrInvalid) {
					return usageError{err}
				}
				return fmt.Errorf("mcp uninstall: %w", err)
			}
			if !ch.Changed() {
				switch {
				case ch.Client == mcpinstall.ClaudeCode:
					fmt.Fprintf(os.Stderr, "Claude Code's user config (%s) has no %q entry; nothing to remove.\n", ch.Path, ch.Name)
				case ch.Old == nil:
					fmt.Fprintf(os.Stderr, "%s does not exist; nothing to remove.\n", ch.Path)
				default:
					fmt.Fprintf(os.Stderr, "%s has no %q entry; nothing to remove.\n", ch.Path, ch.Name)
				}
				return nil
			}
			return applyMCPChange(ch, printOnly, yes, printUninstallPlan, "Removed %q from Claude Code's user config", "Removed the %q entry from %s")
		},
	}
	cmd.Flags().StringVar(&client, "client", "", "AI client: "+strings.Join(mcpinstall.Clients, ", ")+" (required)")
	cmd.Flags().StringVar(&name, "name", mcpinstall.DefaultName, "Name of the server entry")
	cmd.Flags().BoolVar(&printOnly, "print", false, "Print the diff (or the claude command) and change nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Write without asking")
	cmd.MarkFlagsMutuallyExclusive("print", "yes")
	return cmd
}

func checkMCPClient(client, name string) error {
	if client == "" {
		return usageError{fmt.Errorf("--client is required: one of %s", strings.Join(mcpinstall.Clients, ", "))}
	}
	if !slices.Contains(mcpinstall.Clients, client) {
		return usageError{fmt.Errorf("--client %q: want one of %s", client, strings.Join(mcpinstall.Clients, ", "))}
	}
	if err := mcpinstall.ValidName(name); err != nil {
		return usageError{fmt.Errorf("--name: %w", err)}
	}
	return nil
}

// applyMCPChange shows the plan, asks (unless --yes), and applies it. With
// --print it only shows the diff or the command, on stdout.
func applyMCPChange(ch *mcpinstall.Change, printOnly, yes bool, show func(io.Writer, *mcpinstall.Change), doneClaude, doneFile string) error {
	var w io.Writer = os.Stderr
	if printOnly {
		w = os.Stdout
	}
	show(w, ch)
	if printOnly {
		return nil
	}
	// Refuse what Apply would refuse before asking: claude missing or a
	// batch shim, a read-only file.
	if err := ch.Check(); err != nil {
		if errors.Is(err, mcpinstall.ErrClaudeNotFound) || errors.Is(err, mcpinstall.ErrClaudeBatch) {
			return usageError{err}
		}
		return err
	}
	if !yes {
		if !isInteractive() {
			return mustNotPrompt("confirmation", "pass --yes, or --print to see the change")
		}
		prompt := fmt.Sprintf("Write %s?", ch.Path)
		if ch.Client == mcpinstall.ClaudeCode {
			prompt = "Run the claude command above?"
		}
		ok, err := confirm(prompt)
		if err != nil || !ok {
			return err
		}
	}
	backup, err := ch.Apply()
	if err != nil {
		return err
	}
	switch {
	case ch.Absent:
		fmt.Printf("Claude Code's user config has no %q entry; nothing was removed.\n", ch.Name)
		return nil
	case ch.Client == mcpinstall.ClaudeCode:
		fmt.Printf(doneClaude+"\n", ch.Name)
	default:
		fmt.Printf(doneFile+"\n", ch.Name, ch.Path)
	}
	if backup != "" {
		fmt.Fprintf(os.Stderr, "Backup: %s\n", backup)
	}
	fmt.Fprintln(os.Stderr, ch.Hint)
	return nil
}

func printInstallPlan(w io.Writer, ch *mcpinstall.Change) {
	if ch.Client == mcpinstall.ClaudeCode {
		fmt.Fprintf(os.Stderr, "Claude Code keeps user-scope MCP servers in %s and rewrites that file itself,\n"+
			"so ffm runs the claude CLI instead (no diff, no backup):\n", ch.Path)
		if ch.Replaces {
			fmt.Fprintf(os.Stderr, "(replaces the existing %q entry)\n", ch.Name)
		}
		for _, l := range ch.CommandLines() {
			fmt.Fprintln(w, sanitizeTerminal(l))
		}
		return
	}
	switch {
	case ch.Old == nil:
		fmt.Fprintf(os.Stderr, "%s does not exist yet; it will be created.\n", ch.Path)
	case ch.Replaces:
		fmt.Fprintf(os.Stderr, "Replacing the %q entry in %s:\n", ch.Name, ch.Path)
	default:
		fmt.Fprintf(os.Stderr, "Adding the %q entry to %s:\n", ch.Name, ch.Path)
	}
	fmt.Fprint(w, sanitizeTerminal(ch.Diff()))
}

func printUninstallPlan(w io.Writer, ch *mcpinstall.Change) {
	if ch.Client == mcpinstall.ClaudeCode {
		fmt.Fprintf(os.Stderr, "Claude Code keeps user-scope MCP servers in %s and rewrites that file itself,\n"+
			"so ffm runs the claude CLI instead (no diff, no backup):\n", ch.Path)
		if !ch.Replaces {
			fmt.Fprintf(os.Stderr, "(%s could not be read, so the %q entry may not exist; then nothing changes)\n", ch.Path, ch.Name)
		}
		for _, l := range ch.CommandLines() {
			fmt.Fprintln(w, sanitizeTerminal(l))
		}
		return
	}
	fmt.Fprintf(os.Stderr, "Removing the %q entry from %s:\n", ch.Name, ch.Path)
	fmt.Fprint(w, sanitizeTerminal(ch.Diff()))
}

// warnFFMEnv says that FFM_* settings of this shell do not reach the server
// the client starts: it runs with the client's environment.
func warnFFMEnv() {
	var set []string
	for _, k := range []string{"FFM_CONFIG_DIR", "FFM_BENCHES_DIR", "FFM_BACKUPS_DIR"} {
		if os.Getenv(k) != "" {
			set = append(set, k)
		}
	}
	if len(set) > 0 {
		fmt.Fprintf(os.Stderr, "Note: %s is set here but not written into the entry; the server runs with the client's environment.\n",
			strings.Join(set, ", "))
	}
}

// sanitizeTerminal drops control and bidirectional-override characters from
// text read out of a config file before it reaches the terminal.
func sanitizeTerminal(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r < 0x20 || (r >= 0x7f && r <= 0x9f):
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		case r == 0x200b, r == 0x2060, r == 0xfeff:
			return -1
		}
		return r
	}, s)
}

// installedExecutable is the absolute path of the running ffm, symlinks
// resolved. A temporary 'go run' build is refused: the entry would point at
// a file Go deletes.
func installedExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("finding the ffm binary: %w", err)
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	if isGoRunBuild(exe, os.TempDir(), os.Getenv("GOTMPDIR")) {
		return "", usageError{fmt.Errorf("%s is a temporary 'go run' build; install ffm first and run 'ffm mcp install' with the installed binary", exe)}
	}
	return exe, nil
}

// isGoRunBuild reports whether exe lies in a go-build directory under one of
// the temp dirs.
func isGoRunBuild(exe string, tmpDirs ...string) bool {
	if !strings.Contains(exe, "go-build") {
		return false
	}
	for _, dir := range tmpDirs {
		if dir == "" {
			continue
		}
		dirs := []string{dir}
		if real, err := filepath.EvalSymlinks(dir); err == nil && real != dir {
			dirs = append(dirs, real)
		}
		for _, d := range dirs {
			rel, err := filepath.Rel(d, exe)
			if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && strings.Contains(rel, "go-build") {
				return true
			}
		}
	}
	return false
}
