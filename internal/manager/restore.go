package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// restoreStaging is where the archive is unpacked inside the container.
const restoreStaging = "/tmp/ffm-restore"

// benchOverhead is the disk a freshly provisioned bench needs on top of the
// archive: the app sources, the Python virtualenv and node_modules. Measured at
// roughly 1.7 GiB on a frappe+erpnext dev bench; rounded up.
const benchOverhead = 4 << 30

// siteConfigOwnedKeys are the site_config.json keys ffm or the target database
// owns. They are dropped from the archived config during the merge: db_name and
// db_password belong to the site Create just made, and the rest are re-applied
// afterwards from the restore's own parameters.
var siteConfigOwnedKeys = map[string]bool{
	"db_name":        true,
	"db_password":    true,
	"db_user":        true,
	"db_type":        true,
	"host_name":      true,
	"installed_apps": true,
}

// Restore rebuilds a bench from an archive, as a NEW bench.
//
// It never writes into an existing one. That is what lets the whole operation
// borrow Create's rollback: if anything fails, the half-built bench is torn down
// and the host is left as it was found — which would be impossible if a live
// site had already been half-overwritten.
func (s *Service) Restore(in RestoreInput, pw ProgressWriter) (restoreErr error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	archivePath, err := filepath.Abs(in.Archive)
	if err != nil {
		return err
	}
	if _, err := os.Stat(archivePath); err != nil {
		return fmt.Errorf("archive %s: %w", in.Archive, err)
	}

	// Phase A: preflight. Nothing on the host is mutated before this passes.
	pw.Step("Reading the archive header")
	headerJSON, err := archive.PeekHeader(archivePath)
	if err != nil {
		return err
	}
	header, err := ParseHeader(headerJSON)
	if err != nil {
		return err
	}
	if header.NewerSchema() {
		fmt.Fprintf(pw.Stderr(),
			"warning: this archive was written by a newer ffm (archive schema %d, this build knows %d).\n"+
				"         Anything it records that this version does not understand is ignored.\n",
			header.SchemaVersion, SchemaVersion)
	}

	target := in.TargetName
	if target == "" {
		target = header.BenchName
	}
	// Validated here, not in checkArchive, because the name is about to be
	// interpolated into a filesystem path — and when it was not given on the
	// command line it came out of the archive, which is untrusted input. A name
	// of "../../.ssh" would otherwise have the staging directory created
	// outside the benches root before any gate had run.
	if err := bench.ValidateName(target); err != nil {
		return fmt.Errorf("invalid bench name %q: %w", target, err)
	}

	// Held for the whole restore, rollback included: the Delete in the failure
	// path below re-enters it (lockBench is re-entrant within a Service).
	release, err := s.lockBench(target)
	if err != nil {
		return err
	}
	defer release()

	staging := filepath.Join(config.BenchesDir(), fmt.Sprintf("_restore-%s-%d", target, time.Now().UnixNano()))
	defer os.RemoveAll(staging)

	// A first, pessimistic space check from the archive's own size, before a
	// byte is unpacked. The precise check below needs the manifest, which is at
	// the END of the archive — so without this one, running out of disk is
	// discovered by running out of disk.
	if info, err := os.Stat(archivePath); err == nil {
		if err := checkRestoreSpace(in, info.Size()); err != nil {
			return err
		}
	}

	pw.Step("Unpacking and verifying the archive")
	res, err := archive.Extract(archivePath, staging, archive.Limits{MaxBytes: in.MaxExtractBytes})
	if err != nil {
		return err
	}
	manifestRaw, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(archive.ManifestName)))
	if err != nil {
		return fmt.Errorf("read archive manifest: %w", err)
	}
	m, err := ParseManifest(manifestRaw)
	if err != nil {
		return err
	}
	if err := verifyMembers(m, res); err != nil {
		return err
	}

	withFiles := in.WithFiles && m.Header.HasTier(TierFiles)
	if in.WithFiles && !withFiles {
		fmt.Fprintln(pw.Stderr(),
			"note: this archive was taken with --no-files, so the restored site has no attachments")
	}
	problems := checkArchive(m, target, in)
	problems = append(problems, s.nameCollisions(target)...)
	problems = filterOverridden(problems, in)
	if len(problems) > 0 {
		return problemsError(problems)
	}

	webPort, socketIOPort, err := s.restorePorts(m, in)
	if err != nil {
		return err
	}
	if err := checkRestoreSpace(in, res.TotalBytes); err != nil {
		return err
	}

	if in.DryRun {
		printRestorePlan(pw, m, target, webPort, socketIOPort, withFiles, archivePath)
		return nil
	}

	// Phase B: provision an empty bench shaped like the archived one.
	createIn, err := restoreCreateInput(m, target, in, webPort, socketIOPort)
	if err != nil {
		return err
	}
	pw.Printf("Restoring %q from %s into a new bench %q...\n\n",
		m.Header.SiteName, filepath.Base(archivePath), target)
	if err := s.Create(createIn, pw); err != nil {
		return fmt.Errorf("provision the bench to restore into: %w", err)
	}

	b, err := s.GetBench(target)
	if err != nil {
		return err
	}
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)

	// From here on, a failure must undo the bench Create just made — otherwise a
	// failed restore leaves a tracked but half-populated bench behind.
	defer func() {
		if restoreErr == nil {
			return
		}
		if in.KeepOnFailure || os.Getenv("FFM_KEEP_ON_FAILURE") != "" {
			fmt.Fprintf(pw.Stderr(),
				"\nRestore failed — leaving bench %q in place (--keep-on-failure).\n"+
					"Remove it with:\n  ffm delete %s --force\n", target, target)
			return
		}
		fmt.Fprintln(pw.Stderr(), "\nRestore failed — removing the half-restored bench...")
		if err := s.Delete(target, DiscardProgress{}); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: could not remove bench %q: %v\n", target, err)
		}
	}()

	if in.PinApps {
		if err := s.pinApps(runner, m, pw); err != nil {
			return err
		}
	}

	// Phase C. The order below is load-bearing; see the comments on each step.
	if err := s.applySiteConfig(b, m, in, pw); err != nil {
		return err
	}
	if err := s.runBenchRestore(runner, b, m, in, staging, withFiles, pw); err != nil {
		return err
	}
	if err := s.reconcileAfterRestore(runner, b, m, in, pw); err != nil {
		return err
	}

	printRestoreSummary(pw, b, m, target, withFiles)
	return nil
}

// nameCollisions checks every place a leftover bench can hide.
func (s *Service) nameCollisions(target string) []Problem {
	tracked, _ := s.BenchExists(target)
	_, dirErr := os.Stat(config.BenchDir(target))
	project := bench.ProjectName(target)
	return checkNameFree(target,
		dirErr == nil,
		dockerHasVolumes(project),
		dockerHasContainers(project),
		tracked)
}

// filterOverridden drops the problems the caller explicitly accepted.
func filterOverridden(problems []Problem, in RestoreInput) []Problem {
	kept := problems[:0]
	for _, p := range problems {
		switch {
		case p.Override == "--allow-missing-encryption-key" && in.AllowMissingEncryptionKey:
			continue
		case strings.HasPrefix(p.Override, "--encryption-key") && in.EncryptionKey != "":
			continue
		}
		kept = append(kept, p)
	}
	return kept
}

// restorePorts decides which host ports the restored bench publishes.
//
// Reusing the archive's pair keeps URLs stable, which is the point of a restore
// onto a fresh machine — but the original bench is often still running on this
// one, so the whole published range is probed, not just the two base ports.
func (s *Service) restorePorts(m Manifest, in RestoreInput) (int, int, error) {
	if in.WebPort > 0 || in.SocketIOPort > 0 {
		web, sio := in.WebPort, in.SocketIOPort
		if sio == 0 {
			sio = web + 1000
		}
		if !bench.ValidBenchPortPair(web, sio) {
			return 0, 0, fmt.Errorf("invalid port pair web=%d socketio=%d: ffm pairs them 1000 apart", web, sio)
		}
		if err := bench.CheckBenchPortRangeFree(web, sio); err != nil {
			return 0, 0, fmt.Errorf("requested ports are not free: %w", err)
		}
		return web, sio, nil
	}
	if in.ReallocatePorts {
		return 0, 0, nil // Create allocates a fresh pair
	}
	web, sio := m.Bench.WebPort, m.Bench.SocketIOPort
	if !bench.ValidBenchPortPair(web, sio) {
		return 0, 0, nil
	}
	if err := bench.CheckBenchPortRangeFree(web, sio); err != nil {
		// Almost always the bench this archive came from, still running.
		return 0, 0, nil
	}
	// A live probe is not enough. The bench this archive came from may be
	// STOPPED, in which case its ports probe free while still being reserved in
	// the state store — reusing them would let `ffm start` on the original fail
	// with "port is already allocated", i.e. a restore breaking an untouched
	// bench. AllocatePorts treats the store as reservations for this reason.
	s.lock()
	claimed, err := benchPortsClaimed(s.Store, web, sio)
	s.unlock()
	if err != nil || claimed {
		return 0, 0, nil
	}
	return web, sio, nil
}

// benchPortsClaimed reports whether any tracked bench already owns part of the
// port range a pair would publish.
func benchPortsClaimed(store *state.Store, web, sio int) (bool, error) {
	benches, err := store.Load()
	if err != nil {
		return false, err
	}
	want := make(map[int]bool, bench.PublishedPortSpan*2)
	for _, p := range bench.BenchPortRange(web, sio) {
		want[p] = true
	}
	for _, b := range benches {
		for _, p := range bench.BenchPortRange(b.WebPort, b.SocketIOPort) {
			if want[p] {
				return true, nil
			}
		}
	}
	return false, nil
}

// restoreCreateInput rebuilds the CreateInput that reproduces the archived bench.
func restoreCreateInput(m Manifest, target string, in RestoreInput, webPort, socketIOPort int) (CreateInput, error) {
	mode := m.Header.Mode
	if mode == "" {
		mode = "dev"
	}

	adminPassword := in.AdminPassword
	if adminPassword == "" {
		adminPassword = m.Secrets.AdminPassword
	}
	if adminPassword == "" {
		return CreateInput{}, fmt.Errorf("the archive records no Administrator password — " +
			"pass --admin-password to set one")
	}

	domain := in.Domain
	if domain == "" {
		domain = m.Bench.Domain
	}
	noSSL := in.NoSSL || (mode == "prod" && m.Bench.TLSMode == state.TLSNone)

	// The host uid is a property of THIS machine, never of the archive: an
	// archive taken on a uid-1000 host restores onto a uid-1001 host, where the
	// workspace bind mount is unwritable without the remap.
	matchHostUser, err := needsHostUserRemap()
	if err != nil {
		return CreateInput{}, err
	}

	// Aliases come back only for a true restore. Under a new name the source
	// bench may still be running and claiming those hostnames, and two benches
	// must never both answer on one name.
	var aliases []string
	aliasTLS := false
	if target == m.Header.BenchName {
		aliases = m.Bench.DomainAliases
		aliasTLS = m.Bench.AliasTLS
	}

	return CreateInput{
		Name:              target,
		FrappeBranch:      m.Bench.FrappeBranch,
		FrappeRepo:        m.Bench.FrappeRepo,
		Apps:              appsForRestore(m),
		AdminPassword:     adminPassword,
		DBPassword:        m.Bench.DBPassword,
		DBType:            m.Bench.DBEngine(),
		GithubToken:       in.GithubToken,
		Mode:              mode,
		Domain:            domain,
		NoSSL:             noSSL,
		AcmeEmail:         in.AcmeEmail,
		MariaDBBufferPool: m.Bench.MariaDBBufferPool,
		GunicornWorkers:   m.Bench.GunicornWorkers,
		WorkerLongCount:   m.Bench.WorkerLongCount,
		WorkerShortCount:  m.Bench.WorkerShortCount,
		RedisCacheMaxmem:  m.Bench.RedisCacheMaxmem,
		RedisQueueMaxmem:  m.Bench.RedisQueueMaxmem,
		SlowQueryLog:      m.Bench.SlowQueryLog,
		FixedWebPort:      webPort,
		FixedSocketIOPort: socketIOPort,
		MatchHostUser:     matchHostUser,
		DomainAliases:     aliases,
		AliasTLS:          aliasTLS,
		KeepOnFailure:     in.KeepOnFailure,
		SkipAppInstall:    true,
		SkipAssetBuild:    true,
	}, nil
}

// applySiteConfig merges the archived site configuration into the new site.
//
// This runs BEFORE anything reads the site, because of encryption_key: Frappe
// generates that key lazily, so a single failed decrypt mints a fresh one and
// persists it — permanently orphaning every Password field in the data about to
// be restored. Writing it host-side rather than through `bench set-config`
// keeps Frappe out of the loop entirely at the one moment that matters.
//
// It also carries across the keys nobody else would: maintenance_mode,
// mail_server, max_file_size, rate limits, per-app integration settings. Only
// the handful of keys the new site owns are dropped.
func (s *Service) applySiteConfig(b state.Bench, m Manifest, in RestoreInput, pw ProgressWriter) error {
	pw.Step("Applying the archived site configuration")
	path := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", b.SiteName, "site_config.json")
	current, err := readJSONFile(path)
	if err != nil {
		return fmt.Errorf("read the new site's site_config.json: %w", err)
	}

	for k, v := range m.Site.SiteConfig {
		if siteConfigOwnedKeys[k] {
			continue
		}
		current[k] = v
	}
	if key := m.Secrets.EncryptionKey; key != "" {
		current["encryption_key"] = key
	} else if in.AllowMissingEncryptionKey {
		fmt.Fprintln(pw.Stderr(),
			"warning: no encryption key in the archive — Password fields in the restored site "+
				"(email passwords, integration secrets) will not decrypt")
	}

	return writeSiteConfig(path, current)
}

// writeSiteConfig rewrites a site_config.json and enforces its mode.
//
// The explicit Chmod is not redundant: os.WriteFile applies its mode only when
// it CREATES the file, and this one always exists already — so without it the
// site's Fernet encryption key and database password keep whatever mode Frappe
// happened to give them.
func writeSiteConfig(path string, cfg map[string]any) error {
	raw, err := json.MarshalIndent(cfg, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// runBenchRestore hands the dump and file tarballs to Frappe.
func (s *Service) runBenchRestore(runner *bench.Runner, b state.Bench, m Manifest, in RestoreInput,
	staging string, withFiles bool, pw ProgressWriter) error {

	remote := fmt.Sprintf("%s-%d", restoreStaging, time.Now().UnixNano())
	if out, err := runner.ExecSilent("frappe", "mkdir", "-p", remote); err != nil {
		return fmt.Errorf("prepare the restore directory in the container: %w\n%s", err, out)
	}
	defer func() {
		if _, err := runner.ExecSilent("frappe", "rm", "-rf", remote); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not clean up %s: %v\n", remote, err)
		}
	}()

	copyIn := func(memberPath string) (string, error) {
		local := filepath.Join(staging, filepath.FromSlash(memberPath))
		// Extraction wrote 0600 for the host user; `docker cp` lands the file as
		// root inside the container, so it has to be group/other readable for
		// the frappe user to read it back.
		if err := os.Chmod(local, 0o644); err != nil {
			return "", err
		}
		dest := remote + "/" + filepath.Base(memberPath)
		if err := runner.CopyTo("frappe", local, dest); err != nil {
			return "", fmt.Errorf("copy %s into the container: %w", filepath.Base(memberPath), err)
		}
		return dest, nil
	}

	pw.Step("Copying the archive into the container")
	dbRemote, err := copyIn(archive.Prefix + "/db/database.sql.gz")
	if err != nil {
		return err
	}
	var publicRemote, privateRemote string
	if withFiles {
		if publicRemote, err = copyIn(archive.Prefix + "/files/public-files.tgz"); err != nil {
			return err
		}
		if privateRemote, err = copyIn(archive.Prefix + "/files/private-files.tgz"); err != nil {
			return err
		}
	}

	rootUser := "root"
	if b.IsPostgres() {
		rootUser = "postgres"
	}
	// --force because this runs through `exec -T` with no tty to answer
	// Frappe's own prompt, and because every gate it would ask about has
	// already been checked in preflight against better information.
	//
	// Not passed, deliberately:
	//   --db-name          accepted by the command and never forwarded to the
	//                      restore, so it silently does nothing.
	//   --admin-password   a no-op here: install_app returns early because the
	//                      restored database already lists frappe as installed,
	//                      so the hook that would apply it never fires. The
	//                      password is set explicitly afterwards instead.
	//   --encryption-key   only when the dump really is encrypted; against a
	//                      plaintext dump it derails the file restore with
	//                      "Invalid path".
	cmd := fmt.Sprintf("cd /workspace/frappe-bench && bench --site %s restore %s"+
		" --db-root-username %s --db-root-password %s --force",
		b.SiteName, dbRemote, rootUser, b.DBPassword)
	if withFiles {
		cmd += fmt.Sprintf(" --with-public-files %s --with-private-files %s", publicRemote, privateRemote)
	}
	if key := restoreEncryptionKey(m, in); key != "" {
		cmd += " --encryption-key " + key
	}

	pw.Step("Restoring the database" + filesSuffix(withFiles))
	if out, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
		return fmt.Errorf("bench restore: %w\n%s", err,
			scrubSecrets(out, benchSecrets(b, m.Secrets.SiteDBPassword, m.Secrets.EncryptionKey)...))
	}
	return nil
}

// restoreEncryptionKey returns the key to decrypt the dump, or empty when the
// dump is not encrypted. Passing a key for a plaintext dump breaks the restore.
func restoreEncryptionKey(m Manifest, in RestoreInput) string {
	if dbMemberEncoding(m) != archive.EncodingGPG {
		return ""
	}
	if in.EncryptionKey != "" {
		return in.EncryptionKey
	}
	return m.Secrets.BackupEncryptionKey
}

// reconcileAfterRestore repairs everything `bench restore` leaves inconsistent.
func (s *Service) reconcileAfterRestore(runner *bench.Runner, b state.Bench, m Manifest,
	in RestoreInput, pw ProgressWriter) error {

	sitePrefix := "cd /workspace/frappe-bench && bench --site " + b.SiteName + " "

	// The Administrator password. `bench restore --admin-password` looks like it
	// does this and does not: install_app returns early because the restored
	// database already lists frappe, so after_install — which is what applies
	// the password — never runs. This is the only thing that actually sets it.
	pw.Step("Setting the Administrator password")
	if out, err := runner.ExecSilent("frappe", "bash", "-c",
		sitePrefix+"set-admin-password "+b.AdminPassword); err != nil {
		return fmt.Errorf("set the Administrator password: %w\n%s", err,
			scrubSecrets(out, benchSecrets(b)...))
	}

	// installed_apps in site_config. Frappe v16 mirrors the list there and the
	// restore does not update it, so it is left claiming only frappe while the
	// database and `bench list-apps` both know better.
	pw.Step("Reconciling the installed app list")
	installed, source := collectInstalledApps(runner, filepath.Join(b.Dir, "workspace", "frappe-bench"),
		b.SiteName, map[string]any{})
	if len(installed) > 0 && source == "list-apps" {
		if err := patchSiteConfig(b, func(cfg map[string]any) {
			cfg["installed_apps"] = installed
		}); err != nil {
			return err
		}
	}

	// Re-apply ffm's own site settings last, so they win over anything the
	// archived configuration or the restored database brought with it — the
	// archive's host_name and socketio_port describe the OLD bench.
	pw.Step("Re-applying this bench's site settings")
	settings := []string{"use " + b.SiteName}
	if b.IsDev() {
		settings = append(settings, "--site "+b.SiteName+" set-config developer_mode 1")
	}
	if b.IsProd() && b.ProxyHost != "" {
		settings = append(settings, "--site "+b.SiteName+" set-config host_name "+b.ProxyHost)
	}
	for _, sub := range settings {
		if out, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && bench "+sub); err != nil {
			return fmt.Errorf("apply site settings: %w\n%s", err, scrubSecrets(out, benchSecrets(b)...))
		}
	}

	if !in.SkipMigrate {
		pw.Step("Running bench migrate — this may take a few minutes")
		if out, err := runner.ExecSilent("frappe", "bash", "-c", sitePrefix+"migrate"); err != nil {
			return fmt.Errorf("bench migrate: %w\n%s", err, scrubSecrets(out, benchSecrets(b)...))
		}
	}

	pw.Step("Building assets — this may take a few minutes")
	if out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && bench build"); err != nil {
		return fmt.Errorf("bench build: %w\n%s", err, out)
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c", sitePrefix+"clear-cache"); err != nil {
		return fmt.Errorf("bench clear-cache: %w\n%s", err, out)
	}

	// Re-apply the realtime patches and verify they took. PatchUtilsJs returns
	// nil when it does not recognise the file, so a silent no-op here is exactly
	// how socket.io would break without anything saying so.
	if err := bench.PatchAuthenticateJs(b.Dir); err != nil {
		return fmt.Errorf("re-apply the realtime auth patch: %w", err)
	}
	if err := bench.PatchUtilsJs(b.Dir); err != nil {
		return fmt.Errorf("re-apply the realtime url patch: %w", err)
	}
	if !bench.RealtimeAcceptsAnyHost(b.Dir) {
		return fmt.Errorf("the restored bench's realtime auth patch did not apply — " +
			"socket.io would reject every connection. The archived Frappe version is probably " +
			"newer than this ffm knows how to patch; run 'ffm update'")
	}

	// Restart so the app server picks up the restored database and app state.
	pw.Step("Restarting the bench")
	if err := runner.RestartService("frappe"); err != nil {
		return fmt.Errorf("restart the frappe container: %w", err)
	}
	if b.IsDev() {
		if err := bench.PatchProcfileWorker(b.Dir); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not patch the Procfile worker: %v\n", err)
		}
		if _, err := runner.ExecSilent("frappe", "bash", "-c",
			"cd /workspace/frappe-bench && nohup bench start > /home/frappe/bench-start.log 2>&1 &"); err != nil {
			return fmt.Errorf("start the dev server: %w", err)
		}
		if err := bench.WaitForHTTP(fmt.Sprintf("http://localhost:%d", b.WebPort), 60*time.Second); err != nil {
			// Not fatal — the data is already restored, and failing here would
			// tear that down over a dev server that has not finished booting.
			fmt.Fprintf(pw.Stderr(), "warning: %v\n", webServerUnreachable(runner, true, err))
		}
		// The API key/secret are reissued rather than restored: Frappe mints a
		// new secret on every generate_keys call, so the archived pair cannot be
		// put back.
		if err := s.SetupFFC(b.Name, DiscardProgress{}); err != nil && s.Verbose {
			fmt.Fprintf(pw.Stderr(), "warning: could not reconfigure ffc: %v\n", err)
		}
	}

	// The state record must agree with the site. Apps in particular: the bench
	// record and the site's installed_apps drift, and after a restore the site
	// is authoritative.
	return s.UpdateBench(b.Name, func(rec *state.Bench) {
		if len(installed) > 0 {
			rec.Apps = appsWithoutFramework(installed, rec.Apps)
		}
		// The archived tunnel cannot be reproduced: its auth token lives in the
		// host's tunnel.json, not in the archive.
		rec.Tunnel = nil
	})
}

// patchSiteConfig applies a mutation to the site's site_config.json in place.
func patchSiteConfig(b state.Bench, fn func(map[string]any)) error {
	path := filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", b.SiteName, "site_config.json")
	cfg, err := readJSONFile(path)
	if err != nil {
		return err
	}
	fn(cfg)
	return writeSiteConfig(path, cfg)
}

// appsWithoutFramework turns a site's installed_apps into ffm's app-spec list,
// preserving any spec that already carries a URL or branch.
func appsWithoutFramework(installed, existing []string) []string {
	bySpec := make(map[string]string, len(existing))
	for _, spec := range existing {
		bySpec[bench.ParseAppSpec(spec, "").DisplayName()] = spec
	}
	var out []string
	for _, app := range installed {
		if app == "frappe" {
			continue
		}
		if spec, ok := bySpec[app]; ok {
			out = append(out, spec)
			continue
		}
		out = append(out, app)
	}
	return out
}

// pinApps checks each app out at the commit the archive recorded.
//
// This must be a fetch plus checkout, not `bench get-app --branch <sha>`: git
// rejects a SHA where it expects a branch name, so the clone fails outright.
func (s *Service) pinApps(runner *bench.Runner, m Manifest, pw ProgressWriter) error {
	pinned := 0
	for _, app := range m.Apps {
		if app.Commit == "" {
			fmt.Fprintf(pw.Stderr(), "warning: no commit recorded for app %q — leaving it at branch HEAD\n", app.Name)
			continue
		}
		dir := "/workspace/frappe-bench/apps/" + app.Name
		// An app present at backup time is not necessarily present now: it is
		// cloned only if it reached CreateInput.Apps. Skip rather than fail —
		// a missing app is reported by the installed_apps gate, and failing
		// here would roll back an otherwise good restore.
		if _, err := runner.ExecSilent("frappe", "test", "-d", dir+"/.git"); err != nil {
			fmt.Fprintf(pw.Stderr(), "warning: app %q is not on this bench — cannot pin it\n", app.Name)
			continue
		}

		pw.Step(fmt.Sprintf("Pinning %s to %s", app.Name, shortCommit(app.Commit)))
		// Two things this must survive:
		//   - the remote is named "upstream", not "origin" — that is what
		//     `bench init` and `bench get-app` clone with;
		//   - frappe's working tree is already dirty, because Create applied the
		//     realtime patches to it, so a plain checkout aborts as soon as the
		//     pinned commit touches those files — precisely the drift --pin-apps
		//     exists to undo. --force discards them; they are re-applied
		//     unconditionally later in the restore.
		cmd := fmt.Sprintf(
			"cd %s && remote=$(git remote | head -1) && git fetch --quiet ${remote:-origin} "+
				"&& git checkout --force --quiet %s", dir, app.Commit)
		if out, err := runner.ExecSilent("frappe", "bash", "-c", cmd); err != nil {
			return fmt.Errorf("pin %s to %s: %w\n%s", app.Name, shortCommit(app.Commit), err, out)
		}
		pinned++
	}
	if pinned == 0 {
		return nil
	}
	if out, err := runner.ExecSilent("frappe", "bash", "-c",
		"cd /workspace/frappe-bench && bench setup requirements"); err != nil {
		return fmt.Errorf("bench setup requirements after pinning: %w\n%s", err, out)
	}
	return nil
}

// needsHostUserRemap reports whether this host's uid differs from the one the
// frappe/bench image ships with, in which case the workspace bind mount is
// unwritable across the boundary without the remap layer.
func needsHostUserRemap() (bool, error) {
	uid, gid, err := hostUserIDs()
	if err != nil {
		return false, err
	}
	return uid != 1000 || gid != 1000, nil
}

// checkRestoreSpace refuses a restore that cannot fit.
func checkRestoreSpace(in RestoreInput, archiveBytes int64) error {
	if in.SkipSpaceCheck {
		return nil
	}
	dir := config.BenchesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	free, ok := freeBytes(dir)
	if !ok {
		return nil
	}
	// The archive is unpacked once, and the bench built around it needs its own
	// app sources, virtualenv and node_modules on top.
	need := uint64(archiveBytes*2) + benchOverhead
	if free < need {
		return fmt.Errorf("not enough space in %s: a restore needs about %s and %s is free "+
			"(use --skip-space-check to try anyway)", dir, humanBytes(int64(need)), humanBytes(int64(free)))
	}
	return nil
}

// dockerHasVolumes reports whether any volume belongs to a compose project.
func dockerHasVolumes(project string) bool {
	out, err := exec.Command("docker", "volume", "ls", "-q",
		"--filter", "label=com.docker.compose.project="+project).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// dockerHasContainers reports whether any container belongs to a compose project.
func dockerHasContainers(project string) bool {
	out, err := exec.Command("docker", "ps", "-aq",
		"--filter", "label=com.docker.compose.project="+project).Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// printRestorePlan renders what --dry-run would have done.
func printRestorePlan(pw ProgressWriter, m Manifest, target string, webPort, socketIOPort int,
	withFiles bool, archivePath string) {

	pw.Printf("\nThe archive is valid and this restore would succeed.\n\n")
	pw.Printf("  Archive:       %s\n", archivePath)
	pw.Printf("  Taken:         %s (ffm %s)\n",
		m.Header.CreatedAt.Local().Format("2006-01-02 15:04"), m.Header.FfmVersion)
	if m.Header.Label != "" {
		pw.Printf("  Label:         %s\n", m.Header.Label)
	}
	pw.Printf("  From bench:    %s  (site %s)\n", m.Header.BenchName, m.Header.SiteName)
	pw.Printf("  New bench:     %s  (site %s)\n", target, restoredSiteName(m, target))
	pw.Printf("  Mode:          %s, %s, frappe %s\n", m.Header.Mode, m.Header.DBType, m.Header.FrappeVersion)
	if webPort > 0 {
		pw.Printf("  Ports:         %d / %d\n", webPort, socketIOPort)
	} else {
		pw.Printf("  Ports:         allocated at restore time\n")
	}
	pw.Printf("  Apps:          %s\n", strings.Join(m.Site.InstalledApps, ", "))
	pw.Printf("  Attachments:   %s\n", yesNo(withFiles))
	pw.Printf("  Encryption key present: %s\n", yesNo(m.Secrets.EncryptionKey != ""))
	pw.Printf("\nRun the same command without --dry-run to restore.\n")
}

// restoredSiteName is the site the restored bench will serve.
func restoredSiteName(m Manifest, target string) string {
	if m.Header.Mode == "prod" {
		return m.Bench.Domain
	}
	return target + ".localhost"
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// printRestoreSummary reports what came back and what could not.
func printRestoreSummary(pw ProgressWriter, b state.Bench, m Manifest, target string, withFiles bool) {
	pw.Printf("\nRestored %q into bench %q.\n", m.Header.SiteName, target)
	if b.IsProd() {
		pw.Printf("  URL:           %s\n", b.ProxyHost)
	} else {
		pw.Printf("  URL:           http://localhost:%d\n", b.WebPort)
	}
	pw.Printf("  Site:          %s\n", b.SiteName)
	pw.Printf("  Admin:         administrator / %s\n", b.AdminPassword)
	pw.Printf("  Attachments:   %s\n", yesNo(withFiles))

	// Say plainly what a logical restore cannot carry, rather than letting the
	// user discover it.
	if m.Header.SiteName != b.SiteName {
		pw.Printf("\nThe site was renamed from %s to %s. Absolute URLs stored inside the\n"+
			"database still point at the old name — check Website Settings, Email Accounts,\n"+
			"webhooks and print formats.\n", m.Header.SiteName, b.SiteName)
	}
	if len(m.Bench.DomainAliases) > 0 && target != m.Header.BenchName {
		pw.Printf("\nThe original bench answered on extra hostnames. They were not restored, because\n" +
			"the bench they belong to may still be running. Add them with:\n")
		for _, a := range m.Bench.DomainAliases {
			pw.Printf("  ffm domain add %s %s\n", a, target)
		}
	}
	if m.Bench.Tunnel != nil && m.Bench.Tunnel.Enabled {
		pw.Printf("\nThe original bench had a VPS tunnel (%s). Its token lives in this host's\n"+
			"tunnel.json, not in the archive, so re-enable it with:\n  ffm tunnel %s --server %s\n",
			m.Bench.Tunnel.Server, target, m.Bench.Tunnel.Server)
	}
	if b.IsDev() {
		pw.Printf("\nThe ffc API key was reissued — Frappe mints a new secret on every request,\n" +
			"so the archived one cannot be put back.\n")
	}
}
