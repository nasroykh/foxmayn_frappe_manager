package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

// Snapshots are quick, local rollback points for one bench: the site's
// database (and optionally its files) as Frappe's own backup writes them.
//
// They live in <bench>/workspace/.ffm-snapshots/<name>/, inside the bind
// mount, so `bench backup` writes them in place and nothing is streamed.
// They are not backups: they go away with the bench (delete, recreate), stay
// on this host and restore in place. `ffm backup` is the portable copy.
const snapshotsDir = ".ffm-snapshots"

// snapshotMeta is <snapshot>/snapshot.json.
type snapshotMeta struct {
	Name       string            `json:"name"`
	Bench      string            `json:"bench"`
	Site       string            `json:"site"`
	CreatedAt  time.Time         `json:"created_at"`
	Files      bool              `json:"files"`
	FfmVersion string            `json:"ffm_version"`
	Commits    map[string]string `json:"commits,omitempty"`
}

// Snapshot is one snapshot as listed.
type Snapshot struct {
	Name      string
	CreatedAt time.Time
	Files     bool
	Size      int64
	// Commits maps each app to the commit it was at when the snapshot was
	// taken; restoring onto other code may need bench migrate.
	Commits map[string]string
}

var snapshotNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// SnapshotInput takes a snapshot.
type SnapshotInput struct {
	Bench string
	// Name defaults to the UTC time, e.g. 20261008-104500.
	Name string
	// Files also captures the site's public and private files.
	Files bool
}

func snapshotRoot(b state.Bench) string {
	return filepath.Join(b.Dir, "workspace", snapshotsDir)
}

// snapshotContainerPath is where the frappe container sees a snapshot.
func snapshotContainerPath(name string) string {
	return "/workspace/" + snapshotsDir + "/" + name
}

// CreateSnapshot dumps the site's database (and with Files its attachments)
// into a named snapshot.
func (s *Service) CreateSnapshot(in SnapshotInput, pw ProgressWriter) (string, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	name := in.Name
	if name == "" {
		name = s.clock().UTC().Format("20060102-150405")
	}
	if !snapshotNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid snapshot name %q: letters, digits, '.', '_' and '-', up to 64", name)
	}
	release, err := s.lockBench(in.Bench)
	if err != nil {
		return "", err
	}
	defer release()
	return s.createSnapshotLocked(in, name, pw)
}

// createSnapshotLocked is CreateSnapshot for a caller holding the bench lock.
func (s *Service) createSnapshotLocked(in SnapshotInput, name string, pw ProgressWriter) (string, error) {
	b, err := s.GetBench(in.Bench)
	if err != nil {
		return "", err
	}
	root := snapshotRoot(b)
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err == nil {
		return "", fmt.Errorf("snapshot %q of %q already exists", name, b.Name)
	}
	// 0700: the dump holds every password hash and API secret of the site.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}

	runner := s.runnerFor(b)
	remote := snapshotContainerPath(name)
	q := bench.ShellQuote
	cmd := fmt.Sprintf("umask 077 && mkdir -p %s && cd /workspace/frappe-bench && bench --site %s backup --verbose --compress"+
		" --backup-path-db %s/database.sql.gz --backup-path-conf %s/site_config.json",
		remote, q(b.SiteName), remote, remote)
	if in.Files {
		cmd += fmt.Sprintf(" --with-files --backup-path-files %s/public-files.tgz --backup-path-private-files %s/private-files.tgz", remote, remote)
	}
	pw.Step(fmt.Sprintf("Snapshotting %q as %q", b.Name, name))
	if out, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
		_ = os.RemoveAll(dir)
		_, _ = runner.ExecSilent("frappe", "rm", "-rf", remote)
		return "", fmt.Errorf("bench backup: %w\n%s", err, benchBackupFailure(out, s.Verbose))
	}

	meta := snapshotMeta{Name: name, Bench: b.Name, Site: b.SiteName, CreatedAt: s.clock().UTC(),
		Files: in.Files, FfmVersion: version.Version, Commits: appCommits(runner)}
	raw, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), append(raw, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write snapshot.json: %w", err)
	}
	pw.Printf("Snapshot %q of %q taken. Roll back with: ffm snapshot restore %s --name %s\n", name, b.Name, b.Name, name)
	return name, nil
}

// appCommits records the commit of every app, for the warning on restore.
func appCommits(runner *bench.Runner) map[string]string {
	out, err := runner.ExecSilent("frappe", "bash", "-c",
		`cd /workspace/frappe-bench/apps && for d in */; do printf '%s %s\n' "${d%/}" "$(git -C "$d" rev-parse HEAD 2>/dev/null)"; done`)
	if err != nil {
		return nil
	}
	commits := map[string]string{}
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) == 2 {
			commits[f[0]] = f[1]
		}
	}
	return commits
}

// ListSnapshots returns a bench's snapshots, newest first.
func (s *Service) ListSnapshots(name string) ([]Snapshot, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(snapshotRoot(b))
	if os.IsNotExist(err) {
		return []Snapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Snapshot{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(snapshotRoot(b), e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
		if err != nil {
			continue // incomplete: CreateSnapshot writes the metadata last
		}
		var m snapshotMeta
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		out = append(out, Snapshot{Name: e.Name(), CreatedAt: m.CreatedAt, Files: m.Files, Size: dirSize(dir), Commits: m.Commits})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// RestoreSnapshotInput rolls a bench back to a snapshot.
type RestoreSnapshotInput struct {
	Bench string
	// Name defaults to the newest snapshot.
	Name string
	// Migrate runs bench migrate afterwards, for code that moved on.
	Migrate bool
}

// RestoreSnapshot replaces the site's database (and, when the snapshot has
// them, its files) with the snapshot's, in place.
//
// Unlike `ffm restore`, which only ever creates a bench, this overwrites the
// running site: that is the point of a rollback point. Files added after the
// snapshot stay, because Frappe's restore extracts over the directory.
func (s *Service) RestoreSnapshot(in RestoreSnapshotInput, pw ProgressWriter) (string, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	release, err := s.lockBench(in.Bench)
	if err != nil {
		return "", err
	}
	defer release()
	return s.restoreSnapshotLocked(in, pw)
}

// restoreSnapshotLocked is RestoreSnapshot for a caller holding the bench lock.
func (s *Service) restoreSnapshotLocked(in RestoreSnapshotInput, pw ProgressWriter) (string, error) {
	b, err := s.GetBench(in.Bench)
	if err != nil {
		return "", err
	}
	snaps, err := s.ListSnapshots(b.Name)
	if err != nil {
		return "", err
	}
	var snap *Snapshot
	for i := range snaps {
		if in.Name == "" || snaps[i].Name == in.Name {
			snap = &snaps[i]
			break
		}
	}
	if snap == nil {
		if in.Name == "" {
			return "", fmt.Errorf("bench %q has no snapshots (take one with: ffm snapshot create %s)", b.Name, b.Name)
		}
		return "", fmt.Errorf("bench %q has no snapshot %q (see: ffm snapshot list %s)", b.Name, in.Name, b.Name)
	}

	runner := s.runnerFor(b)
	remote := snapshotContainerPath(snap.Name)
	rootUser := "root"
	if b.IsPostgres() {
		rootUser = "postgres"
	}
	q := bench.ShellQuote
	cmd := fmt.Sprintf(`cd /workspace/frappe-bench && IFS= read -r p && bench --site %s restore %s/database.sql.gz --db-root-username %s --db-root-password "$p" --force`,
		q(b.SiteName), remote, rootUser)
	if snap.Files {
		cmd += fmt.Sprintf(" --with-public-files %s/public-files.tgz --with-private-files %s/private-files.tgz", remote, remote)
	}
	pw.Step(fmt.Sprintf("Rolling %q back to snapshot %q", b.Name, snap.Name))
	if err := runner.ExecStdin("frappe", strings.NewReader(b.DBPassword+"\n"), "bash", "-c", cmd); err != nil {
		return "", fmt.Errorf("bench restore: %w", err)
	}
	if err := afterDatabaseSwap(runner, b, in.Migrate, pw); err != nil {
		return "", err
	}
	if moved := movedApps(snap.Commits, appCommits(runner)); len(moved) > 0 && !in.Migrate {
		pw.Printf("  The code of %s changed since the snapshot; if the site misbehaves, run bench migrate (or restore with --migrate).\n",
			strings.Join(moved, ", "))
	}
	pw.Printf("Bench %q rolled back to snapshot %q.\n", b.Name, snap.Name)
	return snap.Name, nil
}

// afterDatabaseSwap runs what a site needs once its database was replaced
// underneath it: optionally a migrate, then a cache clear, because Redis still
// holds documents and settings read from the old database.
func afterDatabaseSwap(runner *bench.Runner, b state.Bench, migrate bool, pw ProgressWriter) error {
	site := "cd /workspace/frappe-bench && bench --site " + bench.ShellQuote(b.SiteName)
	if migrate {
		pw.Step("Running bench migrate")
		if out, err := runner.ExecSilent("frappe", "bash", "-c", site+" migrate"); err != nil {
			return fmt.Errorf("bench migrate: %w\n%s", err, lastLines(out, 15))
		}
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c", site+" clear-cache"); err != nil {
		return fmt.Errorf("bench clear-cache: %w\n%s", err, lastLines(out, 5))
	}
	return nil
}

// movedApps lists the apps whose commit differs from the snapshot's.
func movedApps(then, now map[string]string) []string {
	var out []string
	for app, c := range then {
		if n, ok := now[app]; ok && c != "" && n != c {
			out = append(out, app)
		}
	}
	sort.Strings(out)
	return out
}

// DeleteSnapshot removes one snapshot.
func (s *Service) DeleteSnapshot(benchName, name string) error {
	if !snapshotNameRe.MatchString(name) {
		return fmt.Errorf("invalid snapshot name %q", name)
	}
	b, err := s.GetBench(benchName)
	if err != nil {
		return err
	}
	dir := filepath.Join(snapshotRoot(b), name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("bench %q has no snapshot %q", b.Name, name)
	}
	if err := os.RemoveAll(dir); err != nil {
		// Written by the container user; a host user that is not it may
		// need the container to delete them.
		if out, rerr := s.runnerFor(b).ExecSilent("frappe", "rm", "-rf", snapshotContainerPath(name)); rerr != nil {
			return fmt.Errorf("delete snapshot: %w (%s)", err, out)
		}
	}
	return nil
}
