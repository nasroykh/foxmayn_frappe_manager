package cli

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type jsonWorktree struct {
	Bench      string `json:"bench"`
	Source     string `json:"source"`
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	PathExists bool   `json:"path_exists"`
	URL        string `json:"url"`
}

type jsonWorktrees struct {
	Schema    string         `json:"schema"`
	Worktrees []jsonWorktree `json:"worktrees"`
}

type jsonWorktreeAdd struct {
	Schema        string `json:"schema"`
	Bench         string `json:"bench"`
	Source        string `json:"source"`
	Branch        string `json:"branch"`
	Path          string `json:"path"`
	URL           string `json:"url"`
	CreatedBranch bool   `json:"created_branch"`
}

func newWorktreeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worktree",
		Short: "Run a branch of your project in its own bench, in a git worktree",
		Long: `A worktree bench is a copy of a project bench (made by ffm up inside an app
repository) whose mounted checkout is a git worktree on its own branch: its own
database, ports and URL, running next to the original.

ffm never deletes your checkout or changes its git state on its own. Removal is
git worktree remove without --force, so git refuses a worktree with
uncommitted or untracked files; --force is passed only when you pass it. A
branch is deleted only with git branch -d, only when ffm created it, and only
with --delete-branch.`,
	}
	cmd.AddCommand(newWorktreeAddCmd(), newWorktreeRemoveCmd(), newWorktreeListCmd(), newWorktreeHookCmd())
	return cmd
}

func newWorktreeAddCmd() *cobra.Command {
	var (
		in       manager.WorktreeAddInput
		jsonFlag bool
	)
	cmd := &cobra.Command{
		Use:   "add <branch>",
		Short: "Clone a project bench and mount a new git worktree of the branch into it",
		Long: `Clone the source bench (backup and restore, like ffm clone), add a git worktree
for the branch next to your checkout, and mount it as the clone's app.

The source is --from, or the bench that mounts the main worktree of the
repository you are in. The bench is named <source>-<branch slug> and answers on
<slug>.<source>.localhost besides its own <bench>.localhost. The worktree goes
to <repo-parent>/<repo-name>.worktrees/<slug> unless --path says otherwise.

A branch that does not exist is created from --base (default: the checkout's
HEAD). Files of the repository's .worktreeinclude (gitignore syntax) that are
untracked in your checkout are copied into the worktree, never over an
existing file.

If anything fails after the worktree is added, the bench is removed, the
worktree is kept, and the commands that remove it are printed.`,
		Example: `  ffm worktree add feat/login
  ffm worktree add fix/csv --base origin/main --from myapp --no-files`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Branch = args[0]
			in.Cwd = "."
			var res manager.WorktreeAddResult
			err := toStderrIf(jsonFlag, func() (err error) {
				res, err = manager.New(verbose).WorktreeAdd(in, manager.CLIProgress{})
				return err
			})
			if err != nil {
				return err
			}
			if jsonFlag {
				return writeJSON(jsonWorktreeAdd{Schema: "ffm.worktree/v1", Bench: res.Bench, Source: res.Source,
					Branch: res.Branch, Path: res.Path, URL: res.URL, CreatedBranch: res.CreatedBranch})
			}
			fmt.Printf("\nBench %q is running branch %q.\n  worktree: %s\n  url:      %s\n", res.Bench, res.Branch, res.Path, res.URL)
			fmt.Printf("Remove it with: ffm worktree remove %s\n", res.Bench)
			return nil
		},
	}
	cmd.Flags().StringVar(&in.From, "from", "", "Source bench (default: the bench of this repository's main checkout)")
	cmd.Flags().StringVar(&in.Base, "base", "", "Commit or branch a new branch starts from")
	cmd.Flags().StringVar(&in.Path, "path", "", "Where the worktree goes (default: <repo>.worktrees/<slug> beside the checkout)")
	cmd.Flags().StringVar(&in.Name, "name", "", "Bench name (default: <source>-<branch slug>)")
	cmd.Flags().BoolVar(&in.NoFiles, "no-files", false, "Leave the site's attachments behind")
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Print the result as JSON (ffm.worktree/v1); progress goes to stderr")
	return cmd
}

func newWorktreeRemoveCmd() *cobra.Command {
	var in manager.WorktreeRemoveInput
	cmd := &cobra.Command{
		Use:   "remove <branch|path|bench>",
		Short: "Remove a worktree bench and its git worktree",
		Long: `Remove the git worktree with git worktree remove, then delete the bench. A
backup of the bench is taken first (--no-backup skips it).

git refuses a worktree with uncommitted or untracked files; then nothing is
changed. --force passes --force to git and discards them. --keep-worktree
removes only the bench. --delete-branch runs git branch -d on the branch, only
when ffm created it; git refuses an unmerged branch.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			in.Target = args[0]
			res, err := manager.New(verbose).WorktreeRemove(in, manager.CLIProgress{})
			if err != nil {
				return err
			}
			switch {
			case res.WorktreeRemoved:
				fmt.Printf("Removed the worktree %s and bench %q.\n", res.Path, res.Bench)
			default:
				fmt.Printf("Removed bench %q; the worktree %s was kept.\n", res.Bench, res.Path)
			}
			if res.BranchDeleted {
				fmt.Printf("Deleted branch %q.\n", res.Branch)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&in.Force, "force", false, "Pass --force to git worktree remove: discard uncommitted and untracked files")
	cmd.Flags().BoolVar(&in.KeepWorktree, "keep-worktree", false, "Remove the bench only; leave the worktree")
	cmd.Flags().BoolVar(&in.DeleteBranch, "delete-branch", false, "Also run git branch -d on a branch ffm created")
	cmd.Flags().BoolVar(&in.NoBackup, "no-backup", false, "Do not back the bench up before deleting it")
	return cmd
}

func newWorktreeListCmd() *cobra.Command {
	var jsonFlag bool
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List worktree benches",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			wts, err := manager.New(verbose).WorktreeList()
			if err != nil {
				return err
			}
			if jsonFlag {
				doc := jsonWorktrees{Schema: "ffm.worktrees/v1", Worktrees: []jsonWorktree{}}
				for _, w := range wts {
					doc.Worktrees = append(doc.Worktrees, jsonWorktree{Bench: w.Bench, Source: w.Source, Branch: w.Branch,
						Path: w.Path, PathExists: w.PathExists, URL: w.URL})
				}
				return writeJSON(doc)
			}
			if len(wts) == 0 {
				fmt.Println("No worktree benches. Make one with: ffm worktree add <branch>")
				return nil
			}
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "BENCH\tSOURCE\tBRANCH\tURL\tPATH")
			for _, w := range wts {
				path := w.Path
				if !w.PathExists {
					path += " (missing)"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", w.Bench, w.Source, w.Branch, w.URL, path)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonFlag, "json", false, "Print JSON (ffm.worktrees/v1)")
	return cmd
}

// toStderrIf runs fn with os.Stdout pointed at stderr when redirect is set, so
// progress lines never mix into a machine-readable document on stdout.
func toStderrIf(redirect bool, fn func() error) error {
	if !redirect {
		return fn()
	}
	real := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = real }()
	return fn()
}
