package manager

import (
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// appDrift is one app whose restored code is not the commit the archive
// recorded.
type appDrift struct {
	Name, Archived, Restored string
}

// driftReport compares the restored apps with the archive.
type driftReport struct {
	// Checked counts the apps that had a recorded commit and a readable HEAD.
	Checked int
	Drift   []appDrift
}

// restoredHeads reads HEAD of each named app in one exec. An app that is not
// on the bench, or not a git repository, is absent from the result.
func restoredHeads(runner *bench.Runner, names []string) (map[string]string, error) {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = bench.ShellQuote(n)
	}
	out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench/apps && for d in "+strings.Join(quoted, " ")+
			`; do printf '%s %s\n' "$d" "$(git -C "$d" rev-parse HEAD 2>/dev/null)"; done`)
	if err != nil {
		return nil, err
	}
	heads := make(map[string]string, len(names))
	for _, line := range strings.Split(out, "\n") {
		name, head, _ := strings.Cut(strings.TrimSpace(line), " ")
		if name != "" && head != "" {
			heads[name] = strings.TrimSpace(head)
		}
	}
	return heads, nil
}

// commitDrift compares each app's recorded commit with its restored HEAD.
// Archived apps never appear in apps (see pinnable): they are the backed-up
// tree by construction.
func commitDrift(apps []AppInfo, heads map[string]string) driftReport {
	var r driftReport
	for _, a := range apps {
		head := heads[a.Name]
		if a.Commit == "" || head == "" {
			continue
		}
		r.Checked++
		if !sameCommit(a.Commit, head) {
			r.Drift = append(r.Drift, appDrift{Name: a.Name, Archived: a.Commit, Restored: head})
		}
	}
	return r
}

// sameCommit compares two commit ids, either of which may be abbreviated.
func sameCommit(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if len(a) > len(b) {
		a, b = b, a
	}
	return a != "" && strings.HasPrefix(b, a)
}

// checkDrift reports, before the database is restored and migrated, every app
// whose code is not what the archive recorded. Without --pin-apps that is any
// app whose branch moved since the backup, and nothing used to say so: the
// restore exited 0 and migrate ran the newer code's patches over the restored
// data.
func checkDrift(runner *bench.Runner, plan restorePlan, pinned bool, pw ProgressWriter) driftReport {
	apps := plan.pinnable()
	var names []string
	for _, a := range apps {
		if a.Commit != "" {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return driftReport{}
	}
	heads, err := restoredHeads(runner, names)
	if err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: could not read the restored apps' commits to compare them "+
			"with the archive: %v\n", err)
		return driftReport{}
	}
	r := commitDrift(apps, heads)
	if len(r.Drift) == 0 {
		return r
	}
	fmt.Fprintf(pw.Stderr(), "warning: %d app(s) are not at the commit the archive recorded; "+
		"bench migrate and bench build run on this code:\n", len(r.Drift))
	for _, d := range r.Drift {
		fmt.Fprintf(pw.Stderr(), "  %-14s archived %s  restored %s\n", d.Name, shortCommit(d.Archived), shortCommit(d.Restored))
	}
	if !pinned {
		fmt.Fprintln(pw.Stderr(), "  Restore with --pin-apps to get the archived commits.")
	}
	return r
}

// summaryLine is the restore summary's account of the app code, or "" when
// there was nothing to compare.
func (r driftReport) summaryLine() string {
	switch {
	case r.Checked == 0:
		return ""
	case len(r.Drift) == 0:
		return "at the commits the archive recorded"
	default:
		return fmt.Sprintf("%d of %d apps are NOT at the archived commit (see the warning above)",
			len(r.Drift), r.Checked)
	}
}

// hasRecordedCommits reports whether any app the plan clones has a commit the
// archive recorded, i.e. whether pinning would change anything.
func hasRecordedCommits(plan restorePlan) bool {
	for _, a := range plan.pinnable() {
		if a.Commit != "" {
			return true
		}
	}
	return false
}
