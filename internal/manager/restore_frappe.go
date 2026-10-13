package manager

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/version"
)

// frappeBackupKind marks the manifest header ffm builds for an import, so the
// plan and the summary can say where the data came from.
const frappeBackupKind = "frappe-backup"

// frappeStagingDir holds the copies of the user's files, named the way
// `bench restore` is handed them.
const frappeStagingDir = "frappe-backup"

// maxSiteConfigBytes bounds a site_config.json read from the backup.
const maxSiteConfigBytes = 1 << 20

// frappeFiles is the validated content of a FrappeBackupInput.
type frappeFiles struct {
	dbPath      string
	dbGzip      bool
	publicPath  string
	publicExt   string // ".tar" or ".tgz"
	privatePath string
	privateExt  string
	total       int64
	modTime     time.Time

	info dumpInfo

	encryptionKey       string
	backupEncryptionKey string
	ignoredConfigKeys   int
}

// restoreFrappeBackup builds a new bench from the files `bench backup` (or
// Frappe Cloud) writes.
//
// It runs the same core as an archive restore, so the gates, Create, the data
// load and the reconcile steps are the proven ones. What is different is the
// input: there is no manifest, so one is built from the dump itself.
func (s *Service) restoreFrappeBackup(in RestoreInput, pw ProgressWriter) error {
	fb := in.Frappe
	target := in.TargetName
	if target == "" {
		return errors.New("give the new bench a name: 'ffm restore --frappe-backup <db.sql.gz> <name>' " +
			"(Frappe's backup files do not say what to call it)")
	}
	if err := bench.ValidateNewName(target); err != nil {
		return fmt.Errorf("invalid bench name %q: %w", target, err)
	}
	if in.Archive != "" {
		return errors.New("a Frappe backup import takes the database file from --frappe-backup, not an archive argument")
	}
	mode := fb.Mode
	if mode == "" {
		mode = "dev"
	}
	if mode != "dev" && mode != "prod" {
		return fmt.Errorf("invalid --mode %q: must be 'dev' or 'prod'", mode)
	}
	overrides, err := parseAppOverrides(in.AppOverrides)
	if err != nil {
		return err
	}

	pw.Step("Checking the Frappe backup files")
	files, err := inspectFrappeFiles(fb, pw)
	if err != nil {
		return err
	}

	// Problems that need no Docker are all reported together, before anything
	// is created.
	var problems []Problem
	engine, perr := chooseDBEngine(files.info.Engine, fb.DBType)
	if perr != nil {
		problems = append(problems, Problem{Message: perr.Error()})
	}
	branch, bproblems := chooseFrappeBranch(files.info.FrappeVersion(), files.info.FrappeBranch(), fb.FrappeBranch,
		overrides["frappe"] != "")
	problems = append(problems, bproblems...)
	specs, installed, aproblems := planDumpApps(files.info, fb.Apps, overrides)
	problems = append(problems, aproblems...)

	adminPassword := in.AdminPassword
	if adminPassword == "" {
		if adminPassword, err = randomPassword(); err != nil {
			return err
		}
	}
	dbPassword, err := randomPassword()
	if err != nil {
		return err
	}

	m := Manifest{
		Header: Header{
			Kind:          frappeBackupKind,
			SchemaVersion: SchemaVersion,
			FfmVersion:    version.Version,
			CreatedAt:     files.modTime.UTC(),
			BenchName:     target,
			Mode:          mode,
			DBType:        engine,
			FrappeVersion: files.info.FrappeVersion(),
			Tiers:         frappeTiers(files),
		},
		Bench: state.Bench{
			Name:          target,
			Mode:          mode,
			DBType:        engine,
			FrappeBranch:  branch,
			Apps:          specs,
			DBPassword:    dbPassword,
			AdminPassword: adminPassword,
		},
		Site: SiteInfo{InstalledApps: installed},
		Secrets: Secrets{
			AdminPassword:       adminPassword,
			DBRootPassword:      dbPassword,
			EncryptionKey:       files.encryptionKey,
			BackupEncryptionKey: files.backupEncryptionKey,
		},
		Members: []archive.Member{{Path: frappeStagingDir + "/" + files.dbName(), Encoding: archive.EncodingGzip}},
	}
	if !files.dbGzip {
		m.Members[0].Encoding = archive.EncodingNone
	}
	m.Header.SiteName = restoredSiteName(m, target, in.Domain)
	m.Site.SiteName = m.Header.SiteName
	addSecrets(pw, manifestSecrets(m)...)

	members := restoreMembers{DB: frappeStagingDir + "/" + files.dbName()}
	if files.publicPath != "" {
		members.Public = frappeStagingDir + "/public-files" + files.publicExt
	}
	if files.privatePath != "" {
		members.Private = frappeStagingDir + "/private-files" + files.privateExt
	}

	if len(problems) > 0 {
		// The rest of the gates (name, mode, domain, encryption key, app
		// sources) are in restoreCore; report these first so a missing --apps is
		// not buried.
		return problemsError(problems)
	}

	release, err := s.lockBench(target)
	if err != nil {
		return err
	}
	defer release()

	staging := filepath.Join(config.BenchesDir(), fmt.Sprintf("_restore-%s-%d", target, time.Now().UnixNano()))
	defer os.RemoveAll(staging)
	if err := checkRestoreSpace(in, files.total); err != nil {
		return err
	}
	if !in.DryRun {
		pw.Step("Staging the backup files")
		if err := stageFrappeFiles(staging, files, members); err != nil {
			return err
		}
	}

	return s.restoreCore(in, pw, restoreRun{
		m: m, staging: staging, totalBytes: files.total, label: filepath.Base(files.dbPath),
		members: members, frappe: true,
	}, target, overrides)
}

func frappeTiers(f frappeFiles) []string {
	t := []string{TierCore}
	if f.publicPath != "" || f.privatePath != "" {
		t = append(t, TierFiles)
	}
	return t
}

// dbName is the name the dump is given in the container.
func (f frappeFiles) dbName() string {
	if f.dbGzip {
		return "database.sql.gz"
	}
	return "database.sql"
}

// stageFrappeFiles copies the user's files into staging under the names
// `bench restore` expects. Copies, not links: the copy into the container
// changes the file's mode, and that must never touch the user's original.
func stageFrappeFiles(staging string, f frappeFiles, members restoreMembers) error {
	pairs := [][2]string{{f.dbPath, members.DB}}
	if members.Public != "" {
		pairs = append(pairs, [2]string{f.publicPath, members.Public})
	}
	if members.Private != "" {
		pairs = append(pairs, [2]string{f.privatePath, members.Private})
	}
	for _, p := range pairs {
		dst := filepath.Join(staging, filepath.FromSlash(p[1]))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := copyFile(p[0], dst); err != nil {
			return fmt.Errorf("stage %s: %w", filepath.Base(p[0]), err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// inspectFrappeFiles validates every input file without Docker and reads what
// the dump says about the site.
func inspectFrappeFiles(fb *FrappeBackupInput, pw ProgressWriter) (frappeFiles, error) {
	var f frappeFiles
	if fb.DB == "" {
		return f, errors.New("--frappe-backup needs the database dump (…-database.sql.gz)")
	}
	st, err := regularFile(fb.DB)
	if err != nil {
		return f, fmt.Errorf("database dump: %w", err)
	}
	lower := strings.ToLower(fb.DB)
	if !strings.HasSuffix(lower, ".sql.gz") && !strings.HasSuffix(lower, ".sql") {
		return f, fmt.Errorf("database dump %s: expected a .sql.gz or .sql file", filepath.Base(fb.DB))
	}
	f.dbPath, f.total, f.modTime = fb.DB, st.Size(), st.ModTime()

	pw.Step("Reading the database dump (this reads all of it once)")
	r, closeFn, err := openDump(fb.DB)
	if err != nil {
		return f, err
	}
	defer closeFn()
	f.dbGzip = dumpIsGzip(fb.DB)
	f.info, err = inspectDump(r)
	if err != nil {
		return f, err
	}
	if !f.info.HasUser || !f.info.HasAuth {
		return f, errors.New("this does not look like a Frappe site database: the dump has no tabUser / __Auth " +
			"table, so the restored site would have nobody to log in as. Is it a partial backup?")
	}
	if !f.info.HasApps || len(f.info.Apps) == 0 {
		return f, errors.New("the dump lists no installed apps (its 'tabInstalled Application' table is missing or " +
			"empty): it is not a complete Frappe site backup")
	}

	for _, spec := range []struct {
		flag, p string
		pub     bool
	}{{"--files", fb.Files, true}, {"--private-files", fb.PrivateFiles, false}} {
		if spec.p == "" {
			continue
		}
		st, err := regularFile(spec.p)
		if err != nil {
			return f, fmt.Errorf("%s: %w", spec.flag, err)
		}
		ext, err := tarExt(spec.p)
		if err != nil {
			return f, fmt.Errorf("%s: %w", spec.flag, err)
		}
		pw.Step("Checking " + filepath.Base(spec.p))
		if err := checkFilesTar(spec.p, ext); err != nil {
			return f, fmt.Errorf("%s %s: %w", spec.flag, filepath.Base(spec.p), err)
		}
		f.total += st.Size()
		if spec.pub {
			f.publicPath, f.publicExt = spec.p, ext
		} else {
			f.privatePath, f.privateExt = spec.p, ext
		}
	}

	if fb.Config != "" {
		if err := readFrappeConfig(fb.Config, &f); err != nil {
			return f, fmt.Errorf("--config: %w", err)
		}
	}
	return f, nil
}

func dumpIsGzip(p string) bool {
	fh, err := os.Open(p)
	if err != nil {
		return false
	}
	defer fh.Close()
	b := make([]byte, 2)
	_, _ = io.ReadFull(fh, b)
	return b[0] == 0x1f && b[1] == 0x8b
}

func regularFile(p string) (os.FileInfo, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", p)
	}
	if st.Size() == 0 {
		return nil, fmt.Errorf("%s is empty", p)
	}
	return st, nil
}

// tarExt returns the extension `bench restore` picks its extraction by: it
// treats ".tar" and ".tgz" differently, so a ".tar.gz" is handed over as ".tgz".
func tarExt(p string) (string, error) {
	l := strings.ToLower(p)
	switch {
	case strings.HasSuffix(l, ".tar"):
		return ".tar", nil
	case strings.HasSuffix(l, ".tgz"), strings.HasSuffix(l, ".tar.gz"):
		return ".tgz", nil
	}
	return "", fmt.Errorf("%s: expected a .tar, .tgz or .tar.gz file", filepath.Base(p))
}

// checkFilesTar reads a file tarball to its end: it must be a tar of the kind
// the extension says, complete, and free of entries that escape the directory
// they are extracted into.
func checkFilesTar(p, ext string) error {
	fh, err := os.Open(p)
	if err != nil {
		return err
	}
	defer fh.Close()
	var r io.Reader = bufio.NewReaderSize(fh, 1<<20)
	if ext == ".tgz" {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return fmt.Errorf("not a gzip file: %w", err)
		}
		r = zr
	}
	tr := tar.NewReader(r)
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if n == 0 {
				return fmt.Errorf("not a tar file: %w", err)
			}
			return fmt.Errorf("the tar is truncated or corrupt after %d entries: %w", n, err)
		}
		n++
		if bad := unsafeTarEntry(h); bad != "" {
			return fmt.Errorf("refusing: %s", bad)
		}
	}
	if n == 0 {
		return errors.New("the tar has no entries")
	}
	return nil
}

func unsafeTarEntry(h *tar.Header) string {
	escapes := func(name string) bool {
		if strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
			return true
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".." {
				return true
			}
		}
		return false
	}
	if escapes(h.Name) {
		return fmt.Sprintf("entry %q would be extracted outside the site's directory", h.Name)
	}
	switch h.Typeflag {
	case tar.TypeSymlink, tar.TypeLink:
		if escapes(h.Linkname) {
			return fmt.Sprintf("link %q points outside the site's directory (%q)", h.Name, h.Linkname)
		}
	case tar.TypeChar, tar.TypeBlock, tar.TypeFifo:
		return fmt.Sprintf("entry %q is a device or pipe", h.Name)
	}
	return ""
}

// readFrappeConfig takes ONLY the encryption keys from the backup's
// site_config.json. db_name, db_password, host_name and the rest describe the
// old site's database server; copying them would aim the new site at it.
func readFrappeConfig(p string, f *frappeFiles) error {
	st, err := regularFile(p)
	if err != nil {
		return err
	}
	if st.Size() > maxSiteConfigBytes {
		return fmt.Errorf("%s is %s: too big for a site_config.json", filepath.Base(p), humanBytes(st.Size()))
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return fmt.Errorf("%s is not a JSON object: %w", filepath.Base(p), err)
	}
	if v, ok := cfg["encryption_key"]; ok {
		key, _ := v.(string)
		if err := validFernetKey(key); err != nil {
			return fmt.Errorf("encryption_key: %w", err)
		}
		f.encryptionKey = key
	}
	if v, ok := cfg["backup_encryption_key"]; ok {
		key, _ := v.(string)
		if key == "" || strings.ContainsAny(key, "\x00\n\r") {
			return errors.New("backup_encryption_key is not a usable string")
		}
		f.backupEncryptionKey = key
	}
	for k := range cfg {
		if k != "encryption_key" && k != "backup_encryption_key" {
			f.ignoredConfigKeys++
		}
	}
	return nil
}

// validFernetKey checks the shape of a Frappe encryption_key: 32 bytes of
// URL-safe base64. Anything else would be written into the site and fail every
// Password field later.
func validFernetKey(k string) error {
	if k == "" {
		return errors.New("is empty or not a string")
	}
	b, err := base64.URLEncoding.DecodeString(k)
	if err != nil || len(b) != 32 {
		return errors.New("is not a Fernet key (expected 32 bytes of URL-safe base64, 44 characters)")
	}
	return nil
}

// chooseDBEngine settles MariaDB or Postgres: the dump decides, and a --db-type
// that disagrees is refused rather than loaded into the wrong server.
func chooseDBEngine(dump, requested string) (string, error) {
	switch requested {
	case "", "mariadb", "postgres":
	default:
		return "", fmt.Errorf("invalid --db-type %q: must be 'mariadb' or 'postgres'", requested)
	}
	switch {
	case dump != "" && requested != "" && dump != requested:
		return "", fmt.Errorf("the dump is a %s dump but --db-type %s was chosen: a %s dump cannot be loaded into %s. "+
			"Drop --db-type or use --db-type %s", dump, requested, dump, requested, dump)
	case dump != "":
		return dump, nil
	case requested != "":
		return requested, nil
	}
	return "", errors.New("cannot tell from the dump whether it is MariaDB or PostgreSQL: pass --db-type")
}

var frappeMajorRe = regexp.MustCompile(`^(\d+)\.`)
var versionBranchRe = regexp.MustCompile(`^version-(\d+)$`)

// chooseFrappeBranch picks the Frappe branch the new bench is built on. A
// dump restored onto another major version is migrated by Frappe on the way in
// and often fails, so the version recorded in the dump decides the default, and
// a branch of another major is refused.
func chooseFrappeBranch(dumpVersion, dumpBranch, requested string, repoOverridden bool) (string, []Problem) {
	major := ""
	if m := frappeMajorRe.FindStringSubmatch(dumpVersion); m != nil {
		major = m[1]
	}
	if requested == "" {
		switch {
		case versionBranchRe.MatchString(dumpBranch):
			return dumpBranch, nil
		case major != "":
			return "version-" + major, nil
		}
		return bench.DefaultFrappeBranch, nil
	}
	if bm := versionBranchRe.FindStringSubmatch(requested); bm != nil && major != "" && bm[1] != major && !repoOverridden {
		return requested, []Problem{{Message: fmt.Sprintf(
			"the dump comes from Frappe %s but --frappe-branch %s is a different major version: Frappe would have to "+
				"migrate across versions while restoring. Use --frappe-branch version-%s", dumpVersion, requested, major)}}
	}
	return requested, nil
}

// planDumpApps reconciles the apps the dump lists with the ones the caller
// named. An app the dump lists that nobody said where to get is refused: bench
// would fail to find it minutes into the build, or restore a site whose
// DocTypes have no code.
func planDumpApps(info dumpInfo, apps []string, overrides map[string]string) (specs, installed []string, problems []Problem) {
	named := map[string]bool{}
	for _, raw := range apps {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		specs = append(specs, raw)
		named[bench.ParseAppSpec(raw, "").DisplayName()] = true
	}
	for name := range overrides {
		named[name] = true
	}

	seen := map[string]bool{"frappe": true}
	installed = []string{"frappe"}
	var missing []string
	for _, a := range info.Apps {
		if !appNameRe.MatchString(a.Name) {
			problems = append(problems, Problem{Message: fmt.Sprintf(
				"the dump lists an app named %q, which is not a Frappe app name: the file is corrupt or hostile", a.Name)})
			continue
		}
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		installed = append(installed, a.Name)
		if !named[a.Name] {
			missing = append(missing, a.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		short := make([]string, len(missing))
		copy(short, missing)
		problems = append(problems, Problem{Message: fmt.Sprintf(
			"the dump says the site had these apps besides frappe, and no source was given for them: %s. "+
				"Frappe's backup does not carry app code, so ffm has to fetch it. Add --apps %s (apps under "+
				"github.com/frappe or /erpnext), or --app <name>=<git-url>[@branch] for each other app",
			strings.Join(missing, ", "), strings.Join(short, ","))})
	}
	return specs, installed, problems
}

// printFrappeRestorePlan is --dry-run's output for a Frappe backup.
func printFrappeRestorePlan(pw ProgressWriter, m Manifest, plan restorePlan, target string, in RestoreInput,
	webPort, socketIOPort int, run restoreRun) {

	pw.Printf("\nThe Frappe backup files are valid and this import would succeed.\n\n")
	pw.Printf("  Database dump: %s\n", run.label)
	pw.Printf("  New bench:     %s  (site %s)\n", target, m.Header.SiteName)
	pw.Printf("  Mode:          %s, %s, frappe %s\n", m.Header.Mode, m.Header.DBType, orUnknown(m.Header.FrappeVersion))
	if webPort > 0 {
		pw.Printf("  Ports:         %d / %d\n", webPort, socketIOPort)
	} else {
		pw.Printf("  Ports:         allocated at restore time\n")
	}
	pw.Printf("  Apps:          %s\n", strings.Join(m.Site.InstalledApps, ", "))
	pw.Printf("  App sources:\n")
	for _, line := range describePlan(plan) {
		pw.Printf("    %s\n", line)
	}
	pw.Printf("  Public files:  %s\n", yesNo(run.members.Public != ""))
	pw.Printf("  Private files: %s\n", yesNo(run.members.Private != ""))
	pw.Printf("  Encryption key present: %s\n", yesNo(m.Secrets.EncryptionKey != ""))
	pw.Printf("\nRun the same command without --dry-run to import.\n")
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

// printFrappeRestoreNotes says what an import from Frappe's files cannot carry.
func printFrappeRestoreNotes(pw ProgressWriter, b state.Bench) {
	pw.Printf("\nImported from Frappe's own backup files, not an ffm archive:\n" +
		"  - The Administrator password was set to the one above (the dump's own was replaced);\n" +
		"    choose it next time with --admin-password.\n" +
		"  - Absolute URLs stored inside the database still point at the original site's name —\n" +
		"    check Website Settings, Email Accounts, webhooks and print formats.\n")
	if b.IsProd() {
		pw.Printf("  - Point DNS for %s at this host before the first request.\n", b.SiteName)
	}
}
