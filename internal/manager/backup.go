package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// stagingRoot is where the backup is assembled inside the container. It is
// deliberately NOT under /workspace: that path is a bind mount, so a staging
// directory there would survive a crash as litter inside the user's bench, be
// swept into the next backup's size estimate, and be owned by the container
// user on a host that may not share its uid.
const stagingRoot = "/tmp/ffm-backup"

// Backup archives a bench's site into a single portable file.
//
// The archive is logical, not physical: Frappe's own dump plus its file
// tarballs plus the metadata needed to rebuild the bench around them. On a real
// bench that is a 45x-to-2000x size difference against snapshotting the
// database volume and the workspace, it needs no downtime, and it restores onto
// a different host, a different architecture and a different host uid — none of
// which a physical copy of a MariaDB data directory or a Python venv can do.
func (s *Service) Backup(in BackupInput, pw ProgressWriter) error {
	b, err := s.GetBench(in.BenchName)
	if err != nil {
		return err
	}
	release, err := s.lockBench(b.Name)
	if err != nil {
		return err
	}
	defer release()
	return s.backupLocked(in, pw)
}

// backupLocked is Backup for a caller that already holds the bench's lock
// (run-due, which holds it across backup and prune).
func (s *Service) backupLocked(in BackupInput, pw ProgressWriter) (backupErr error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(in.BenchName)
	if err != nil {
		return err
	}
	trigger := in.Trigger
	if trigger == "" {
		trigger = TriggerManual
	}
	if trigger != TriggerManual && trigger != TriggerScheduled {
		return fmt.Errorf("unknown backup trigger %q", trigger)
	}
	if _, err := os.Stat(b.Dir); err != nil {
		return fmt.Errorf("bench directory %s is missing — nothing to back up", b.Dir)
	}

	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
	frappeBench := filepath.Join(b.Dir, "workspace", "frappe-bench")

	// Read the site's identity from the host side of the bind mount. Doing this
	// before touching Docker means a bench whose containers are broken can still
	// be archived, which is precisely when a backup is most wanted.
	siteCfg, err := readJSONFile(filepath.Join(frappeBench, "sites", b.SiteName, "site_config.json"))
	if err != nil {
		return fmt.Errorf("read site_config.json for %s: %w", b.SiteName, err)
	}
	commonCfg, err := readJSONFile(filepath.Join(frappeBench, "sites", "common_site_config.json"))
	if err != nil {
		// Not fatal: the site config is what a restore needs, the common config
		// is a bonus that create can largely regenerate.
		fmt.Fprintf(pw.Stderr(), "warning: could not read common_site_config.json: %v\n", err)
		commonCfg = map[string]any{}
	}

	// Bring the bench up if it is down. `bench backup` needs the database, and
	// refusing here would block the most valuable moment to take a backup: just
	// before `ffm delete` or `ffm recreate`.
	startedForBackup := false
	if status := s.LiveStatus(b); status != "running" {
		if in.SkipIfStopped {
			// "unknown" means docker could not be asked at all — under cron,
			// typically a PATH without docker. Reporting that as "stopped"
			// would turn a broken scheduler into a silent string of skips.
			if status == "unknown" {
				return fmt.Errorf("could not query Docker for the state of %q — is docker on "+
					"PATH and the daemon running?", b.Name)
			}
			return ErrBenchStopped
		}
		pw.Step("Starting the bench for the backup (it was stopped)")
		if err := runner.UpServices(dbService(b), "frappe"); err != nil {
			return fmt.Errorf("start bench for backup: %w", err)
		}
		startedForBackup = true
		defer func() {
			pw.Step("Stopping the bench again")
			if err := runner.Stop(); err != nil {
				fmt.Fprintf(pw.Stderr(), "warning: could not stop the bench again: %v\n", err)
			}
		}()
		if err := s.waitForDB(runner, b, pw); err != nil {
			return err
		}
	}

	now := time.Now().UTC()
	staging := fmt.Sprintf("%s-%d", stagingRoot, now.UnixNano())
	defer func() {
		if _, err := runner.ExecSilent("frappe", "rm", "-rf", staging); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not remove %s in the container: %v\n", staging, err)
		}
	}()

	pw.Step("Collecting bench and app metadata")
	apps := collectAppInfo(runner, appNames(frappeBench), pw)
	installed, installedSource := collectInstalledApps(runner, frappeBench, b.SiteName, siteCfg)
	frappeVersion := readFrappeVersion(frappeBench)

	withFiles := !in.NoFiles
	tiers := []string{TierCore}
	if withFiles {
		tiers = append(tiers, TierFiles)
	}

	// Take the Frappe backup into an explicit staging directory. Never the
	// default sites/<site>/private/backups: new_backup() calls
	// delete_temp_backups() first, so that directory is pruned on every run and
	// is a moving target rather than a place to read from.
	pw.Step("Running bench backup" + filesSuffix(withFiles))
	dumpPaths, err := runBenchBackup(runner, b.SiteName, staging, withFiles)
	if err != nil {
		return err
	}

	// Frappe encrypts the dump when System Settings has encrypt_backup on, and
	// keeps the .sql.gz name while doing it, so the encoding has to be probed
	// and recorded rather than inferred from the extension by whoever reads it.
	// Every member is probed, not just the dump. With System Settings
	// encrypt_backup on, Frappe encrypts the file tarballs too and leaves their
	// .tgz names untouched, so labelling them by extension would record GPG
	// bytes as gzip and mislead every future reader.
	encodings := probeEncodings(runner, dumpPaths.all())
	if encodings[dumpPaths.db] == archive.EncodingNone {
		if err := validateDump(runner, dumpPaths.db); err != nil {
			return err
		}
	} else {
		pw.Printf("  The database dump is encrypted; its key is stored in the archive.\n")
	}

	sizes, err := statSizes(runner, dumpPaths.all())
	if err != nil {
		return err
	}
	if err := s.checkBackupSpace(in, sizes, pw); err != nil {
		return err
	}

	dest, err := backupDestination(in, b.Name, trigger, now)
	if err != nil {
		return err
	}
	if !honoursFileModes(filepath.Dir(dest)) {
		fmt.Fprintf(pw.Stderr(),
			"warning: %s is on a filesystem that ignores Unix permissions, so the archive cannot be\n"+
				"         restricted to your user. It contains the database root password, the\n"+
				"         Administrator password and the site encryption key — treat it accordingly.\n",
			filepath.Dir(dest))
	}

	pw.Step("Writing " + filepath.Base(dest))
	w, err := archive.Create(dest)
	if err != nil {
		return err
	}
	defer func() {
		if backupErr != nil {
			w.Abort()
		}
	}()

	header := NewHeader(b, b.SiteName, frappeVersion, in.Label, tiers, now)
	header.Trigger = trigger
	headerJSON, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return err
	}
	var members []archive.Member
	add := func(m archive.Member, err error) error {
		if err != nil {
			return err
		}
		members = append(members, m)
		return nil
	}

	// The header goes first so a reader can preflight in one read.
	if err := add(w.AddBytes(archive.HeaderName, headerJSON, archive.EncodingNone)); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		data any
	}{
		{archive.Prefix + "/site/site_config.json", siteCfg},
		{archive.Prefix + "/site/common_site_config.json", commonCfg},
	} {
		raw, err := json.MarshalIndent(f.data, "", "  ")
		if err != nil {
			return err
		}
		if err := add(w.AddBytes(f.name, raw, archive.EncodingNone)); err != nil {
			return err
		}
	}
	for _, d := range dumpPaths.members(withFiles) {
		size, ok := sizes[d.remote]
		if !ok {
			continue
		}
		enc := encodings[d.remote]
		if enc == archive.EncodingNone && d.name != dumpPaths.confName() {
			// Not encrypted, and everything except the config member is
			// produced by `bench backup --compress`.
			enc = archive.EncodingGzip
		}
		pw.Step(fmt.Sprintf("Adding %s (%s)", filepath.Base(d.name), humanBytes(size)))
		if err := add(streamMember(runner, w, d.name, d.remote, size, enc)); err != nil {
			return err
		}
	}

	manifest := Manifest{
		Header: header,
		Bench:  b,
		Site: SiteInfo{
			SiteName:            b.SiteName,
			DBName:              stringField(siteCfg, "db_name"),
			DBUser:              stringField(siteCfg, "db_user"),
			InstalledApps:       installed,
			InstalledAppsSource: installedSource,
			SiteConfig:          siteCfg,
			CommonSiteConfig:    commonCfg,
		},
		Apps: apps,
		Secrets: Secrets{
			AdminPassword:       b.AdminPassword,
			DBRootPassword:      b.DBPassword,
			SiteDBPassword:      stringField(siteCfg, "db_password"),
			EncryptionKey:       stringField(siteCfg, "encryption_key"),
			BackupEncryptionKey: stringField(siteCfg, "backup_encryption_key"),
		},
		Members: members,
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	// The manifest goes last, so its absence is what identifies a truncated
	// archive. Nothing may be written after it.
	if _, err := w.AddBytes(archive.ManifestName, manifestJSON, archive.EncodingNone); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	info, _ := os.Stat(dest)
	pw.Printf("\nBacked up %q.\n", b.Name)
	pw.Printf("  Archive:   %s\n", dest)
	if info != nil {
		pw.Printf("  Size:      %s\n", humanBytes(info.Size()))
	}
	pw.Printf("  Site:      %s\n", b.SiteName)
	pw.Printf("  Contents:  %s\n", strings.Join(tiers, ", "))
	if len(installed) > 0 {
		pw.Printf("  Apps:      %s\n", strings.Join(installed, ", "))
	}
	for _, a := range apps {
		if a.Dirty {
			fmt.Fprintf(pw.Stderr(),
				"warning: app %q has uncommitted changes that a restore cannot reproduce, "+
					"because it rebuilds the app from commit %s:\n         %s\n",
				a.Name, shortCommit(a.Commit), strings.Join(a.DirtyPaths, ", "))
		}
	}
	if startedForBackup {
		pw.Printf("  Note:      the bench was started for this backup and has been stopped again.\n")
	}
	pw.Printf("\nRestore it with:\n  ffm restore %s <newname>\n", dest)
	return nil
}

// dumpSet is where Frappe wrote each part of its backup inside the container.
type dumpSet struct {
	db      string
	conf    string
	public  string
	private string
}

func (d dumpSet) all() []string {
	out := []string{d.db, d.conf}
	if d.public != "" {
		out = append(out, d.public)
	}
	if d.private != "" {
		out = append(out, d.private)
	}
	return out
}

func (d dumpSet) confName() string { return archive.Prefix + "/site/site_config_backup.json" }

// member pairs an in-archive name with the container path it is read from.
type member struct{ name, remote string }

func (d dumpSet) members(withFiles bool) []member {
	out := []member{
		{archive.Prefix + "/db/database.sql.gz", d.db},
		{d.confName(), d.conf},
	}
	if withFiles && d.public != "" {
		out = append(out, member{archive.Prefix + "/files/public-files.tgz", d.public})
	}
	if withFiles && d.private != "" {
		out = append(out, member{archive.Prefix + "/files/private-files.tgz", d.private})
	}
	return out
}

// runBenchBackup invokes Frappe's own backup with every output path pinned.
func runBenchBackup(runner *bench.Runner, siteName, staging string, withFiles bool) (dumpSet, error) {
	d := dumpSet{
		db:   staging + "/database.sql.gz",
		conf: staging + "/site_config_backup.json",
	}
	// --verbose is not for the happy path: the output is captured and thrown
	// away on success. It is the only way to get a cause out of a failure.
	// `bench backup` wraps the whole operation in a bare `except Exception` and
	// prints "Database or site_config.json may be corrupted" for everything —
	// a dropped packet between containers, a full disk, a real corruption —
	// then prints the traceback only when verbose is set. Without it the
	// exception is discarded before ffm ever sees it.
	cmd := fmt.Sprintf("mkdir -p %s && cd /workspace/frappe-bench && bench --site %s backup --verbose --compress"+
		" --backup-path-db %s --backup-path-conf %s", staging, bench.ShellQuote(siteName), d.db, d.conf)
	if withFiles {
		d.public = staging + "/public-files.tgz"
		d.private = staging + "/private-files.tgz"
		cmd += fmt.Sprintf(" --with-files --backup-path-files %s --backup-path-private-files %s",
			d.public, d.private)
	}
	out, err := runner.ExecSilent("frappe", "bash", "-c", cmd)
	if err == nil {
		return d, nil
	}
	return dumpSet{}, fmt.Errorf("bench backup: %w\n%s", err, benchBackupFailure(out, runner.Verbose))
}

const tracebackMarker = "Traceback (most recent call last)"

// exceptionLine matches the last line of a Python traceback: the exception's
// dotted type followed by its message.
var exceptionLine = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*(Error|Exception|Exit|Interrupt)(: .*)?$`)

// secretAssignment matches the ways a credential appears in a Python frame
// dump: a keyword argument (password='x') and a dictionary entry
// ('password': 'x' or "password": "x"), where the key itself is quoted — in
// either style — and so sits between the name and the separator.
var secretAssignment = regexp.MustCompile(
	`(?i)(['"]?[a-z_]*(?:password|passwd|secret|token|encryption_key)['"]?\s*[=:]\s*)('[^']*'|"[^"]*")`)

// benchBackupFailure turns `bench backup --verbose` output into an error body.
//
// The traceback cannot be shown as-is. frappe.get_traceback runs with
// with_context=True, so every frame is followed by a dump of its locals: it
// runs to hundreds of lines on a real backup, and the database frames carry the
// site's database password in clear. So the default is Frappe's own messages
// plus the traceback's final line, which is the part that names the cause;
// --verbose opts into the whole thing, still with the passwords removed.
func benchBackupFailure(out string, verbose bool) string {
	out = secretAssignment.ReplaceAllString(out, "${1}'[redacted]'")
	head, traceback, found := strings.Cut(out, tracebackMarker)
	if !found {
		return strings.TrimSpace(out)
	}
	parts := []string{strings.TrimSpace(head)}
	if verbose {
		parts = append(parts, tracebackMarker+traceback)
	} else if cause := lastExceptionLine(traceback); cause != "" {
		parts = append(parts, cause,
			"(run ffm --verbose backup for Frappe's full traceback)")
	}
	var body []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			body = append(body, p)
		}
	}
	return strings.Join(body, "\n")
}

// lastExceptionLine returns the final "SomeError: message" line of a traceback.
func lastExceptionLine(traceback string) string {
	lines := strings.Split(traceback, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); exceptionLine.MatchString(line) {
			return line
		}
	}
	return ""
}

// probeEncodings asks file(1) what each backup file actually is.
//
// The name cannot be trusted: an encrypted Frappe backup keeps the extension it
// would have had in the clear, so "…database.sql.gz" is routinely GPG/AES256.
func probeEncodings(runner *bench.Runner, paths []string) map[string]string {
	out := make(map[string]string, len(paths))
	for _, p := range paths {
		out[p] = archive.EncodingNone
		desc, err := runner.ExecSilent("frappe", "file", "-b", p)
		if err != nil {
			continue
		}
		upper := strings.ToUpper(desc)
		if strings.Contains(upper, "GPG") || strings.Contains(upper, "PGP") {
			out[p] = archive.EncodingGPG
		}
	}
	return out
}

// validateDump checks that the dump contains Frappe's authentication table.
//
// This is the same shape of check Frappe performs at RESTORE time, run here
// instead: an empty or truncated dump should fail the backup that produced it,
// not the restore months later that needed it.
func validateDump(runner *bench.Runner, path string) error {
	out, err := runner.ExecSilent("frappe", "bash", "-c",
		fmt.Sprintf("zgrep -c -m1 '__Auth' %s || true", path))
	if err != nil {
		return fmt.Errorf("validate dump: %w\n%s", err, out)
	}
	if strings.TrimSpace(out) == "0" || strings.TrimSpace(out) == "" {
		return fmt.Errorf("the database dump does not contain Frappe's __Auth table — " +
			"it is empty or truncated, so the backup has been discarded")
	}
	return nil
}

// statSizes reads the byte size of each container path.
func statSizes(runner *bench.Runner, paths []string) (map[string]int64, error) {
	sizes := make(map[string]int64, len(paths))
	for _, p := range paths {
		out, err := runner.ExecSilent("frappe", "stat", "-c", "%s", p)
		if err != nil {
			return nil, fmt.Errorf("stat %s: %w\n%s", p, err, out)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("stat %s: unexpected size %q", p, out)
		}
		sizes[p] = n
	}
	return sizes, nil
}

// streamMember copies one container file into the archive without buffering it.
func streamMember(runner *bench.Runner, w *archive.Writer, name, remote string, size int64, encoding string) (archive.Member, error) {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		err := runner.ExecStream("frappe", pw, "cat", remote)
		pw.CloseWithError(err)
		errc <- err
	}()
	m, addErr := w.AddStream(name, size, pr, encoding)
	pr.CloseWithError(addErr)
	if streamErr := <-errc; streamErr != nil {
		return archive.Member{}, fmt.Errorf("read %s from the container: %w", remote, streamErr)
	}
	if addErr != nil {
		return archive.Member{}, addErr
	}
	return m, nil
}

// appNames lists the apps present in the bench's apps/ directory.
func appNames(frappeBench string) []string {
	entries, err := os.ReadDir(filepath.Join(frappeBench, "apps"))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// collectAppInfo records each app's git provenance.
//
// The commit comes from git, never from sites/apps.json: on a bench ffm created,
// that file records a null commit_hash for frappe itself, because bench init
// runs in a temporary directory that is copied into place afterwards.
func collectAppInfo(runner *bench.Runner, names []string, pw ProgressWriter) []AppInfo {
	var apps []AppInfo
	for _, name := range names {
		dir := "/workspace/frappe-bench/apps/" + name
		info := AppInfo{Name: name}
		if out, err := runner.ExecSilent("frappe", "git", "-C", dir, "rev-parse", "HEAD"); err == nil {
			info.Commit = strings.TrimSpace(out)
		}
		if out, err := runner.ExecSilent("frappe", "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			info.Branch = strings.TrimSpace(out)
		}
		if out, err := runner.ExecSilent("frappe", "git", "-C", dir, "config", "--get", "remote.origin.url"); err == nil {
			info.Remote = strings.TrimSpace(out)
		}
		// Two prefix-free queries rather than `status --porcelain`: porcelain
		// prefixes each path with a two-character status and a space, and
		// ExecSilent trims the combined output, which eats the first line's
		// leading space and shifts that one path by a character.
		var changed string
		if out, err := runner.ExecSilent("frappe", "git", "-C", dir, "diff", "--name-only", "HEAD"); err == nil {
			changed = out
		}
		if out, err := runner.ExecSilent("frappe", "git", "-C", dir,
			"ls-files", "--others", "--exclude-standard"); err == nil && out != "" {
			changed = strings.TrimSpace(changed + "\n" + out)
		}
		info.DirtyPaths = parseDirtyPaths(name, changed)
		info.Dirty = len(info.DirtyPaths) > 0
		apps = append(apps, info)
	}
	return apps
}

// maxDirtyPaths caps how many modified files are recorded per app, so a bench
// with a large uncommitted working tree cannot bloat the manifest.
const maxDirtyPaths = 20

// ffmPatchedPaths are the files ffm edits inside apps/frappe itself.
//
// PatchAuthenticateJs and PatchUtilsJs run on every create, start and
// rebuild, so frappe's working tree is modified on every bench ffm has ever
// made. Restore re-applies both, which makes them reproducible by definition —
// counting them as "uncommitted changes" would put a warning on every backup
// and teach the user to ignore the one that matters.
var ffmPatchedPaths = map[string]bool{
	"realtime/middlewares/authenticate.js": true,
	"realtime/utils.js":                    true,
}

// parseDirtyPaths filters a newline-separated list of changed paths down to the
// ones ffm did not modify itself, de-duplicating along the way — a file can be
// reported by more than one git query.
func parseDirtyPaths(app, changed string) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, line := range strings.Split(changed, "\n") {
		path := strings.Trim(strings.TrimSpace(line), `"`)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		if app == "frappe" && ffmPatchedPaths[path] {
			continue
		}
		if len(paths) == maxDirtyPaths {
			paths = append(paths, "…")
			break
		}
		paths = append(paths, path)
	}
	return paths
}

// shortCommit abbreviates a git SHA for human output.
func shortCommit(commit string) string {
	if len(commit) > 8 {
		return commit[:8]
	}
	if commit == "" {
		return "an unknown commit"
	}
	return commit
}

// collectInstalledApps returns the site's app list and where it came from.
//
// The database is the authority, but a bench whose web stack is broken can
// still be archived, so this degrades to the on-disk sources rather than
// failing the backup.
func collectInstalledApps(runner *bench.Runner, frappeBench, siteName string, siteCfg map[string]any) ([]string, string) {
	out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && bench --site "+bench.ShellQuote(siteName)+" list-apps --format json")
	if err == nil {
		var parsed map[string]any
		if json.Unmarshal([]byte(out), &parsed) == nil {
			if apps := appListFromSiteMap(parsed, siteName); len(apps) > 0 {
				return apps, "list-apps"
			}
		}
	}
	if raw, err := os.ReadFile(filepath.Join(frappeBench, "sites", "apps.txt")); err == nil {
		var apps []string
		for _, line := range strings.Split(string(raw), "\n") {
			if l := strings.TrimSpace(line); l != "" {
				apps = append(apps, l)
			}
		}
		if len(apps) > 0 {
			return apps, "apps.txt"
		}
	}
	if raw, ok := siteCfg["installed_apps"].([]any); ok {
		var apps []string
		for _, v := range raw {
			if s, ok := v.(string); ok {
				apps = append(apps, s)
			}
		}
		return apps, "site_config"
	}
	return nil, ""
}

// appListFromSiteMap pulls the app names out of `bench list-apps --format json`,
// whose shape is {"<site>": [{"app_name": ...}, ...]} or {"<site>": ["app"]}.
func appListFromSiteMap(parsed map[string]any, siteName string) []string {
	entries, ok := parsed[siteName].([]any)
	if !ok {
		for _, v := range parsed {
			if entries, ok = v.([]any); ok {
				break
			}
		}
	}
	var apps []string
	for _, e := range entries {
		switch v := e.(type) {
		case string:
			apps = append(apps, v)
		case map[string]any:
			if name, ok := v["app_name"].(string); ok {
				apps = append(apps, name)
			}
		}
	}
	return apps
}

// readFrappeVersion reads the framework version straight from the source tree,
// so it works whether or not the containers are healthy.
func readFrappeVersion(frappeBench string) string {
	raw, err := os.ReadFile(filepath.Join(frappeBench, "apps", "frappe", "frappe", "__init__.py"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "__version__") {
			continue
		}
		if _, v, ok := strings.Cut(line, "="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// readJSONFile decodes a JSON object from the host filesystem.
func readJSONFile(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Base(path), err)
	}
	return out, nil
}

// stringField reads a string value out of a decoded JSON object.
func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// dbService returns the compose service name for a bench's database.
func dbService(b state.Bench) string {
	if b.IsPostgres() {
		return "postgres"
	}
	return "mariadb"
}

// waitForDB blocks until the bench's database accepts connections.
func (s *Service) waitForDB(runner *bench.Runner, b state.Bench, pw ProgressWriter) error {
	return waitForDBReady(runner, b.IsPostgres(), b.DBPassword, pw.Stderr())
}

// waitForDBReady waits for the database twice over: once from inside the
// database container, and once from the frappe container that has to reach it.
//
// The second probe is not redundant. The first one talks to the database over
// its own loopback and so cannot observe the network between the two
// containers; when that network is broken the wait passes in zero seconds and
// the failure surfaces much later, as a Frappe error blaming the site.
func waitForDBReady(runner *bench.Runner, isPostgres bool, dbPassword string, w io.Writer) error {
	host, port := "mariadb", 3306
	if isPostgres {
		host, port = "postgres", 5432
		if err := runner.WaitForPostgres(dbPassword, 90*time.Second, w); err != nil {
			return fmt.Errorf("wait for PostgreSQL: %w", err)
		}
	} else if err := runner.WaitForMariaDB(dbPassword, 90*time.Second, w); err != nil {
		return fmt.Errorf("wait for MariaDB: %w", err)
	}
	return runner.WaitForDBFromFrappe(host, port, 60*time.Second)
}

// backupDestination resolves --out into a concrete archive path.
func backupDestination(in BackupInput, benchName, trigger string, now time.Time) (string, error) {
	base := archiveFileName(benchName, trigger, now)
	if trigger == TriggerScheduled {
		// Scheduled archives always land in the bench's own backup directory:
		// that directory is the only place pruning looks, so an archive written
		// anywhere else would never be rotated.
		dir, err := config.EnsureBenchBackupsDir(benchName)
		if err != nil {
			return "", fmt.Errorf("create backup directory: %w", err)
		}
		return filepath.Join(dir, base), nil
	}

	if in.Out == "" {
		dir, err := config.EnsureBenchBackupsDir(benchName)
		if err != nil {
			return "", fmt.Errorf("create backup directory: %w", err)
		}
		return filepath.Join(dir, base), nil
	}
	// An explicit path ending in .tar names the file; anything else is a
	// directory to write into.
	if strings.HasSuffix(in.Out, ".tar") {
		if err := os.MkdirAll(filepath.Dir(in.Out), 0o700); err != nil {
			return "", err
		}
		return in.Out, nil
	}
	if err := os.MkdirAll(in.Out, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(in.Out, base), nil
}

// archiveFileName names an archive. Scheduled ones carry ".auto" so a human
// listing the directory can tell them apart; the header, not the name, is what
// pruning trusts.
func archiveFileName(benchName, trigger string, now time.Time) string {
	stamp := now.UTC().Format("20060102T150405Z")
	if trigger == TriggerScheduled {
		return fmt.Sprintf("%s_%s.auto.ffm.tar", benchName, stamp)
	}
	return fmt.Sprintf("%s_%s.ffm.tar", benchName, stamp)
}

// checkBackupSpace refuses to start writing an archive that cannot fit.
func (s *Service) checkBackupSpace(in BackupInput, sizes map[string]int64, pw ProgressWriter) error {
	if in.SkipSpaceCheck {
		return nil
	}
	var total int64
	for _, n := range sizes {
		total += n
	}
	dir := in.Out
	if dir == "" {
		dir = config.BackupsDir()
	}
	if strings.HasSuffix(dir, ".tar") {
		dir = filepath.Dir(dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	free, ok := freeBytes(dir)
	if !ok {
		return nil
	}
	// The members are already compressed, so the archive is close to their sum;
	// the margin covers tar padding and the metadata members.
	need := uint64(total) + 64<<20
	if free < need {
		return fmt.Errorf("not enough space in %s: the archive needs about %s and %s is free "+
			"(use --skip-space-check to try anyway)", dir, humanBytes(int64(need)), humanBytes(int64(free)))
	}
	return nil
}

// filesSuffix renders the backup step message's tail.
func filesSuffix(withFiles bool) string {
	if withFiles {
		return " --with-files"
	}
	return " (database only)"
}

// humanBytes renders a byte count for human output.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
