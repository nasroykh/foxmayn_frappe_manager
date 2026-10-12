package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// The Claude Code WorktreeCreate and WorktreeRemove hooks. Claude Code hands
// the hook a JSON document on stdin; a create hook must print the worktree's
// path as the last non-empty line of stdout, so everything else, including
// the progress of the clone, goes to stderr.

// worktreeService is what the hooks need of manager.Service.
type worktreeService interface {
	WorktreeAdd(manager.WorktreeAddInput, manager.ProgressWriter) (manager.WorktreeAddResult, error)
	WorktreeRemove(manager.WorktreeRemoveInput, manager.ProgressWriter) (manager.WorktreeRemoveResult, error)
}

// writerProgress sends every progress line to w.
type writerProgress struct{ w io.Writer }

func (p writerProgress) Step(msg string)                   { fmt.Fprintf(p.w, "  → %s\n", msg) }
func (p writerProgress) Printf(format string, args ...any) { fmt.Fprintf(p.w, format, args...) }
func (p writerProgress) Println(args ...any)               { fmt.Fprintln(p.w, args...) }
func (p writerProgress) Stderr() io.Writer                 { return p.w }

// runWorktreeCreateHook makes a worktree bench for the session Claude Code is
// starting and prints the worktree's path as the only line on stdout.
func runWorktreeCreateHook(svc worktreeService, stdin io.Reader, stdout, stderr io.Writer) error {
	var ev struct {
		Cwd  string `json:"cwd"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(stdin).Decode(&ev); err != nil {
		return fmt.Errorf("worktree create hook: read the event from stdin: %w", err)
	}
	if strings.TrimSpace(ev.Name) == "" {
		return errors.New("worktree create hook: the event has no name")
	}
	res, err := svc.WorktreeAdd(manager.WorktreeAddInput{Branch: ev.Name, Cwd: ev.Cwd}, writerProgress{stderr})
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "Bench %q at %s\n", res.Bench, res.URL)
	fmt.Fprintln(stdout, res.Path)
	return nil
}

// runWorktreeRemoveHook removes the worktree and bench Claude Code made. It
// never forces: a worktree with uncommitted or untracked files is kept, with
// its bench, and the hook fails with git's reason on stderr. Only a branch
// ffm created is deleted, with git branch -d.
func runWorktreeRemoveHook(svc worktreeService, stdin io.Reader, stderr io.Writer) error {
	var ev struct {
		WorktreePath string `json:"worktree_path"`
	}
	if err := json.NewDecoder(stdin).Decode(&ev); err != nil {
		return fmt.Errorf("worktree remove hook: read the event from stdin: %w", err)
	}
	if strings.TrimSpace(ev.WorktreePath) == "" {
		return errors.New("worktree remove hook: the event has no worktree_path")
	}
	res, err := svc.WorktreeRemove(manager.WorktreeRemoveInput{
		Target: ev.WorktreePath, PathOnly: true, DeleteBranch: true,
	}, writerProgress{stderr})
	if err != nil {
		return err
	}
	if _, serr := os.Lstat(ev.WorktreePath); serr == nil {
		return fmt.Errorf("worktree remove hook: %s still exists after the removal", ev.WorktreePath)
	}
	fmt.Fprintf(stderr, "Removed worktree bench %q.\n", res.Bench)
	return nil
}

const worktreeHookSnippet = `{
  "hooks": {
    "WorktreeCreate": [
      { "hooks": [ { "type": "command", "command": "ffm worktree hook create", "timeout": 1800 } ] }
    ],
    "WorktreeRemove": [
      { "hooks": [ { "type": "command", "command": "ffm worktree hook remove", "timeout": 1800 } ] }
    ]
  }
}
`

func newWorktreeHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook",
		Short: "Claude Code WorktreeCreate and WorktreeRemove hooks",
		Long: `Make Claude Code's worktrees (claude --worktree, subagents with isolation:
"worktree") ffm worktree benches. 'ffm worktree hook print' prints the
.claude/settings.json to add; the hooks read Claude Code's JSON on stdin.

Create is synchronous: it clones a bench, which takes minutes, hence the long
timeout in the snippet. Remove never forces: a worktree with uncommitted or
untracked files is kept together with its bench, and the hook fails.`,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "create",
			Short: "WorktreeCreate hook: reads {cwd, name} on stdin, prints the worktree path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				// Anything the pipeline prints on stdout would corrupt the
				// path Claude Code reads from the last line.
				out := os.Stdout
				os.Stdout = os.Stderr
				defer func() { os.Stdout = out }()
				return runWorktreeCreateHook(manager.New(verbose), os.Stdin, out, os.Stderr)
			},
		},
		&cobra.Command{
			Use:   "remove",
			Short: "WorktreeRemove hook: reads {worktree_path} on stdin, removes the worktree and its bench",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				out := os.Stdout
				os.Stdout = os.Stderr
				defer func() { os.Stdout = out }()
				return runWorktreeRemoveHook(manager.New(verbose), os.Stdin, os.Stderr)
			},
		},
		&cobra.Command{
			Use:   "print",
			Short: "Print the .claude/settings.json snippet that installs the hooks",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				fmt.Print(worktreeHookSnippet)
				fmt.Fprintln(os.Stderr, "\nMerge this into .claude/settings.json (or ~/.claude/settings.json).\n"+
					"Claude Code does not process .worktreeinclude when a WorktreeCreate hook exists, so\n"+
					"the create hook copies the files matching the repository's .worktreeinclude itself.")
				return nil
			},
		},
	)
	return cmd
}
