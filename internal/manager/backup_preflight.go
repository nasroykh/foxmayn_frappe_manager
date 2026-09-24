package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Problem is one reason a restore should not proceed.
//
// Override names the flag that forces past it, or is empty when the problem is
// not overridable at all. Keeping the override on the problem — rather than
// letting one blanket --force cover every gate — is what stops a user who
// passed --force to skip a confirmation prompt from also silently disabling the
// encryption-key gate.
type Problem struct {
	Message  string
	Override string
}

// String renders a problem with its escape hatch, if it has one.
func (p Problem) String() string {
	if p.Override == "" {
		return p.Message
	}
	return p.Message + "\n      (override with " + p.Override + ")"
}

// problemsError formats a set of problems as a single refusal.
func problemsError(problems []Problem) error {
	var b strings.Builder
	b.WriteString("cannot restore this archive:\n")
	for _, p := range problems {
		b.WriteString("  - ")
		b.WriteString(p.String())
		b.WriteString("\n")
	}
	return fmt.Errorf("%s", strings.TrimRight(b.String(), "\n"))
}

// checkArchive validates an archive against the restore that was asked for.
//
// It is a pure function over already-extracted data: no Docker, no state store,
// no network. That is deliberate — it makes the whole gate matrix unit-testable,
// and it means every refusal happens before anything on the host is mutated.
func checkArchive(m Manifest, targetName string, in RestoreInput) []Problem {
	var problems []Problem

	if !m.Header.HasTier(TierCore) {
		problems = append(problems, Problem{
			Message: "the archive has no database tier — it cannot restore a site",
		})
	}

	if err := bench.ValidateNewName(targetName); err != nil {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("invalid target bench name %q: %v", targetName, err),
		})
	}

	if m.Secrets.EncryptionKey == "" {
		problems = append(problems, Problem{
			Message: "the archive carries no site encryption_key — every Password field in the " +
				"restored site (email account passwords, integration secrets, payment keys) " +
				"will be permanently undecryptable",
			Override: "--allow-missing-encryption-key",
		})
	}

	// A GPG-encrypted dump is useless without the key that decrypts it, and the
	// extension does not reveal the encryption, so this must be checked against
	// the recorded encoding.
	if dbMemberEncoding(m) == archive.EncodingGPG && m.Secrets.BackupEncryptionKey == "" && in.EncryptionKey == "" {
		problems = append(problems, Problem{
			Message: "the database dump is GPG-encrypted and the archive carries no " +
				"backup_encryption_key",
			Override: "--encryption-key <key>",
		})
	}

	mode := m.Header.Mode
	if mode == "prod" {
		domain := in.Domain
		if domain == "" {
			domain = m.Bench.Domain
		}
		if domain == "" {
			problems = append(problems, Problem{
				Message:  "the archive is a production bench but records no domain",
				Override: "--domain <domain>",
			})
		}
		// --no-ssl or an ACME email settles it explicitly, so only an archive
		// that records nothing AND a caller that says nothing is a problem.
		if m.Bench.TLSMode == "" && !in.NoSSL && in.AcmeEmail == "" {
			// Records written before TLSMode existed force the inference this
			// field was added to replace. Say so rather than silently guessing:
			// deriving no-SSL from ProxyHost's scheme is wrong once
			// `ffm set-proxy --port 80` has rewritten it.
			problems = append(problems, Problem{
				Message: "the archive does not record how TLS was served for this production " +
					"bench, so ffm cannot tell whether to re-request a certificate",
				Override: "--no-ssl (plain HTTP) or --acme-email <addr> (Let's Encrypt)",
			})
		}
	}

	problems = append(problems, checkManifestValues(m, in)...)
	return problems
}

// dbMemberEncoding returns the recorded encoding of the database member, or
// EncodingNone when the archive has no database member.
func dbMemberEncoding(m Manifest) string {
	for _, mem := range m.Members {
		if strings.HasPrefix(mem.Path, archive.Prefix+"/db/") {
			return mem.Encoding
		}
	}
	return archive.EncodingNone
}

// verifyMembers checks the bytes actually extracted against what the manifest
// claims, so a corrupted or tampered archive fails before it touches a site.
func verifyMembers(m Manifest, res *archive.ExtractResult) error {
	var missing, corrupt []string
	for _, want := range m.Members {
		got, ok := res.Members[want.Path]
		if !ok {
			missing = append(missing, want.Path)
			continue
		}
		if got.Size != want.Size || got.SHA256 != want.SHA256 {
			corrupt = append(corrupt, want.Path)
		}
	}
	sort.Strings(missing)
	sort.Strings(corrupt)

	switch {
	case len(missing) > 0 && len(corrupt) > 0:
		return fmt.Errorf("archive is damaged: %d member(s) missing (%s) and %d corrupted (%s)",
			len(missing), strings.Join(missing, ", "), len(corrupt), strings.Join(corrupt, ", "))
	case len(missing) > 0:
		return fmt.Errorf("archive is incomplete: missing %s", strings.Join(missing, ", "))
	case len(corrupt) > 0:
		return fmt.Errorf("archive is corrupted: checksum mismatch on %s", strings.Join(corrupt, ", "))
	}
	return nil
}

// appsForRestore returns the app specs to pass to Create.
//
// Two sources disagree in practice and both matter. state.Bench.Apps carries
// what the user asked for, including SSH/HTTPS URLs and @branch suffixes needed
// to clone a private fork; the site's installed_apps is the authoritative list
// of what the database actually expects. On a real bench these drift — a bench
// record of ["erpnext"] against installed_apps of [frappe, erpnext] — so the
// specs win where they overlap, and any installed app missing from them is
// appended by bare name rather than dropped.
func appsForRestore(m Manifest) []string {
	specs := append([]string(nil), m.Bench.Apps...)

	known := make(map[string]bool, len(specs)+1)
	// frappe is the framework itself: bench init installs it and it is never a
	// --apps entry.
	known["frappe"] = true
	for _, raw := range specs {
		known[bench.ParseAppSpec(raw, "").DisplayName()] = true
	}
	// An app installed on the site but absent from the bench record has to be
	// cloned from somewhere. The bare name resolves against Frappe's official
	// app registry, which is wrong for anything a user got with an explicit
	// URL, so the remote recorded at backup time wins when there is one.
	remotes := make(map[string]AppInfo, len(m.Apps))
	for _, a := range m.Apps {
		remotes[a.Name] = a
	}
	for _, app := range m.Site.InstalledApps {
		if known[app] {
			continue
		}
		known[app] = true
		if info, ok := remotes[app]; ok && info.Remote != "" {
			spec := info.Remote
			if info.Branch != "" && info.Branch != "HEAD" {
				spec += "@" + info.Branch
			}
			specs = append(specs, spec)
			continue
		}
		specs = append(specs, app)
	}
	return specs
}

// scrubSecrets replaces credentials with a placeholder.
//
// Every captured command output passes through this before it can reach an
// error string, a ProgressWriter line, jobs.json (which is persisted in plain
// text) or the dashboard's job-detail page. Frappe interpolates connection
// details into its own failure messages, so a failed restore prints the
// database root password unless it is scrubbed here.
func scrubSecrets(s string, secrets ...string) string {
	for _, secret := range secrets {
		// Short values would turn common substrings into noise; real ffm
		// credentials are far longer than this floor.
		if len(secret) < 6 {
			continue
		}
		s = strings.ReplaceAll(s, secret, "***")
	}
	return s
}

// benchSecrets returns the values that must never appear in output for a bench.
func benchSecrets(b state.Bench, extra ...string) []string {
	return append([]string{b.DBPassword, b.AdminPassword}, extra...)
}

// checkNameFree reports why a bench name cannot be used for a fresh restore.
//
// It looks in four places, not one. A bench directory or a leftover Docker
// volume from a half-deleted bench is invisible to the state store, and a
// restore that reused an orphaned volume would silently inherit its old
// database — including its old root password, which then fails the prod
// healthcheck and blocks every service that depends on it.
func checkNameFree(name string, dirExists, volumeExists, containerExists bool, tracked bool) []Problem {
	var problems []Problem
	if tracked {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("a bench named %q already exists — restore always creates a new "+
				"bench, so choose another name: ffm restore <archive> <newname>", name),
		})
	}
	if dirExists {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("%s already exists on disk", filepath.Join("~", "frappe", name)),
		})
	}
	if volumeExists {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("Docker volumes for project %q still exist — a restore would reuse "+
				"the old database, including its old root password", bench.ProjectName(name)),
		})
	}
	if containerExists {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("containers for project %q still exist", bench.ProjectName(name)),
		})
	}
	return problems
}

// honoursFileModes reports whether a directory's filesystem actually applies
// POSIX permission bits.
//
// This is a runtime probe rather than a filesystem-type guess because the case
// that matters is ordinary: a Windows drive mounted into WSL2 silently ignores
// chmod, so an archive holding the database root password and the site
// encryption key would sit there world-readable while reporting mode 0600.
func honoursFileModes(dir string) bool {
	f, err := os.CreateTemp(dir, ".ffm-mode-probe-*")
	if err != nil {
		return true // cannot tell; do not cry wolf
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()

	if err := f.Chmod(0o600); err != nil {
		return true
	}
	info, err := os.Stat(name)
	if err != nil {
		return true
	}
	return info.Mode().Perm() == 0o600
}

// An archive is a file that moves between machines, so every value read out of
// one is untrusted input — the same standing this codebase already gives a
// domain alias, which is normalised before it can reach a Traefik label.
//
// The sinks here are worse than a label: Create and Restore interpolate the
// branch, repo, app specs, passwords and encryption key into `bash -c` command
// strings that run inside the container. These patterns are deliberately
// narrower than what the underlying tools accept, because the cost of a
// too-strict rule is a clear error message and the cost of a too-loose one is
// arbitrary command execution.
var (
	// gitRefRe matches a branch, tag or ref path.
	gitRefRe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	// gitURLRe matches an SSH or HTTPS repository URL, without shell characters.
	gitURLRe = regexp.MustCompile(`^[A-Za-z0-9._~:/@-]+$`)
	// appNameRe matches a Frappe app's module name.
	appNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// commitRe matches a full or abbreviated git object id.
	commitRe = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	// secretRe and shellMetaRe gate the credentials an archive carries. Every
	// command now shell-quotes them, so this is defence in depth against an
	// untrusted archive rather than the only barrier.
	secretRe = regexp.MustCompile(`^[A-Za-z0-9._~:@!*+,=/#$%^&()<>?|;'"-]+$`)
	// shellMetaRe matches what must never reach a `bash -c` string unquoted.
	shellMetaRe = regexp.MustCompile("[`$;&|<>()\\\\'\"\\s]")
)

// checkManifestValues validates every archive-sourced value that ends up in a
// shell command or a rendered compose file.
func checkManifestValues(m Manifest, in RestoreInput) []Problem {
	var problems []Problem
	bad := func(field, value string) {
		problems = append(problems, Problem{
			Message: fmt.Sprintf("the archive's %s contains characters ffm will not pass to a shell: %q "+
				"— the archive is corrupt or was not written by ffm", field, value),
		})
	}

	if b := m.Bench.FrappeBranch; b != "" && !gitRefRe.MatchString(b) {
		bad("frappe branch", b)
	}
	if r := m.Bench.FrappeRepo; r != "" {
		spec := bench.ParseAppSpec(r, "")
		if !gitURLRe.MatchString(spec.Source) || (spec.Branch != "" && !gitRefRe.MatchString(spec.Branch)) {
			bad("frappe repo", r)
		}
	}
	for _, raw := range appsForRestore(m) {
		spec := bench.ParseAppSpec(raw, "")
		okSource := appNameRe.MatchString(spec.Source) || gitURLRe.MatchString(spec.Source)
		if !okSource || (spec.Branch != "" && !gitRefRe.MatchString(spec.Branch)) {
			bad("app source", raw)
		}
	}
	for _, app := range m.Apps {
		if !appNameRe.MatchString(app.Name) {
			bad("app name", app.Name)
		}
		if in.PinApps && app.Commit != "" && !commitRe.MatchString(app.Commit) {
			bad("app commit", app.Commit)
		}
	}
	adminPassword := m.Secrets.AdminPassword
	if in.AdminPassword != "" {
		// Not used: --admin-password replaces it, which is exactly the way out
		// the message below recommends.
		adminPassword = ""
	}
	for _, f := range []struct{ field, value string }{
		{"administrator password", adminPassword},
		{"database password", m.Secrets.DBRootPassword},
		{"backup encryption key", m.Secrets.BackupEncryptionKey},
	} {
		if f.value == "" {
			continue
		}
		if shellMetaRe.MatchString(f.value) || !secretRe.MatchString(f.value) {
			// Never echo a credential back, even a rejected one.
			problems = append(problems, Problem{
				Message: fmt.Sprintf("the archive's %s contains characters ffm cannot pass to a shell "+
					"safely — restore with an explicit --admin-password, or take the backup again", f.field),
			})
		}
	}
	// The domain is rendered into a Traefik router rule between backticks, where
	// an unvalidated value injects router configuration.
	if d := m.Bench.Domain; d != "" {
		if _, err := bench.NormalizeDomain(d); err != nil {
			problems = append(problems, Problem{
				Message: fmt.Sprintf("the archive's domain is not a valid hostname: %v", err),
			})
		}
	}
	for _, a := range m.Bench.DomainAliases {
		if _, err := bench.NormalizeDomain(a); err != nil {
			problems = append(problems, Problem{
				Message: fmt.Sprintf("the archive's domain alias is not a valid hostname: %v", err),
			})
		}
	}
	return problems
}
