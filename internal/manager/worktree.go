package manager

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// A worktree bench is a clone of a project bench (ffm up in an app repository)
// whose mounted checkout is a git worktree of the source's checkout, on its
// own branch: a second copy of the app, with its own database, running next to
// the first.
//
// The worktree belongs to the user. What ffm may do to it, and nothing else:
//
//   - create it, with `git worktree add`, only after the bench clone worked;
//   - remove it with `git worktree remove` WITHOUT --force, so git refuses a
//     worktree with uncommitted or untracked files; --force is passed only
//     when the user passed it;
//   - delete its branch with `git branch -d` (never -D, so git refuses an
//     unmerged branch), only when ffm created the branch and the user asked.
//
// No code path deletes the checkout directory itself, resets, checks out or
// cleans anything in it. Every failure after the git step keeps the worktree
// and prints the commands that clean it up.

// worktreeClone is Clone; tests replace it to avoid driving a backup and a
// restore through the fake docker.
var worktreeClone = func(s *Service, in CloneInput, pw ProgressWriter) error { return s.Clone(in, pw) }

// ErrWorktreeUnknown is returned when no worktree bench matches a target.
var ErrWorktreeUnknown = errors.New("no ffm worktree bench matches")

// WorktreeAddInput creates a worktree bench.
type WorktreeAddInput struct {
	Branch string
	// From is the source bench. Empty means the bench whose checkout is the
	// main worktree of the repository containing Cwd.
	From string
	Cwd  string
	// Base is the commit or branch a new branch starts from (git's default,
	// HEAD of the source checkout, when empty).
	Base string
	// Path is where the worktree goes. Empty means
	// <repo-parent>/<repo-name>.worktrees/<slug>.
	Path string
	// Name overrides the bench name (<from>-<slug>).
	Name    string
	NoFiles bool
}

// WorktreeAddResult says what WorktreeAdd made.
type WorktreeAddResult struct {
	Bench, Source, Branch, Path, URL string
	// CreatedBranch is true when ffm created the branch.
	CreatedBranch bool
}

// WorktreeAdd clones the source bench, adds a git worktree for the branch and
// mounts it into the clone.
func (s *Service) WorktreeAdd(in WorktreeAddInput, pw ProgressWriter) (res WorktreeAddResult, err error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	branch := strings.TrimSpace(in.Branch)
	if err := checkBranchName(branch); err != nil {
		return res, err
	}
	if strings.HasPrefix(in.Base, "-") {
		return res, fmt.Errorf("invalid --base %q", in.Base)
	}

	var src state.Bench
	if in.From != "" {
		src, err = s.GetBench(in.From)
	} else {
		src, err = s.WorktreeSource(in.Cwd)
	}
	if err != nil {
		return res, err
	}
	app, repo, ok := projectMountOf(src)
	switch {
	case !ok:
		return res, fmt.Errorf("bench %q has no mounted project checkout: a worktree bench needs a bench made by ffm up inside an app repository", src.Name)
	case src.WorktreeOf != "":
		return res, fmt.Errorf("bench %q is itself a worktree of %q: use --from %s", src.Name, src.WorktreeOf, src.WorktreeOf)
	}
	if err := checkProjectMount(app, repo); err != nil {
		return res, err
	}
	top, _, err := runGit(repo, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		return res, fmt.Errorf("%s is not a git checkout: %w", repo, err)
	}
	if canon, cerr := canonicalDir(strings.TrimSpace(top)); cerr != nil || canon != repo {
		return res, fmt.Errorf("the checkout %s is not the root of its git repository (%s): a worktree bench needs the app at the repository root",
			repo, strings.TrimSpace(top))
	}

	name := in.Name
	if name == "" {
		if name, err = bench.WorktreeBenchName(src.Name, branch); err != nil {
			return res, err
		}
	} else if err := bench.ValidateNewName(name); err != nil {
		return res, err
	}
	alias, err := bench.WorktreeAlias(src.Name, branch)
	if err != nil {
		return res, err
	}
	if exists, err := s.BenchExists(name); err != nil {
		return res, err
	} else if exists {
		return res, fmt.Errorf("bench %q already exists: pass --name for another, or remove it with ffm worktree remove", name)
	}
	if err := s.checkAliasesFree([]string{alias}, name); err != nil {
		return res, err
	}

	path, canonPath, err := worktreePath(in.Path, repo, branch)
	if err != nil {
		return res, err
	}
	if err := bench.ValidateProjectMount(app, canonPath); err != nil {
		return res, err
	}
	if err := checkMountOutsideFFM(canonPath); err != nil {
		return res, err
	}
	if pathWithin(canonPath, repo) || pathWithin(repo, canonPath) {
		return res, fmt.Errorf("the worktree path %s overlaps the source checkout %s: put the worktree outside it (the default is %s)",
			path, repo, defaultWorktreePath(repo, branch))
	}

	localExists := gitOK(repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if localExists && in.Base != "" {
		return res, fmt.Errorf("branch %q already exists, so --base %q has no effect: drop --base or choose another branch", branch, in.Base)
	}
	if listed, _, lerr := runGit(repo, "worktree", "list", "--porcelain"); lerr == nil {
		for _, l := range strings.Split(listed, "\n") {
			if strings.TrimSpace(l) == "branch refs/heads/"+branch {
				return res, fmt.Errorf("branch %q is already checked out in another worktree of %s", branch, repo)
			}
		}
	}
	remoteBranch := !localExists && in.Base == "" && remoteHasBranch(repo, branch)

	pw.Printf("Worktree bench %q for branch %q (from %q)...\n", name, branch, src.Name)
	if err := worktreeClone(s, CloneInput{Source: src.Name, Target: name, NoFiles: in.NoFiles}, pw); err != nil {
		return res, fmt.Errorf("clone %q: %w", src.Name, err)
	}

	release, err := s.lockBench(name)
	if err != nil {
		return res, err
	}
	defer release()

	// From here the bench is ours to delete on failure; the worktree is not.
	args := []string{"worktree", "add"}
	switch {
	case localExists, remoteBranch:
		// An existing local branch is used as it is; a remote-only one is
		// left to git's own guess (a new local branch tracking it).
		args = append(args, path, branch)
	default:
		args = append(args, "-b", branch, path)
		if in.Base != "" {
			args = append(args, in.Base)
		}
	}
	pw.Step("Adding the git worktree at " + path)
	if _, serr, gerr := runGit(repo, args...); gerr != nil {
		_ = s.deleteLocked(name, pw)
		return res, fmt.Errorf("git %s failed, so the clone %q was removed and your repository is untouched: %w\n%s",
			strings.Join(args, " "), name, gerr, strings.TrimSpace(serr))
	}
	created := !localExists
	canonPath, cerr := canonicalDir(path)
	if cerr != nil {
		canonPath = canonPath0(path)
	}
	if n := copyWorktreeInclude(repo, canonPath, pw); n > 0 {
		pw.Printf("  Copied %d file(s) listed in .worktreeinclude\n", n)
	}

	link := worktreeLink{Of: src.Name, Branch: branch, CreatedBranch: created, Alias: alias}
	if aerr := s.attachProjectMount(name, app, canonPath, link, pw); aerr != nil {
		_ = s.deleteLocked(name, pw)
		return res, fmt.Errorf("mount the worktree into bench %q: %w\n\nThe bench was removed. The worktree is still there; clean it up with:\n  %s",
			name, aerr, worktreeCleanupHint(repo, path, branch, created))
	}

	res = WorktreeAddResult{Bench: name, Source: src.Name, Branch: branch, Path: path, URL: "http://" + alias, CreatedBranch: created}
	return res, nil
}

// canonPath0 is the cleaned absolute path, for a path that cannot be resolved.
func canonPath0(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// worktreeCleanupHint is the git commands that undo a worktree add, without
// force. The branch line appears only for a branch ffm created.
func worktreeCleanupHint(repo, path, branch string, created bool) string {
	h := "git -C " + bench.ShellQuote(repo) + " worktree remove " + bench.ShellQuote(path)
	if created {
		h += "\n  git -C " + bench.ShellQuote(repo) + " branch -d " + bench.ShellQuote(branch)
	}
	return h
}

// checkBranchName refuses what git would not accept as a branch, and a name
// git would read as an option.
func checkBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("the branch name is empty")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	if _, serr, err := runGit(".", "check-ref-format", "--branch", branch); err != nil {
		return fmt.Errorf("invalid branch name %q: %s", branch, strings.TrimSpace(serr))
	}
	return nil
}

// defaultWorktreePath is <repo-parent>/<repo-name>.worktrees/<slug>: beside the
// checkout, not inside it, so the worktree is never inside the source bench's
// mount.
func defaultWorktreePath(repo, branch string) string {
	slug := bench.WorktreeSlug(branch)
	if len(slug) > 63 {
		slug = strings.TrimRight(slug[:56], "-") + "-" + bench.BranchHash(branch)
	}
	return filepath.Join(filepath.Dir(repo), filepath.Base(repo)+".worktrees", slug)
}

// worktreePath returns where the worktree goes as the user would write it
// (cleaned, absolute) and with the symlinks of its existing part resolved. It
// must not exist yet.
func worktreePath(given, repo, branch string) (path, canon string, err error) {
	path = given
	if path == "" {
		path = defaultWorktreePath(repo, branch)
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", "", err
	}
	if _, err := os.Lstat(path); err == nil {
		return "", "", fmt.Errorf("%s already exists: ffm adds a new worktree there and never reuses a directory — choose another with --path", path)
	}
	return path, canonicalLoose(path), nil
}

// canonicalLoose resolves the symlinks of the existing part of p and appends
// the rest, so a path that does not exist (yet, or any more) still compares.
func canonicalLoose(p string) string {
	p = canonPath0(p)
	rest := ""
	for cur := p; ; {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// remoteHasBranch reports whether some remote has a tracking ref for branch.
func remoteHasBranch(repo, branch string) bool {
	out, _, err := runGit(repo, "for-each-ref", "--format=%(refname)", "refs/remotes")
	if err != nil {
		return false
	}
	for _, ref := range strings.Fields(out) {
		rest := strings.TrimPrefix(ref, "refs/remotes/")
		if i := strings.IndexByte(rest, '/'); i > 0 && rest[i+1:] == branch {
			return true
		}
	}
	return false
}

// copyWorktreeInclude copies the untracked files matching the repository's
// .worktreeinclude into the new worktree, never over an existing file, and
// returns how many it copied. Failures are warnings: the files are a
// convenience (.env and the like).
func copyWorktreeInclude(repo, wt string, pw ProgressWriter) int {
	if _, err := os.Stat(filepath.Join(repo, ".worktreeinclude")); err != nil {
		return 0
	}
	out, _, err := runGit(repo, "ls-files", "-z", "--others", "--ignored", "--exclude-from=.worktreeinclude")
	if err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: could not read .worktreeinclude matches: %v\n", err)
		return 0
	}
	n := 0
	for _, rel := range strings.Split(out, "\x00") {
		if rel == "" {
			continue
		}
		clean := filepath.Clean(rel)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			continue
		}
		src, dst := filepath.Join(repo, clean), filepath.Join(wt, clean)
		if fi, err := os.Lstat(src); err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := copyFileNew(src, dst); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: copy %s: %v\n", clean, err)
			continue
		}
		n++
	}
	return n
}

// copyFileNew copies src to a dst that does not exist, keeping src's mode.
func copyFileNew(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// WorktreeSource returns the bench whose mounted checkout is the main worktree
// of the git repository containing dir ("" is the working directory).
func (s *Service) WorktreeSource(dir string) (state.Bench, error) {
	if dir == "" {
		dir = "."
	}
	out, _, err := runGit(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return state.Bench{}, fmt.Errorf("%s is not inside a git repository: pass --from <bench>", dir)
	}
	main, err := canonicalDir(filepath.Dir(strings.TrimSpace(out)))
	if err != nil {
		return state.Bench{}, err
	}
	benches, err := s.LoadBenches()
	if err != nil {
		return state.Bench{}, err
	}
	for _, b := range benches {
		if _, p, ok := projectMountOf(b); ok && p == main && b.WorktreeOf == "" {
			return b, nil
		}
	}
	return state.Bench{}, fmt.Errorf("no bench mounts %s: run ffm up in that repository first, or pass --from <bench>", main)
}

// WorktreeInfo describes one worktree bench.
type WorktreeInfo struct {
	Bench, Source, Branch, Path, URL string
	PathExists                       bool
}

// WorktreeList returns the benches made by WorktreeAdd.
func (s *Service) WorktreeList() ([]WorktreeInfo, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return nil, err
	}
	var out []WorktreeInfo
	for _, b := range benches {
		if b.WorktreeOf == "" {
			continue
		}
		info := WorktreeInfo{Bench: b.Name, Source: b.WorktreeOf, Branch: b.WorktreeBranch, Path: b.ProjectAppPath, URL: "http://" + primaryHostOf(b)}
		if a, err := bench.WorktreeAlias(b.WorktreeOf, b.WorktreeBranch); err == nil && slices.Contains(b.DomainAliases, a) {
			info.URL = "http://" + a
		}
		_, serr := os.Stat(b.ProjectAppPath)
		info.PathExists = serr == nil
		out = append(out, info)
	}
	return out, nil
}

// WorktreeRemoveInput removes a worktree bench.
type WorktreeRemoveInput struct {
	// Target is a bench name, a worktree path or a branch. PathOnly accepts a
	// path alone (the Claude Code hook, which is told the path).
	Target   string
	PathOnly bool
	// Force passes --force to `git worktree remove`: the user accepts losing
	// uncommitted and untracked files in the worktree.
	Force bool
	// KeepWorktree leaves the worktree and removes only the bench.
	KeepWorktree bool
	// DeleteBranch runs `git branch -d` on a branch ffm created.
	DeleteBranch bool
	// NoBackup skips the backup taken before the bench is deleted.
	NoBackup bool
}

// WorktreeRemoveResult says what WorktreeRemove did.
type WorktreeRemoveResult struct {
	Bench, Path, Branch string
	WorktreeRemoved     bool
	BranchDeleted       bool
}

// resolveWorktreeBench finds the worktree bench a target names.
func (s *Service) resolveWorktreeBench(target string, pathOnly bool) (state.Bench, error) {
	benches, err := s.LoadBenches()
	if err != nil {
		return state.Bench{}, err
	}
	var wts []state.Bench
	for _, b := range benches {
		if b.WorktreeOf != "" {
			wts = append(wts, b)
		}
	}
	want := canonicalLoose(target)
	for _, b := range wts {
		if canonicalLoose(b.ProjectAppPath) == want {
			return b, nil
		}
	}
	if !pathOnly {
		for _, b := range wts {
			if b.Name == target {
				return b, nil
			}
		}
		var byBranch []state.Bench
		for _, b := range wts {
			if b.WorktreeBranch == target {
				byBranch = append(byBranch, b)
			}
		}
		switch len(byBranch) {
		case 1:
			return byBranch[0], nil
		case 0:
		default:
			var names []string
			for _, b := range byBranch {
				names = append(names, b.Name)
			}
			return state.Bench{}, fmt.Errorf("branch %q has several worktree benches (%s): name the bench", target, strings.Join(names, ", "))
		}
	}
	return state.Bench{}, fmt.Errorf("%w %q (see ffm worktree list)", ErrWorktreeUnknown, target)
}

// WorktreeRemove removes the git worktree (without --force unless asked), then
// the bench. If git refuses, nothing is changed.
func (s *Service) WorktreeRemove(in WorktreeRemoveInput, pw ProgressWriter) (res WorktreeRemoveResult, err error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.resolveWorktreeBench(in.Target, in.PathOnly)
	if err != nil {
		return res, err
	}
	res = WorktreeRemoveResult{Bench: b.Name, Path: b.ProjectAppPath, Branch: b.WorktreeBranch}
	release, err := s.lockBench(b.Name)
	if err != nil {
		return res, err
	}
	defer release()

	_, existsErr := os.Lstat(b.ProjectAppPath)
	exists := existsErr == nil
	gitWork := !in.KeepWorktree && (exists || (in.DeleteBranch && b.WorktreeCreatedBranch))
	var repo string
	if gitWork {
		src, serr := s.GetBench(b.WorktreeOf)
		if serr != nil {
			return res, fmt.Errorf("the source bench %q of %q is gone, and ffm finds the repository through it: remove the worktree with git "+
				"(git worktree remove %s), then run ffm worktree remove %s --keep-worktree",
				b.WorktreeOf, b.Name, bench.ShellQuote(b.ProjectAppPath), b.Name)
		}
		_, repo, _ = projectMountOf(src)
	}

	removeWT := !in.KeepWorktree && exists
	if removeWT && !in.Force {
		// Read-only look first, so a dirty worktree is refused before the
		// backup below. git worktree remove stays the authority.
		if st, _, serr := runGit(b.ProjectAppPath, "status", "--porcelain"); serr == nil && strings.TrimSpace(st) != "" {
			return res, dirtyWorktreeError(b)
		}
	}
	if _, err := s.backupBeforeDestroy(b, "worktree remove", in.NoBackup, pw); err != nil {
		return res, err
	}
	if removeWT {
		args := []string{"worktree", "remove"}
		if in.Force {
			args = append(args, "--force")
		}
		args = append(args, b.ProjectAppPath)
		pw.Step("Removing the git worktree " + b.ProjectAppPath)
		if _, serr, gerr := runGit(repo, args...); gerr != nil {
			return res, fmt.Errorf("git refused to remove the worktree %s, so nothing was changed (bench %q is kept): %s\n"+
				"Commit or stash what it holds, or pass --force to discard it",
				b.ProjectAppPath, b.Name, strings.TrimSpace(serr))
		}
		res.WorktreeRemoved = true
	} else if !exists && !in.KeepWorktree {
		pw.Printf("The worktree %s is already gone.\n", b.ProjectAppPath)
		res.WorktreeRemoved = true
	}

	if err := s.deleteLocked(b.Name, pw); err != nil {
		return res, err
	}

	if in.DeleteBranch {
		switch {
		case !b.WorktreeCreatedBranch:
			pw.Printf("Keeping branch %q: ffm did not create it.\n", b.WorktreeBranch)
		case in.KeepWorktree:
			pw.Printf("Keeping branch %q: its worktree was kept.\n", b.WorktreeBranch)
		default:
			if _, serr, gerr := runGit(repo, "branch", "-d", b.WorktreeBranch); gerr != nil {
				fmt.Fprintf(pw.Stderr(), "warning: branch %q was not deleted (git branch -d refuses unmerged branches): %s\n",
					b.WorktreeBranch, strings.TrimSpace(serr))
			} else {
				res.BranchDeleted = true
			}
		}
	}
	return res, nil
}

func dirtyWorktreeError(b state.Bench) error {
	return fmt.Errorf("the worktree %s has uncommitted or untracked files, so nothing was changed (bench %q is kept): "+
		"commit or stash them, or pass --force to discard them", b.ProjectAppPath, b.Name)
}

// runGit runs git in dir (git -C dir …) and returns its stdout and stderr.
func runGit(dir string, args ...string) (stdout, stderr string, err error) {
	cmd := execx.Command("git", append([]string{"-C", dir}, args...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err = cmd.Run()
	return o.String(), e.String(), err
}

// gitOK reports whether a git command exits 0.
func gitOK(dir string, args ...string) bool {
	_, _, err := runGit(dir, args...)
	return err == nil
}
