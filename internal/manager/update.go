package manager

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// UpdateInput updates a bench's apps (and with ToBranch, its Frappe branch).
type UpdateInput struct {
	Name string
	// Apps limits the pull to these apps (default: every app).
	Apps []string
	// ToBranch moves frappe and every app on the bench's current Frappe
	// branch to this branch (a major upgrade), with the toolchain it needs.
	ToBranch string
	DryRun   bool
	// NoRollback leaves a failed update as it failed, in maintenance mode,
	// for inspection.
	NoRollback bool
	// Out receives bench's output.
	Out io.Writer
}

// appState is an app's position before an update.
type appState struct {
	Name, Branch, Commit string
}

// updatePlan is what an update will do.
type updatePlan struct {
	Apps      []appState // every app, in apps.txt order
	Pull      []string   // the apps pulled
	Local     []string   // apps without an upstream remote, not pulled
	Switch    []string   // the apps moved to ToBranch
	Toolchain bench.Toolchain
	Rebuild   bool // the image must change (Node major)
}

// errDirtyApps refuses an update over uncommitted changes, which bench's
// pull would rebase or refuse, and a rollback would lose.
var errDirtyApps = errors.New("uncommitted changes")

// errLocalCommits refuses to pull apps whose HEAD is not their upstream
// branch: the pull resets to upstream.
var errLocalCommits = errors.New("commits not on upstream")

// Update runs the update pipeline under the bench lock:
//
//  1. preflight: running, clean git trees, a valid target branch;
//  2. a pre-update backup (portable) and a snapshot (fast rollback);
//  3. maintenance on, scheduler paused;
//  4. ffm's frappe patches reverted, then pull (or switch branch, rebuild the
//     image and the virtualenv for the new toolchain), requirements, migrate,
//     build, patches re-applied;
//  5. processes restarted, /api/method/ping, maintenance off, scheduler on.
//
// A failure after step 3 rolls back: every app back to its recorded commit
// (and branch, and toolchain), requirements, the snapshot restored in place,
// assets rebuilt. --no-rollback leaves it in maintenance for inspection.
func (s *Service) Update(in UpdateInput, pw ProgressWriter) (updateErr error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	out := in.Out
	if out == nil {
		out = io.Discard
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}
	if in.ToBranch != "" && !gitRefRe.MatchString(in.ToBranch) {
		return fmt.Errorf("invalid branch %q", in.ToBranch)
	}
	pw.Step("Checking the apps")
	plan, err := s.planUpdate(b, in)
	if err != nil {
		return err
	}
	printPlan(pw, b, in, plan)
	if in.DryRun {
		return nil
	}
	if free, ok := freeBytes(b.Dir); ok && free < 3<<30 {
		return fmt.Errorf("only %s free for the bench: an update needs room for new code, a virtualenv and assets", humanBytes(int64(free)))
	}

	stamp := s.clock().UTC().Format("20060102-150405")
	pw.Step("Backing the site up before the update")
	if err := s.backupLocked(BackupInput{BenchName: b.Name, NoFiles: true, Label: "before update " + stamp}, pw); err != nil {
		return fmt.Errorf("pre-update backup: %w", err)
	}
	snap, err := s.createSnapshotLocked(SnapshotInput{Bench: b.Name}, "before-update-"+stamp, pw)
	if err != nil {
		return fmt.Errorf("pre-update snapshot: %w", err)
	}

	pw.Step("Maintenance mode on, scheduler paused")
	if err := s.setMaintenance(b, true); err != nil {
		return err
	}
	if err := s.setScheduler(b, "pause"); err != nil {
		return err
	}
	finish := func() {
		pw.Step("Maintenance mode off, scheduler resumed")
		if err := s.setMaintenance(b, false); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: %v\n", err)
		}
		if err := s.setScheduler(b, "resume"); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: %v\n", err)
		}
	}

	err = s.applyUpdate(b, in, plan, out, pw)
	if err == nil {
		finish()
		// The real check: with maintenance off the site must answer ping.
		pw.Step("Checking that the site answers")
		if err = waitForSite(b, false, s.runnerFor(b)); err != nil {
			err = fmt.Errorf("the site does not answer after the update: %w", err)
			if merr := s.setMaintenance(b, true); merr != nil {
				fmt.Fprintf(pw.Stderr(), "warning: %v\n", merr)
			}
		}
	}
	if err == nil {
		if _, rerr := s.runnerFor(b).ExecSilent("frappe", "bash", "-c", "rm -rf /workspace/frappe-bench/"+prevEnv); rerr != nil {
			fmt.Fprintf(pw.Stderr(), "warning: could not delete the previous virtualenv %s: %v\n", prevEnv, rerr)
		}
		pw.Printf("Updated %q. The data before it: ffm snapshot restore %s --name %s; the code before it: %s\n",
			b.Name, b.Name, snap, describeCommits(plan.Apps))
		return nil
	}
	if in.NoRollback {
		return fmt.Errorf("%w\nThe bench is left as the update failed, in maintenance mode. Roll back by hand: the snapshot is %q, "+
			"the previous commits are %s (and after a --to-branch, the previous virtualenv is %s)", err, snap, describeCommits(plan.Apps), prevEnv)
	}
	pw.Printf("\nThe update failed: %v\nRolling back...\n", err)
	if rerr := s.rollbackUpdate(b, plan, snap, out, pw); rerr != nil {
		return fmt.Errorf("%w\nTHE ROLLBACK FAILED TOO: %v\nThe bench is in maintenance mode. Snapshot %q and the backup taken before the update are on disk; previous commits: %s",
			err, rerr, snap, describeCommits(plan.Apps))
	}
	finish()
	return fmt.Errorf("the update failed and was rolled back (apps, toolchain and database as before): %w", err)
}

// planUpdate reads every app's branch and commit and decides what moves.
func (s *Service) planUpdate(b state.Bench, in UpdateInput) (updatePlan, error) {
	var plan updatePlan
	runner := s.quietRunnerFor(b)
	apps := readAppsTxt(b)
	for _, want := range in.Apps {
		if !slices.Contains(apps, want) {
			return plan, fmt.Errorf("bench %q has no app %q", b.Name, want)
		}
	}
	var dirty, ahead []string
	for _, app := range apps {
		// branch, HEAD, upstream/<branch> (or "none"), then changed files.
		o, err := runner.ExecSilent("frappe", "bash", "-c", fmt.Sprintf(
			`cd /workspace/frappe-bench/apps/%s && if ! git rev-parse --git-dir >/dev/null 2>&1; then printf 'nogit\n\nnone\n'; exit 0; fi && `+
				`b=$(git rev-parse --abbrev-ref HEAD) && echo "$b" && git rev-parse HEAD && `+
				`(git rev-parse --verify -q "refs/remotes/upstream/$b" || echo none) && git status --porcelain --untracked-files=no | cut -c4-`,
			bench.ShellQuote(app)))
		if err != nil {
			return plan, fmt.Errorf("read %s's git state: %w\n%s", app, err, lastLines(o, 3))
		}
		lines := strings.Split(o, "\n")
		if len(lines) < 3 {
			return plan, fmt.Errorf("read %s's git state: unexpected output", app)
		}
		st := appState{Name: app, Branch: strings.TrimSpace(lines[0]), Commit: strings.TrimSpace(lines[1])}
		if st.Branch == "nogit" {
			// Not a repository (bench new-app --no-git): never pulled, and a
			// rollback leaves its files alone.
			st = appState{Name: app}
		}
		upstream := strings.TrimSpace(lines[2])
		plan.Apps = append(plan.Apps, st)
		if changed := parseDirtyPaths(app, strings.Join(lines[3:], "\n")); len(changed) > 0 {
			dirty = append(dirty, fmt.Sprintf("%s (%s)", app, strings.Join(changed, ", ")))
		}
		wanted := len(in.Apps) == 0 || slices.Contains(in.Apps, app)
		switch {
		case !wanted:
		case upstream == "none":
			// A local app (bench new-app, archived source): nothing to pull.
			if len(in.Apps) > 0 {
				return plan, fmt.Errorf("%s has no upstream/%s to pull from", app, st.Branch)
			}
			plan.Local = append(plan.Local, app)
		case upstream != st.Commit && in.ToBranch == "":
			// The pull resets to upstream (keeping ffm's shallow clones
			// shallow), which would drop these commits.
			ahead = append(ahead, fmt.Sprintf("%s (HEAD %s, upstream/%s %s)", app, shortCommit(st.Commit), st.Branch, shortCommit(upstream)))
		default:
			plan.Pull = append(plan.Pull, app)
		}
		if in.ToBranch != "" && st.Branch == b.FrappeBranch && st.Branch != in.ToBranch {
			plan.Switch = append(plan.Switch, app)
		}
	}
	if len(dirty) > 0 {
		return plan, fmt.Errorf("%w in %s: commit or stash them first — an update resets onto upstream and a rollback would lose them",
			errDirtyApps, strings.Join(dirty, "; "))
	}
	if len(ahead) > 0 {
		return plan, fmt.Errorf("%w: %s are not at their upstream branch (local or pinned commits); push them, "+
			"or leave those apps out with --apps", errLocalCommits, strings.Join(ahead, "; "))
	}
	if in.ToBranch != "" {
		if !slices.Contains(plan.Switch, "frappe") {
			return plan, fmt.Errorf("frappe is on %q, not on the bench's branch %q; switch it by hand", plan.Apps[0].Branch, b.FrappeBranch)
		}
		plan.Toolchain = bench.ToolchainFor(in.ToBranch)
		if err := plan.Toolchain.ValidateFor(in.ToBranch); err != nil {
			return plan, err
		}
		plan.Rebuild = plan.Toolchain.Node != b.Node
	}
	return plan, nil
}

func printPlan(pw ProgressWriter, b state.Bench, in UpdateInput, plan updatePlan) {
	if in.ToBranch != "" {
		pw.Printf("Major upgrade of %q: %s → %s (Python %s, Node %s)\n", b.Name, b.FrappeBranch, in.ToBranch, plan.Toolchain.Python, plan.Toolchain.Node)
		pw.Printf("  switching: %s\n", strings.Join(plan.Switch, ", "))
	} else {
		pw.Printf("Updating %q: pulling %s\n", b.Name, strings.Join(plan.Pull, ", "))
		if len(plan.Local) > 0 {
			pw.Printf("  not pulled (no upstream remote): %s\n", strings.Join(plan.Local, ", "))
		}
	}
	for _, a := range plan.Apps {
		pw.Printf("  %-20s %s @ %s\n", a.Name, a.Branch, shortCommit(a.Commit))
	}
}

func describeCommits(apps []appState) string {
	var parts []string
	for _, a := range apps {
		parts = append(parts, fmt.Sprintf("%s=%s@%s", a.Name, a.Branch, shortCommit(a.Commit)))
	}
	return strings.Join(parts, " ")
}

// revertFfmPatches undoes ffm's edits to frappe's realtime sources, which
// would otherwise make bench's `git pull --rebase` refuse to run.
const revertFfmPatches = `cd /workspace/frappe-bench/apps/frappe && git checkout -- realtime/middlewares/authenticate.js realtime/utils.js 2>/dev/null || true`

func (s *Service) applyUpdate(b state.Bench, in UpdateInput, plan updatePlan, out io.Writer, pw ProgressWriter) error {
	if out == nil {
		out = io.Discard
	}
	runner := s.runnerFor(b)
	exec := func(step, cmd string) error {
		pw.Step(step)
		fmt.Fprintf(out, "$ %s\n", cmd)
		if err := runner.ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", cmd); err != nil {
			return fmt.Errorf("%s: %w", step, err)
		}
		return nil
	}
	if _, err := runner.ExecSilent("frappe", "bash", "-c", revertFfmPatches); err != nil {
		return fmt.Errorf("revert ffm's frappe patches: %w", err)
	}
	if in.ToBranch != "" {
		// Not bench switch-to-branch: it unshallows every app, refuses a major
		// version without --upgrade, and --upgrade installs the new branch's
		// requirements into the old virtualenv, whose Python the new branch may
		// not support. The toolchain, virtualenv and requirements follow below.
		if err := exec("Switching "+strings.Join(plan.Switch, ", ")+" to "+in.ToBranch,
			switchBranchScript(in.ToBranch, plan.Switch)+" && "+pruneStaleScript(plan.Switch)); err != nil {
			return err
		}
		if err := s.changeToolchain(&b, plan.Toolchain, in.ToBranch, pw); err != nil {
			return err
		}
		runner = s.runnerFor(b)
		// bench setup env keeps an existing virtualenv whatever --python says,
		// so the old one moves aside: a rollback moves it back unchanged.
		if err := exec("Rebuilding the virtualenv on Python "+plan.Toolchain.Python,
			"rm -rf "+prevEnv+" && mv env "+prevEnv+" && bench setup env --python "+bench.ShellQuote(plan.Toolchain.PythonBin())); err != nil {
			return err
		}
	} else {
		apps := []string{}
		for _, a := range plan.Pull {
			apps = append(apps, bench.ShellQuote(a))
		}
		if len(apps) == 0 {
			pw.Step("Nothing to pull (every app is local)")
		} else if err := exec("Pulling "+strings.Join(plan.Pull, ", "),
			// --reset fetches the branch tip and resets to it, keeping the
			// shallow clones ffm creates shallow; without it bench unshallows
			// frappe (its whole history) first. Preflight made sure nothing
			// local is lost.
			"bench update --pull --reset --no-backup --apps "+strings.Join(apps, ",")); err != nil {
			return err
		}
	}
	if err := exec("Installing requirements", "bench setup requirements"); err != nil {
		return err
	}
	if err := exec("Clearing the cache", clearCacheCmd(b)); err != nil {
		return err
	}
	if err := exec("Migrating "+b.SiteName, "bench --site "+bench.ShellQuote(b.SiteName)+" migrate"); err != nil {
		return err
	}
	if err := exec("Building assets", "bench build"); err != nil {
		return err
	}
	if err := s.reapplyPatches(b); err != nil {
		return err
	}
	pw.Step("Restarting the bench's processes")
	if err := s.restartAndWait(b, true); err != nil {
		return err
	}
	if in.ToBranch != "" {
		old := b.FrappeBranch
		if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
			rec.FrappeBranch = in.ToBranch
			for i, sp := range rec.Apps {
				if strings.HasSuffix(sp, "@"+old) {
					rec.Apps[i] = strings.TrimSuffix(sp, "@"+old) + "@" + in.ToBranch
				}
			}
		}); err != nil {
			return fmt.Errorf("update state: %w", err)
		}
	}
	return nil
}

// changeToolchain records a new toolchain and, when Node changes, rebuilds
// the image and recreates the containers on it.
// prevEnv is where a --to-branch update keeps the previous virtualenv until
// it succeeds.
const prevEnv = "env.ffm-prev"

// restorePrevEnv puts the previous virtualenv back when the update moved it.
const restorePrevEnv = "cd /workspace/frappe-bench && if [ -d " + prevEnv + " ]; then rm -rf env && mv " + prevEnv + " env; fi"

// pruneStaleScript deletes bytecode caches and then empty folders in apps.
// A module a branch no longer has leaves its folder behind with only
// __pycache__ in it, which Python then imports as a namespace package
// (__file__ None) and Frappe's migrate fails on (v16 dropped frappe.social).
func pruneStaleScript(apps []string) string {
	dirs := make([]string, 0, len(apps))
	for _, a := range apps {
		dirs = append(dirs, bench.ShellQuote("apps/"+a))
	}
	d := strings.Join(dirs, " ")
	return "find " + d + " -name node_modules -prune -o -name .git -prune -o -type d -name __pycache__ -prune -exec rm -rf {} + " +
		// -delete implies -depth, which turns -prune off: exclude by path.
		"&& find " + d + " -type d -empty -not -path '*/.git/*' -not -path '*/node_modules/*' -delete"
}

// clearCacheCmd empties the site's cache, where Frappe keeps each app's
// module list: a list from before a branch switch names modules that are gone.
func clearCacheCmd(b state.Bench) string {
	return "bench --site " + bench.ShellQuote(b.SiteName) + " clear-cache"
}

// switchBranchScript moves apps to branch with a shallow fetch from their
// upstream remote, and makes upstream/<branch> the new branch's upstream so a
// later update pulls it.
func switchBranchScript(branch string, apps []string) string {
	q := bench.ShellQuote
	var sb strings.Builder
	sb.WriteString("set -e")
	for _, a := range apps {
		fmt.Fprintf(&sb, " && cd %s"+
			" && git remote set-branches --add upstream %s"+
			" && git fetch -q --depth 1 upstream %s"+
			" && git checkout -q -B %s %s"+
			" && git branch -q --set-upstream-to %s"+
			" && cd ../..",
			q("apps/"+a), q(branch),
			q("+refs/heads/"+branch+":refs/remotes/upstream/"+branch),
			q(branch), q("refs/remotes/upstream/"+branch), q("upstream/"+branch))
	}
	return sb.String()
}

func (s *Service) changeToolchain(b *state.Bench, tc bench.Toolchain, branch string, pw ProgressWriter) error {
	nodeChanged := tc.Node != b.Node
	b.Python, b.Node = tc.Python, tc.Node
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) { rec.Python, rec.Node = tc.Python, tc.Node }); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	if !nodeChanged {
		return nil
	}
	data, err := s.composeDataFor(*b)
	if err != nil {
		return err
	}
	if err := bench.WriteDockerfile(b.Dir, data); err != nil {
		return fmt.Errorf("write Dockerfile: %w", err)
	}
	runner := s.runnerFor(*b)
	pw.Step("Rebuilding the image for Node " + tc.Node)
	if err := runner.Build(); err != nil {
		return fmt.Errorf("docker compose build: %w", err)
	}
	if err := runner.Up(); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	return nil
}

func (s *Service) reapplyPatches(b state.Bench) error {
	if err := bench.PatchAuthenticateJs(b.Dir); err != nil {
		return fmt.Errorf("re-apply the realtime auth patch: %w", err)
	}
	if err := bench.PatchUtilsJs(b.Dir); err != nil {
		return fmt.Errorf("re-apply the realtime url patch: %w", err)
	}
	if b.IsDev() {
		_ = bench.PatchProcfileWorker(b.Dir)
	}
	return nil
}

// rollbackUpdate puts code, toolchain and data back as they were.
func (s *Service) rollbackUpdate(b state.Bench, plan updatePlan, snap string, out io.Writer, pw ProgressWriter) error {
	if out == nil {
		out = io.Discard
	}
	cur, err := s.GetBench(b.Name)
	if err != nil {
		return err
	}
	runner := s.runnerFor(cur)
	if _, err := runner.ExecSilent("frappe", "bash", "-c", revertFfmPatches); err != nil {
		return fmt.Errorf("revert ffm's frappe patches: %w", err)
	}
	pw.Step("Putting every app back on its previous commit")
	for _, a := range plan.Apps {
		if a.Commit == "" {
			continue // not a git repository
		}
		cmd := fmt.Sprintf("cd /workspace/frappe-bench/apps/%s && git checkout -q -B %s %s",
			bench.ShellQuote(a.Name), bench.ShellQuote(a.Branch), bench.ShellQuote(a.Commit))
		if o, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
			return fmt.Errorf("check %s out at %s: %w\n%s", a.Name, shortCommit(a.Commit), err, lastLines(o, 3))
		}
	}
	names := make([]string, 0, len(plan.Apps))
	for _, a := range plan.Apps {
		names = append(names, a.Name)
	}
	if o, err := runner.ExecSilent("frappe", "bash", "-c", "cd /workspace/frappe-bench && "+pruneStaleScript(names)); err != nil {
		return fmt.Errorf("remove stale module folders: %w\n%s", err, lastLines(o, 3))
	}
	if cur.Python != b.Python || cur.Node != b.Node {
		if err := s.changeToolchain(&cur, bench.Toolchain{Python: b.Python, Node: b.Node}, b.FrappeBranch, pw); err != nil {
			return err
		}
		runner = s.runnerFor(cur)
		pw.Step("Putting the previous virtualenv back")
		if o, err := runner.ExecSilent("frappe", "bash", "-c", restorePrevEnv); err != nil {
			return fmt.Errorf("put the previous virtualenv back: %w\n%s", err, lastLines(o, 3))
		}
		if b.Python == "" {
			_ = s.UpdateBench(b.Name, func(rec *state.Bench) { rec.Python, rec.Node = "", "" })
		}
	}
	pw.Step("Reinstalling the previous requirements")
	if err := runner.ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", "bench setup requirements"); err != nil {
		return fmt.Errorf("bench setup requirements: %w", err)
	}
	if err := runner.ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", clearCacheCmd(b)); err != nil {
		return fmt.Errorf("clear the cache: %w", err)
	}
	if _, err := s.restoreSnapshotLocked(RestoreSnapshotInput{Bench: b.Name, Name: snap}, pw); err != nil {
		return err
	}
	pw.Step("Rebuilding assets")
	if err := runner.ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", "bench build"); err != nil {
		return fmt.Errorf("bench build: %w", err)
	}
	if err := s.reapplyPatches(b); err != nil {
		return err
	}
	return s.restartAndWait(b, true)
}
