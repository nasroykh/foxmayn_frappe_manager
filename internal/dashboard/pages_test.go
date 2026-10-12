package dashboard

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Every page's data type is a metaHolder through the embedded pageBase, which
// is what lets renderPage drop its per-type switch.
var (
	_ metaHolder = (*dashboardPageData)(nil)
	_ metaHolder = (*benchFormData)(nil)
	_ metaHolder = (*benchBackupsData)(nil)
	_ metaHolder = (*backupsOverviewData)(nil)
	_ metaHolder = (*prunePreviewData)(nil)
	_ metaHolder = (*restorePageData)(nil)
	_ metaHolder = (*domainsPageData)(nil)
	_ metaHolder = (*updatePageData)(nil)
	_ metaHolder = (*snapshotsPageData)(nil)
)

// writeTestArchive writes a header-only archive, which is all scanning reads.
func writeTestArchive(t *testing.T, benchName, file, trigger string, at time.Time) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("FFM_BACKUPS_DIR"), benchName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	w, err := archive.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(manager.Header{
		Kind: manager.ArchiveKind, SchemaVersion: manager.SchemaVersion, MinReaderVersion: 1,
		BenchName: benchName, SiteName: benchName + ".localhost", Mode: "dev", DBType: "mariadb",
		CreatedAt: at, Tiers: []string{manager.TierCore}, Trigger: trigger,
	})
	if _, err := w.AddBytes(archive.HeaderName, raw, archive.EncodingNone); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func getPage(h *Handler, handler http.HandlerFunc, path, name string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("name", name)
	rr := httptest.NewRecorder()
	handler(rr, req)
	return rr
}

func writeSnapshot(t *testing.T, b state.Bench, name string) {
	t.Helper()
	dir := filepath.Join(b.Dir, "workspace", ".ffm-snapshots", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := `{"name":"` + name + `","bench":"` + b.Name + `","created_at":"2026-01-01T00:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "snapshot.json"), []byte(meta), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestParityPagesRenderSelfContained(t *testing.T) {
	h, _ := newTestHandler(t)
	b := addBench(t, h, "alpha")
	p := manager.PresetPolicy(24)
	if err := h.Svc.SetBackupSchedule("alpha", &p); err != nil {
		t.Fatal(err)
	}
	writeTestArchive(t, "alpha", "alpha_1.auto.ffm.tar", manager.TriggerScheduled, time.Now().Add(-48*time.Hour))
	writeTestArchive(t, "gone", "gone_1.ffm.tar", "", time.Now().Add(-48*time.Hour))
	writeSnapshot(t, b, "snap1")

	pages := []struct {
		path    string
		handler http.HandlerFunc
		name    string
		want    string
	}{
		{"/admin/backups", h.BackupsOverview, "", "gone"}, // a deleted bench's archives are listed
		{"/admin/benches/alpha/backups", h.BenchBackups, "alpha", "alpha_1.auto.ffm.tar"},
		{"/admin/benches/alpha/backups/prune", h.BackupPrunePreview, "alpha", "Prune backups"},
		{"/admin/restore", h.RestorePage, "", "alpha/alpha_1.auto.ffm.tar"},
		{"/admin/benches/alpha/domains", h.BenchDomains, "alpha", "alpha.localhost"},
		{"/admin/benches/alpha/update", h.AppUpdatePage, "alpha", "Preview"},
		{"/admin/benches/alpha/snapshots", h.BenchSnapshots, "alpha", "snap1"},
		{"/admin/benches/new", h.BenchNew, "", "frappe_repo"},
		{"/admin/benches/alpha", h.BenchDetail, "alpha", "/admin/benches/alpha/doctor"},
	}
	for _, p := range pages {
		rr := getPage(h, p.handler, p.path, p.name)
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status %d: %s", p.path, rr.Code, rr.Body.String())
			continue
		}
		body := rr.Body.String()
		assertSelfContained(t, p.path, body)
		if !strings.Contains(body, p.want) {
			t.Errorf("%s: missing %q", p.path, p.want)
		}
		// The hourly job is described, never installed.
		if strings.Contains(p.path, "backups") && !strings.Contains(body, "Hourly system job") && p.path != "/admin/benches/alpha/backups/prune" {
			t.Errorf("%s: no scheduler status", p.path)
		}
		for _, secret := range []string{"adm1n-secret-pw", "db-secret-pw-77"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: renders a stored password", p.path)
			}
		}
		if strings.Contains(body, `name="csrf_token"`) && !strings.Contains(body, `name="csrf_token" value="`+h.signCSRF(csrfNonceOf(rr))+`"`) {
			t.Errorf("%s: form without the page's CSRF token", p.path)
		}
	}
}

// csrfNonceOf reads the nonce from the cookie the page set.
func csrfNonceOf(rr *httptest.ResponseRecorder) string {
	for _, c := range rr.Result().Cookies() {
		if c.Name == csrfCookieName {
			return c.Value
		}
	}
	return ""
}

func TestParityPagesNeedAuth(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	for path, handler := range map[string]http.HandlerFunc{
		"/admin/backups":                     h.BackupsOverview,
		"/admin/benches/alpha/backups":       h.BenchBackups,
		"/admin/benches/alpha/backups/prune": h.BackupPrunePreview,
		"/admin/restore":                     h.RestorePage,
		"/admin/benches/alpha/domains":       h.BenchDomains,
		"/admin/benches/alpha/update":        h.AppUpdatePage,
		"/admin/benches/alpha/snapshots":     h.BenchSnapshots,
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetPathValue("name", "alpha")
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d without credentials, want 401", path, rr.Code)
		}
	}
}

// parityPosts are the new POST routes with a form that gets past their own
// checks, so that an accepted request becomes a job.
func parityPosts(h *Handler, t *testing.T) []struct {
	name    string
	handler http.HandlerFunc
	path    string
	form    url.Values
	typ     manager.JobType
} {
	return []struct {
		name    string
		handler http.HandlerFunc
		path    string
		form    url.Values
		typ     manager.JobType
	}{
		{"backup", h.BackupRun, "/admin/benches/alpha/backups", url.Values{"label": {"x"}}, manager.JobBackup},
		{"doctor", h.BenchDoctor, "/admin/benches/alpha/doctor", url.Values{}, manager.JobDoctor},
		{"clone", h.BenchClone, "/admin/benches/alpha/clone", url.Values{"target": {"beta"}}, manager.JobClone},
		{"domain-add", h.DomainAdd, "/admin/benches/alpha/domains", url.Values{"domain": {"erp.internal"}}, manager.JobDomainAdd},
		{"domain-remove", h.DomainRemove, "/admin/benches/alpha/domains/remove", url.Values{"domain": {"erp.internal"}, "confirm": {"erp.internal"}}, manager.JobDomainRemove},
		{"update-preview", h.AppUpdatePreview, "/admin/benches/alpha/update/preview", url.Values{}, manager.JobUpdate},
		{"update", h.AppUpdateRun, "/admin/benches/alpha/update", url.Values{}, manager.JobUpdate},
		{"snapshot-create", h.SnapshotCreate, "/admin/benches/alpha/snapshots", url.Values{"name": {"s2"}}, manager.JobSnapshotCreate},
		{"snapshot-restore", h.SnapshotRestore, "/admin/benches/alpha/snapshots/restore", url.Values{"snapshot": {"snap1"}, "confirm": {"snap1"}}, manager.JobSnapshotRestore},
		{"restore", h.RestoreRun, "/admin/restore", url.Values{"archive": {"alpha/alpha_1.ffm.tar"}, "target": {"restored"}}, manager.JobRestore},
	}
}

func TestParityActionsRunAsJobs(t *testing.T) {
	// docker fails fast, so each job ends at once; what is checked is that a
	// job of the right type exists and the request did not wait for it.
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	b := addBench(t, h, "alpha")
	if err := h.Svc.UpdateBench("alpha", func(b *state.Bench) { b.DomainAliases = []string{"erp.internal"} }); err != nil {
		t.Fatal(err)
	}
	writeTestArchive(t, "alpha", "alpha_1.ffm.tar", "", time.Now().Add(-time.Hour))
	writeSnapshot(t, b, "snap1")
	for _, c := range parityPosts(h, t) {
		rr := postForm(h, h.Audited(c.handler), c.path, "alpha", c.form, true)
		id := jobIDFromLocation(t, rr)
		if j := waitJob(t, h, id); j.Type != c.typ {
			t.Errorf("%s: job type %q, want %q", c.name, j.Type, c.typ)
		}
	}
}

func TestParityPostsNeedAuthAndCSRF(t *testing.T) {
	h, _ := newTestHandler(t)
	b := addBench(t, h, "alpha")
	writeTestArchive(t, "alpha", "alpha_1.ffm.tar", "", time.Now().Add(-time.Hour))
	writeSnapshot(t, b, "snap1")
	posts := parityPosts(h, t)
	posts = append(posts, struct {
		name    string
		handler http.HandlerFunc
		path    string
		form    url.Values
		typ     manager.JobType
	}{"snapshot-delete", h.SnapshotDelete, "/admin/benches/alpha/snapshots/delete", url.Values{"snapshot": {"snap1"}, "confirm": {"snap1"}}, ""})
	for _, name := range []string{"prune", "schedule"} {
		handler := h.BackupPrune
		path := "/admin/benches/alpha/backups/prune"
		if name == "schedule" {
			handler, path = h.BackupScheduleSet, "/admin/benches/alpha/backups/schedule"
		}
		posts = append(posts, struct {
			name    string
			handler http.HandlerFunc
			path    string
			form    url.Values
			typ     manager.JobType
		}{name, handler, path, url.Values{}, ""})
	}
	for _, c := range posts {
		// No credentials at all.
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.form.Encode()))
		req.SetPathValue("name", "alpha")
		rr := httptest.NewRecorder()
		c.handler(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without credentials: %d, want 401", c.name, rr.Code)
		}
		// Credentials but no CSRF token.
		rr = postForm(h, h.Audited(c.handler), c.path, "alpha", c.form, false)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s without a CSRF token: %d, want 403", c.name, rr.Code)
		}
		// A GET is not a way in.
		req = httptest.NewRequest(http.MethodGet, c.path, nil)
		req.SetBasicAuth("u", "secret")
		req.SetPathValue("name", "alpha")
		rr = httptest.NewRecorder()
		c.handler(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s via GET: %d, want 405", c.name, rr.Code)
		}
	}
	if n := len(h.Jobs.ListJobs()); n != 0 {
		t.Fatalf("%d jobs created by refused requests", n)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "workspace", ".ffm-snapshots", "snap1")); err != nil {
		t.Fatalf("snapshot deleted by a refused request: %v", err)
	}
	// Every refusal and acceptance is audited by the real routes' wrapper.
}

// Only an archive ScanArchives lists under the backups directory can be
// restored; nothing a form says is ever used as a path.
func TestRestoreRefusesArchivesOutsideTheListing(t *testing.T) {
	h, fake := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	addBench(t, h, "alpha")
	writeTestArchive(t, "alpha", "alpha_1.ffm.tar", "", time.Now().Add(-time.Hour))
	outside := filepath.Join(t.TempDir(), "evil.ffm.tar")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(filepath.Join(os.Getenv("FFM_BACKUPS_DIR"), "alpha"), outside)
	bad := []string{
		"", "alpha", "alpha/", "/etc/passwd", outside, rel,
		"alpha/../alpha/alpha_1.ffm.tar", "../alpha/alpha_1.ffm.tar", "alpha/../../x.ffm.tar",
		"alpha/nope.ffm.tar", "beta/alpha_1.ffm.tar", "alpha/alpha_1.ffm.tar/..", `alpha\..\x`,
		"alpha/sub/alpha_1.ffm.tar", "a b/alpha_1.ffm.tar", "alpha/alpha_1.ffm.tar\x00",
	}
	for _, a := range bad {
		rr := postForm(h, h.Audited(h.RestoreRun), "/admin/restore", "", url.Values{"archive": {a}, "target": {"restored"}}, true)
		loc := rr.Header().Get("Location")
		if rr.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin/restore") || strings.Contains(loc, "/jobs/") {
			t.Errorf("archive %q: %d Location %q, want a refusal back to /admin/restore", a, rr.Code, loc)
		}
	}
	if n := len(h.Jobs.ListJobs()); n != 0 {
		t.Fatalf("%d jobs started for refused archives", n)
	}
	for _, c := range fake.Calls() {
		t.Errorf("a refused restore ran %q", c)
	}
	// Bad target names are refused too, before any job.
	for _, target := range []string{"", "../x", "has space", "list", "-x"} {
		rr := postForm(h, h.Audited(h.RestoreRun), "/admin/restore", "", url.Values{"archive": {"alpha/alpha_1.ffm.tar"}, "target": {target}}, true)
		if strings.Contains(rr.Header().Get("Location"), "/jobs/") {
			t.Errorf("target %q started a job", target)
		}
	}
	// The listed archive is accepted and the job gets the scanned path.
	rr := postForm(h, h.Audited(h.RestoreRun), "/admin/restore", "", url.Values{"archive": {"alpha/alpha_1.ffm.tar"}, "target": {"restored"}, "dry_run": {"1"}}, true)
	waitJob(t, h, jobIDFromLocation(t, rr))
}

func TestPruneNeedsAPreviewOfTheSameArchives(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	p := manager.PresetPolicy(24)
	p.KeepHourly, p.KeepDaily, p.KeepWeekly = 0, 1, 0
	if err := h.Svc.SetBackupSchedule("alpha", &p); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for i := 1; i <= 6; i++ {
		paths = append(paths, writeTestArchive(t, "alpha", "alpha_"+string(rune('0'+i))+".auto.ffm.tar",
			manager.TriggerScheduled, time.Now().Add(-time.Duration(i)*72*time.Hour)))
	}
	manual := writeTestArchive(t, "alpha", "alpha_manual.ffm.tar", "", time.Now().Add(-1000*time.Hour))

	prev := getPage(h, h.BackupPrunePreview, "/admin/benches/alpha/backups/prune", "alpha")
	if prev.Code != http.StatusOK || !strings.Contains(prev.Body.String(), "Would be removed") {
		t.Fatalf("preview: %d", prev.Code)
	}
	for _, path := range append(paths, manual) {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("the preview deleted %s", path)
		}
	}
	res, err := h.Svc.PruneBackups("alpha", true)
	if err != nil || len(res.Removed) < 2 {
		t.Fatalf("setup: %d removable, err %v", len(res.Removed), err)
	}
	var names []string
	for _, a := range res.Removed {
		names = append(names, filepath.Base(a.Path))
		if !strings.Contains(prev.Body.String(), `name="expect" value="`+filepath.Base(a.Path)+`"`) {
			t.Errorf("the preview form does not carry %s", filepath.Base(a.Path))
		}
	}

	// Counts (the old form), nothing, a subset, a same-sized but different
	// set and a superset: none may start a job or delete anything.
	kept := filepath.Base(res.Kept[0].Path)
	for _, form := range []url.Values{
		{},
		{"expect_removed": {itoa(len(res.Removed))}, "expect_partials": {"0"}},
		{"expect": names[1:]},
		{"expect": append(append([]string(nil), names[1:]...), kept)},
		{"expect": append(append([]string(nil), names...), kept)},
		{"expect": names, "expect_partial": {"ghost.partial"}},
	} {
		rr := postForm(h, h.Audited(h.BackupPrune), "/admin/benches/alpha/backups/prune", "alpha", form, true)
		loc := rr.Header().Get("Location")
		if rr.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/admin/benches/alpha/backups/prune") {
			t.Errorf("form %v: %d Location %q, want back to the preview", form, rr.Code, loc)
		}
	}
	if len(h.Jobs.ListJobs()) != 0 {
		t.Fatal("a prune job started without a matching preview")
	}
	for _, path := range append(paths, manual) {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("a refused prune deleted %s", path)
		}
	}

	// The previewed set, in any order, is accepted.
	slices.Reverse(names)
	rr := postForm(h, h.Audited(h.BackupPrune), "/admin/benches/alpha/backups/prune", "alpha",
		url.Values{"expect": names}, true)
	j := waitJob(t, h, jobIDFromLocation(t, rr))
	if j.Type != manager.JobPrune || j.Status != manager.JobSucceeded {
		t.Fatalf("prune job: %s %s %s", j.Type, j.Status, j.Error)
	}
	for _, a := range res.Removed {
		if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
			t.Errorf("%s survived the prune", a.Path)
		}
	}
	if _, err := os.Stat(manual); err != nil {
		t.Errorf("the manual archive was pruned")
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// The dashboard saves the policy and leaves the system scheduler alone.
func TestScheduleSavesPolicyWithoutInstallingAnything(t *testing.T) {
	h, fake := newTestHandler(t)
	addBench(t, h, "alpha")
	rr := postForm(h, h.Audited(h.BackupScheduleSet), "/admin/benches/alpha/backups/schedule", "alpha",
		url.Values{"every": {"6h"}, "keep_daily": {"3"}, "files": {"weekly"}}, true)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status %d", rr.Code)
	}
	b, _ := h.Svc.GetBench("alpha")
	if b.BackupSchedule == nil || !b.BackupSchedule.Enabled || b.BackupSchedule.EveryHours != 6 ||
		b.BackupSchedule.KeepDaily != 3 || b.BackupSchedule.Files != "weekly" {
		t.Fatalf("policy = %+v", b.BackupSchedule)
	}
	flash, _ := flashes.take(strings.TrimPrefix(rr.Header().Get("Location"), "/admin/benches/alpha/backups?flash="))
	if !strings.Contains(flash, "not touched") || !strings.Contains(flash, "ffm backup scheduler install") {
		t.Errorf("flash does not say the job was not touched: %q", flash)
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "crontab") || strings.Contains(c, "systemctl") || strings.Contains(c, "launchctl") || strings.Contains(c, "schtasks") {
			t.Errorf("the dashboard ran a scheduler command: %q", c)
		}
	}

	// An invalid interval changes nothing.
	postForm(h, h.Audited(h.BackupScheduleSet), "/admin/benches/alpha/backups/schedule", "alpha", url.Values{"every": {"90m"}}, true)
	if b, _ := h.Svc.GetBench("alpha"); b.BackupSchedule.EveryHours != 6 {
		t.Errorf("an invalid interval changed the policy: %+v", b.BackupSchedule)
	}
	// Off removes the policy.
	postForm(h, h.Audited(h.BackupScheduleSet), "/admin/benches/alpha/backups/schedule", "alpha", url.Values{"action": {"off"}}, true)
	if b, _ := h.Svc.GetBench("alpha"); b.BackupSchedule != nil {
		t.Errorf("policy still set after off: %+v", b.BackupSchedule)
	}
}

func TestSnapshotRestoreAndDeleteOnlyAcceptListedSnapshots(t *testing.T) {
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	b := addBench(t, h, "alpha")
	writeSnapshot(t, b, "snap1")
	keep := filepath.Join(b.Dir, "workspace", "keepme")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../../keepme", "nope", "", "snap1/../..", "/etc"} {
		rr := postForm(h, h.Audited(h.SnapshotRestore), "/admin/benches/alpha/snapshots/restore", "alpha", url.Values{"snapshot": {name}, "confirm": {name}}, true)
		if strings.Contains(rr.Header().Get("Location"), "/jobs/") {
			t.Errorf("restore of %q started a job", name)
		}
		rr = postForm(h, h.Audited(h.SnapshotDelete), "/admin/benches/alpha/snapshots/delete", "alpha", url.Values{"snapshot": {name}, "confirm": {name}}, true)
		if strings.Contains(rr.Header().Get("Location"), "/jobs/") {
			t.Errorf("delete of %q started a job", name)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a snapshot name reached outside the snapshots directory: %v", err)
	}
	rr := postForm(h, h.Audited(h.SnapshotDelete), "/admin/benches/alpha/snapshots/delete", "alpha", url.Values{"snapshot": {"snap1"}, "confirm": {"snap1"}}, true)
	if j := waitJob(t, h, jobIDFromLocation(t, rr)); j.Type != manager.JobSnapshotDelete || j.Status != manager.JobSucceeded {
		t.Fatalf("delete job: %s %s %s", j.Type, j.Status, j.Error)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "workspace", ".ffm-snapshots", "snap1")); !os.IsNotExist(err) {
		t.Errorf("snapshot not deleted: %v", err)
	}
}

// flashErrOf reads the error flash a handler redirected with.
func flashErrOf(rr *httptest.ResponseRecorder) string {
	loc := rr.Header().Get("Location")
	_, after, ok := strings.Cut(loc, "?flash=")
	if !ok {
		return ""
	}
	_, msg := flashes.take(after)
	return msg
}

// Restoring or deleting a snapshot and removing an alias need the name typed
// into the form; a data-confirm dialog is client-side only.
func TestDestructiveActionsNeedTheNameTyped(t *testing.T) {
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	b := addBench(t, h, "alpha")
	if err := h.Svc.UpdateBench("alpha", func(b *state.Bench) { b.DomainAliases = []string{"erp.internal"} }); err != nil {
		t.Fatal(err)
	}
	writeSnapshot(t, b, "snap1")
	snapDir := filepath.Join(b.Dir, "workspace", ".ffm-snapshots", "snap1")
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		field   string
		value   string
	}{
		{"snapshot-restore", h.SnapshotRestore, "/admin/benches/alpha/snapshots/restore", "snapshot", "snap1"},
		{"snapshot-delete", h.SnapshotDelete, "/admin/benches/alpha/snapshots/delete", "snapshot", "snap1"},
		{"domain-remove", h.DomainRemove, "/admin/benches/alpha/domains/remove", "domain", "erp.internal"},
	}
	for _, c := range cases {
		for _, confirm := range []string{"", "yes", "snap", c.value + " ", strings.ToUpper(c.value)} {
			form := url.Values{c.field: {c.value}}
			if confirm != "" {
				form.Set("confirm", confirm)
			}
			rr := postForm(h, h.Audited(c.handler), c.path, "alpha", form, true)
			if msg := flashErrOf(rr); rr.Code != http.StatusSeeOther || !strings.Contains(msg, "type its name") {
				t.Errorf("%s with confirm %q: %d flash %q, want a refusal asking for the name", c.name, confirm, rr.Code, msg)
			}
		}
	}
	if n := len(h.Jobs.ListJobs()); n != 0 {
		t.Fatalf("%d jobs started without a typed confirmation", n)
	}
	if _, err := os.Stat(snapDir); err != nil {
		t.Fatalf("snapshot deleted without confirmation: %v", err)
	}

	// An alias the bench does not have, or its primary host, is not removable
	// even with the right confirmation.
	for _, d := range []string{"other.internal", "alpha.localhost"} {
		rr := postForm(h, h.Audited(h.DomainRemove), "/admin/benches/alpha/domains/remove", "alpha", url.Values{"domain": {d}, "confirm": {d}}, true)
		if msg := flashErrOf(rr); !strings.Contains(msg, "has no alias") {
			t.Errorf("remove of %q: flash %q, want a refusal naming the missing alias", d, msg)
		}
	}
	if n := len(h.Jobs.ListJobs()); n != 0 {
		t.Fatalf("%d jobs started for a domain that is not an alias", n)
	}
}

// A snapshot delete runs under the bench's job key, so it is refused while a
// restore, update or backup of that bench is running.
func TestSnapshotDeleteRefusedWhileTheBenchIsBusy(t *testing.T) {
	h, _ := newTestHandler(t)
	b := addBench(t, h, "alpha")
	writeSnapshot(t, b, "snap1")
	release := make(chan struct{})
	if _, err := h.Svc.StartAction(h.Jobs, manager.JobUpdate, "alpha", func(manager.ProgressWriter) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)
	rr := postForm(h, h.Audited(h.SnapshotDelete), "/admin/benches/alpha/snapshots/delete", "alpha",
		url.Values{"snapshot": {"snap1"}, "confirm": {"snap1"}}, true)
	if loc := rr.Header().Get("Location"); strings.Contains(loc, "/jobs/") {
		t.Fatalf("delete started beside a running job: %q", loc)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "workspace", ".ffm-snapshots", "snap1")); err != nil {
		t.Fatalf("snapshot deleted while the bench was busy: %v", err)
	}
}

// writeManifestArchive writes an archive with a header and a manifest whose
// secrets are known, like a real backup but without any members.
func writeManifestArchive(t *testing.T, benchName, file, label string, sec manager.Secrets, b state.Bench) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("FFM_BACKUPS_DIR"), benchName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	hdr := manager.Header{
		Kind: manager.ArchiveKind, SchemaVersion: manager.SchemaVersion, MinReaderVersion: 1,
		BenchName: benchName, SiteName: benchName + ".localhost", Mode: "dev", DBType: "mariadb",
		CreatedAt: time.Now().Add(-time.Hour), Tiers: []string{manager.TierCore}, Label: label,
	}
	path := filepath.Join(dir, file)
	w, err := archive.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(hdr)
	if _, err := w.AddBytes(archive.HeaderName, raw, archive.EncodingNone); err != nil {
		t.Fatal(err)
	}
	m, err := w.AddBytes("ffm-backup/note.txt", []byte("x"), archive.EncodingNone)
	if err != nil {
		t.Fatal(err)
	}
	mraw, _ := json.Marshal(manager.Manifest{Header: hdr, Bench: b, Secrets: sec, Members: []archive.Member{m}})
	if _, err := w.AddBytes(archive.ManifestName, mraw, archive.EncodingNone); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A restore is keyed by its new target, which has no record, so the job would
// mask nothing; the archive's own credentials are masked instead.
func TestRestoreJobMasksTheArchivesSecrets(t *testing.T) {
	h, _ := newTestHandler(t)
	const adminPW, rootPW, keyVal = "arch-admin-pw-1", "arch-root-pw-22", "arch-enc-key-333"
	path := writeManifestArchive(t, "alpha", "alpha_1.ffm.tar", "label "+adminPW+" "+keyVal,
		manager.Secrets{AdminPassword: adminPW, DBRootPassword: rootPW, EncryptionKey: keyVal},
		state.Bench{Name: "alpha", AdminPassword: adminPW})
	got := manager.ArchiveSecrets(path)
	for _, want := range []string{adminPW, rootPW, keyVal} {
		if !slices.Contains(got, want) {
			t.Errorf("ArchiveSecrets lacks %q: %q", want, got)
		}
	}
	rr := postForm(h, h.Audited(h.RestoreRun), "/admin/restore", "", url.Values{
		"archive": {"alpha/alpha_1.ffm.tar"}, "target": {"restored"}, "dry_run": {"1"}, "web_port": {"18310"}, "socketio_port": {"19310"},
	}, true)
	j := waitJob(t, h, jobIDFromLocation(t, rr))
	all := j.Error + "\n" + strings.Join(jobText(j), "\n")
	if !strings.Contains(all, "label ***") {
		t.Fatalf("the dry run did not print the label, so the test proves nothing: %q", all)
	}
	for _, secret := range []string{adminPW, rootPW, keyVal} {
		if strings.Contains(all, secret) {
			t.Errorf("job output contains %q: %q", secret, all)
		}
	}
	// A foreign file is not an archive: nothing to mask, no panic.
	junk := filepath.Join(t.TempDir(), "x.ffm.tar")
	os.WriteFile(junk, []byte("not a tar"), 0o600)
	if got := manager.ArchiveSecrets(junk); len(got) != 0 {
		t.Errorf("secrets from junk: %q", got)
	}
}

func jobText(j *manager.Job) []string { return j.Lines }

// A clone is keyed by its new name, so the source's credentials are masked
// explicitly, and the target and domain are trimmed.
func TestCloneJobMasksTheSourcesSecretsAndTrims(t *testing.T) {
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	addBench(t, h, "alpha")
	rr := postForm(h, h.Audited(h.BenchClone), "/admin/benches/alpha/clone", "alpha",
		url.Values{"target": {"  beta \n"}, "domain": {" "}}, true)
	j := waitJob(t, h, jobIDFromLocation(t, rr))
	if j.BenchName != "beta" {
		t.Errorf("job key %q, want the trimmed target", j.BenchName)
	}
	got := manager.BenchSecrets(state.Bench{AdminPassword: "adm1n-secret-pw", DBPassword: "db-secret-pw-77"})
	if !slices.Contains(got, "adm1n-secret-pw") || !slices.Contains(got, "db-secret-pw-77") {
		t.Errorf("BenchSecrets = %q", got)
	}
	// The job text is the masked text: a started action masks what it prints.
	id, err := h.Svc.StartActionSecrets(h.Jobs, manager.JobClone, "gamma", got, func(pw manager.ProgressWriter) error {
		pw.Println("pw is db-secret-pw-77")
		return errors.New("failed with adm1n-secret-pw")
	})
	if err != nil {
		t.Fatal(err)
	}
	j = waitJob(t, h, id)
	if all := j.Error + strings.Join(jobText(j), "\n"); strings.Contains(all, "secret-pw") {
		t.Errorf("secret survived: %q", all)
	}
}

// bench's output during an update reaches the job; it used to go to io.Discard.
func TestUpdateJobForwardsBenchOutputToTheJob(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	body := h.updateJob(manager.UpdateInput{Name: "alpha"}, func(in manager.UpdateInput, pw manager.ProgressWriter) error {
		if in.Out == nil {
			t.Error("UpdateInput.Out is nil")
			return nil
		}
		in.Out.Write([]byte("Already up to date.\nrunning migrate\n"))
		return nil
	})
	id, err := h.Svc.StartAction(h.Jobs, manager.JobUpdate, "alpha", body)
	if err != nil {
		t.Fatal(err)
	}
	j := waitJob(t, h, id)
	if all := strings.Join(jobText(j), "\n"); !strings.Contains(all, "Already up to date.") || !strings.Contains(all, "running migrate") {
		t.Errorf("update output not in the job: %q", all)
	}
}

// After the create form is shown again its password fields are blank; a
// resubmit that leaves them blank must not silently take the defaults.
func TestCreateResubmitMustReenterPasswords(t *testing.T) {
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	// First submit: blank passwords still mean the CLI defaults.
	req := httptest.NewRequest(http.MethodPost, "/admin/benches", strings.NewReader(url.Values{"name": {"alpha"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	in, err := parseCreateForm(req)
	if err != nil || in.AdminPassword != "admin" || in.DBPassword != "ffm123456" {
		t.Fatalf("first submit: %v, %q %q", err, in.AdminPassword, in.DBPassword)
	}

	// An error re-render asks for them again.
	rr := postForm(h, h.Audited(h.BenchCreate), "/admin/benches", "", url.Values{
		"name": {"alpha"}, "web_port": {"8010"}, "socketio_port": {"9999"}, "admin_password": {"s3cret-adm"},
	}, true)
	body := rr.Body.String()
	if rr.Code != http.StatusBadRequest || !strings.Contains(body, `name="reentry" value="1"`) || !strings.Contains(body, "re-enter") {
		t.Fatalf("re-rendered form lacks the re-entry marker: %d", rr.Code)
	}
	if strings.Contains(body, "s3cret-adm") {
		t.Error("the password was echoed back")
	}

	// The resubmit with blank passwords is refused, naming what is missing.
	for _, form := range []url.Values{
		{"name": {"alpha"}, "reentry": {"1"}},
		{"name": {"alpha"}, "reentry": {"1"}, "admin_password": {"s3cret-adm"}},
		{"name": {"alpha"}, "reentry": {"1"}, "db_password": {"dbpw-secret"}},
	} {
		rr := postForm(h, h.Audited(h.BenchCreate), "/admin/benches", "", form, true)
		if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "again") {
			t.Errorf("form %v: %d, want a 400 asking for the passwords", form, rr.Code)
		}
	}
	if n := len(h.Jobs.ListJobs()); n != 0 {
		t.Fatalf("%d jobs started from a blank resubmit", n)
	}

	// Both given: accepted.
	rr = postForm(h, h.Audited(h.BenchCreate), "/admin/benches", "", url.Values{
		"name": {"alpha"}, "reentry": {"1"}, "admin_password": {"s3cret-adm"}, "db_password": {"dbpw-secret"},
	}, true)
	waitJob(t, h, jobIDFromLocation(t, rr))
}

func TestRestartFormReachesRebuildAndFresh(t *testing.T) {
	// Start fails after the build, which keeps the job short.
	h, fake := newTestHandler(t, fakeexec.Rule{Match: " up ", Exit: 1})
	addBench(t, h, "alpha")
	rr := postForm(h, h.Audited(h.BenchRestart), "/admin/benches/alpha/restart", "alpha", url.Values{"fresh": {"1"}}, true)
	waitJob(t, h, jobIDFromLocation(t, rr))
	if !fake.Called(" build ", "--no-cache", "--pull") {
		t.Errorf("fresh did not rebuild without the cache; calls: %q", fake.Calls())
	}

	h2, fake2 := newTestHandler(t, fakeexec.Rule{Match: " up ", Exit: 1})
	addBench(t, h2, "alpha")
	rr = postForm(h2, h2.Audited(h2.BenchRestart), "/admin/benches/alpha/restart", "alpha", url.Values{"rebuild": {"1"}}, true)
	waitJob(t, h2, jobIDFromLocation(t, rr))
	if !fake2.Called(" build") || fake2.Called("--no-cache") {
		t.Errorf("rebuild alone should build with the cache; calls: %q", fake2.Calls())
	}

	h3, fake3 := newTestHandler(t, fakeexec.Rule{Match: " up ", Exit: 1})
	addBench(t, h3, "alpha")
	rr = postForm(h3, h3.Audited(h3.BenchRestart), "/admin/benches/alpha/restart", "alpha", url.Values{}, true)
	waitJob(t, h3, jobIDFromLocation(t, rr))
	if fake3.Called(" build") {
		t.Errorf("a plain restart rebuilt the image; calls: %q", fake3.Calls())
	}
}

func TestParseCreateFormCarriesEveryField(t *testing.T) {
	form := url.Values{
		"name": {"alpha"}, "mode": {"prod"}, "frappe_branch": {"version-15"},
		"frappe_repo": {"https://github.com/org/frappe.git@main"},
		"python":      {"3.12"}, "node": {"22"}, "apps": {"erpnext, hrms\nmyapp@dev"},
		"admin_password": {"s3cret"}, "db_password": {"dbpw"}, "db_type": {"postgres"},
		"github_token": {"ghp_x"}, "domain": {"erp.example.com"}, "no_ssl": {"1"},
		"acme_email": {"a@b.co"}, "domain_aliases": {"erp.internal, shop.internal"},
		"alias_tls": {"1"}, "match_host_user": {"1"}, "web_port": {"8010"}, "socketio_port": {"9010"},
		"lan": {"1"}, "ssh_agent": {"1"}, "agent": {"1"}, "no_seed": {"1"}, "slow_query_log": {"1"},
		"gunicorn_workers": {"4"}, "worker_long": {"2"}, "worker_short": {"3"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/benches", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	in, err := parseCreateForm(req)
	if err != nil {
		t.Fatal(err)
	}
	want := manager.CreateInput{
		Name: "alpha", Mode: "prod", FrappeBranch: "version-15", FrappeRepo: "https://github.com/org/frappe.git@main",
		Python: "3.12", Node: "22", Apps: []string{"erpnext", "hrms", "myapp@dev"},
		AdminPassword: "s3cret", DBPassword: "dbpw", DBType: "postgres", GithubToken: "ghp_x",
		Domain: "erp.example.com", NoSSL: true, AcmeEmail: "a@b.co",
		DomainAliases: []string{"erp.internal", "shop.internal"}, AliasTLS: true, MatchHostUser: true,
		FixedWebPort: 8010, FixedSocketIOPort: 9010, Bind: state.BindLAN, SSHAgent: true, Agent: true,
		NoSeed: true, SlowQueryLog: true, GunicornWorkers: 4, WorkerLongCount: 2, WorkerShortCount: 3,
	}
	if strings.Join(in.Apps, "|") != strings.Join(want.Apps, "|") || strings.Join(in.DomainAliases, "|") != strings.Join(want.DomainAliases, "|") {
		t.Errorf("lists: apps %q aliases %q", in.Apps, in.DomainAliases)
	}
	in.Apps, in.DomainAliases, want.Apps, want.DomainAliases = nil, nil, nil, nil
	if in.Name != want.Name || in.Mode != want.Mode || in.FrappeBranch != want.FrappeBranch || in.FrappeRepo != want.FrappeRepo ||
		in.Python != want.Python || in.Node != want.Node || in.AdminPassword != want.AdminPassword ||
		in.DBPassword != want.DBPassword || in.DBType != want.DBType || in.GithubToken != want.GithubToken ||
		in.Domain != want.Domain || in.NoSSL != want.NoSSL || in.AcmeEmail != want.AcmeEmail ||
		in.AliasTLS != want.AliasTLS || in.MatchHostUser != want.MatchHostUser ||
		in.FixedWebPort != want.FixedWebPort || in.FixedSocketIOPort != want.FixedSocketIOPort ||
		in.Bind != want.Bind || in.SSHAgent != want.SSHAgent || in.Agent != want.Agent || in.NoSeed != want.NoSeed ||
		in.SlowQueryLog != want.SlowQueryLog || in.GunicornWorkers != want.GunicornWorkers ||
		in.WorkerLongCount != want.WorkerLongCount || in.WorkerShortCount != want.WorkerShortCount {
		t.Errorf("parsed input = %+v\nwant %+v", in, want)
	}

	// Without the LAN box the bench binds to loopback, like the CLI.
	delete(form, "lan")
	req = httptest.NewRequest(http.MethodPost, "/admin/benches", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if in, _ := parseCreateForm(req); in.Bind != state.BindLoopback {
		t.Errorf("bind = %q, want loopback", in.Bind)
	}
}

func TestCreateFormRoundTripAndRejection(t *testing.T) {
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	// The new fields are on the form.
	rr := getPage(h, h.BenchNew, "/admin/benches/new", "")
	for _, f := range []string{"frappe_repo", "domain_aliases", "alias_tls", "match_host_user", "web_port", "socketio_port",
		`name="lan"`, "ssh_agent", `name="python"`, `name="node"`, `name="agent"`, "no_seed"} {
		if !strings.Contains(rr.Body.String(), f) {
			t.Errorf("create form lacks %s", f)
		}
	}

	// A pair that does not follow the pairing is refused with the form shown
	// again: what was typed is back, the secrets are not.
	bad := url.Values{
		"name": {"alpha"}, "mode": {"dev"}, "frappe_repo": {"https://example.com/my-frappe.git"},
		"domain_aliases": {"erp.internal"}, "web_port": {"8010"}, "socketio_port": {"9999"},
		"admin_password": {"hunter2-secret"}, "db_password": {"dbpass-secret"}, "github_token": {"ghp_tokensecret"},
	}
	rr = postForm(h, h.Audited(h.BenchCreate), "/admin/benches", "", bad, true)
	body := rr.Body.String()
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rr.Code)
	}
	for _, want := range []string{"my-frappe.git", "erp.internal", "socketio_port must be web_port"} {
		if !strings.Contains(body, want) {
			t.Errorf("re-rendered form lacks %q", want)
		}
	}
	for _, secret := range []string{"hunter2-secret", "dbpass-secret", "ghp_tokensecret"} {
		if strings.Contains(body, secret) {
			t.Errorf("re-rendered form contains %q", secret)
		}
	}
	assertSelfContained(t, "create form error", body)
	if len(h.Jobs.ListJobs()) != 0 {
		t.Fatal("an invalid form started a job")
	}

	// An accepted form reaches the pipeline: the job's first line names the
	// custom repo, and the job (docker fails) is a create job.
	ok := url.Values{
		"name": {"alpha"}, "mode": {"dev"}, "frappe_branch": {"version-15"}, "python": {"3.12"}, "node": {"22"},
		"frappe_repo": {"https://example.com/my-frappe.git@feature"}, "db_type": {"mariadb"},
	}
	rr = postForm(h, h.Audited(h.BenchCreate), "/admin/benches", "", ok, true)
	j := waitJob(t, h, jobIDFromLocation(t, rr))
	if j.Type != manager.JobCreate {
		t.Fatalf("job type %q", j.Type)
	}
	all := strings.Join(j.Lines, "\n")
	if !strings.Contains(all, "https://example.com/my-frappe.git@feature") || !strings.Contains(all, "python: 3.12") || !strings.Contains(all, "node: 22") {
		t.Errorf("the job did not receive the new fields:\n%s", all)
	}

	// The CLI's checks run inside the job: a LAN bench with the default admin
	// password is refused with the CLI's message.
	lan := url.Values{"name": {"beta"}, "mode": {"dev"}, "lan": {"1"}}
	rr = postForm(h, h.Audited(h.BenchCreate), "/admin/benches", "", lan, true)
	j = waitJob(t, h, jobIDFromLocation(t, rr))
	if j.Status != manager.JobFailed || !strings.Contains(j.Error, "default admin password") {
		t.Errorf("LAN with the default password: %s %q", j.Status, j.Error)
	}
}
