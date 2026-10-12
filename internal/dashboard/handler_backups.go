package dashboard

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/scheduler"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// archiveRow is one archive as the pages show it.
type archiveRow struct {
	// Key is "<bench>/<file>", the only way an archive is named in a form.
	Key       string
	Bench     string
	File      string
	Size      string
	Created   string
	Trigger   string
	Label     string
	Files     bool
	Encrypted bool
	Err       string
}

func humanSize(n int64) string {
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

func rowOf(benchName string, a manager.ArchiveInfo) archiveRow {
	file := filepath.Base(a.Path)
	row := archiveRow{
		Key: benchName + "/" + file, Bench: benchName, File: file,
		Size: humanSize(a.Size), Encrypted: a.Encrypted,
	}
	if a.Err != nil {
		row.Err = a.Err.Error()
		return row
	}
	row.Created = a.CreatedAt().Local().Format("2006-01-02 15:04")
	row.Trigger = a.Header.Trigger
	if row.Trigger == "" {
		row.Trigger = manager.TriggerManual
	}
	row.Label = a.Header.Label
	row.Files = a.Header.HasTier(manager.TierFiles)
	return row
}

func archiveRows(benchName string) ([]archiveRow, error) {
	all, err := manager.ScanArchives(benchName)
	if err != nil {
		return nil, err
	}
	rows := make([]archiveRow, 0, len(all))
	for _, a := range all {
		rows = append(rows, rowOf(benchName, a))
	}
	return rows, nil
}

// archiveBenches lists the bench names that have a directory under the backups
// dir, deleted benches included: their archives are the point of a restore.
func archiveBenches() []string {
	entries, err := os.ReadDir(config.BackupsDir())
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() && bench.ValidateName(e.Name()) == nil {
			names = append(names, e.Name())
		}
	}
	return names
}

// findArchive resolves a form's "<bench>/<file>" to an archive that
// ScanArchives lists under the backups dir. The text is never turned into a
// path: the bench part must be a valid bench name, the file part must equal the
// base name of a listed archive, and the path used is the one ScanArchives
// returned. Anything else, "../" included, matches nothing.
func findArchive(key string) (manager.ArchiveInfo, error) {
	benchName, file, ok := strings.Cut(key, "/")
	if !ok || file == "" || strings.ContainsAny(file, `/\`) || file != filepath.Base(file) || bench.ValidateName(benchName) != nil {
		return manager.ArchiveInfo{}, errors.New("choose an archive from the list")
	}
	all, err := manager.ScanArchives(benchName)
	if err != nil {
		return manager.ArchiveInfo{}, err
	}
	for _, a := range all {
		if filepath.Base(a.Path) == file {
			if a.Err != nil {
				return manager.ArchiveInfo{}, fmt.Errorf("archive %s is unreadable: %v", file, a.Err)
			}
			return a, nil
		}
	}
	return manager.ArchiveInfo{}, errors.New("choose an archive from the list")
}

// --- the hourly system job: shown, never changed ---

// schedulerInfo is the state of the hourly system job that runs scheduled
// backups. The dashboard only reads it; installing it stays an explicit CLI
// step (`ffm backup scheduler install`), like `ffm backup schedule --no-install`.
type schedulerInfo struct {
	Known     bool
	Installed bool
	Backend   string
	Where     string
	Drift     bool
	Missing   bool // the job's ffm binary no longer exists
	Warnings  []string
	Err       string
}

// schedulerStatus is replaceable so tests never read the host's crontab.
var schedulerStatus = func() schedulerInfo {
	var want *scheduler.Job
	if job, err := scheduler.CurrentJob(); err == nil {
		want = &job
	}
	st, err := scheduler.CheckStatus(want)
	if err != nil {
		return schedulerInfo{Err: err.Error(), Backend: st.Backend}
	}
	return schedulerInfo{
		Known: true, Installed: st.Installed, Backend: st.Backend, Where: st.Where,
		Drift: st.Drift, Missing: st.BinaryMissing, Warnings: st.Warnings,
	}
}

func describePolicy(p state.BackupPolicy) string {
	var keep []string
	if p.KeepHourly > 0 {
		keep = append(keep, fmt.Sprintf("%d hourly", p.KeepHourly))
	}
	if p.KeepDaily > 0 {
		keep = append(keep, fmt.Sprintf("%d daily", p.KeepDaily))
	}
	if p.KeepWeekly > 0 {
		keep = append(keep, fmt.Sprintf("%d weekly", p.KeepWeekly))
	}
	return fmt.Sprintf("every %dh, keeping %s (never fewer than %d), attachments %s",
		p.EveryHours, strings.Join(keep, " + "), manager.RetentionFloor, p.Files)
}

// --- overview: GET /admin/backups ---

type backupBenchRow struct {
	Name     string
	Archives int
	Newest   string
	Schedule string
	Exists   bool
}

type backupsOverviewData struct {
	pageBase
	Rows      []backupBenchRow
	Scheduler schedulerInfo
}

// BackupsOverview lists every bench (and every deleted bench that left
// archives behind) with its archive count and schedule.
func (h *Handler) BackupsOverview(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	benches, err := h.Svc.LoadBenches()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	seen := map[string]bool{}
	var rows []backupBenchRow
	add := func(name string, b *state.Bench) {
		seen[name] = true
		row := backupBenchRow{Name: name, Exists: b != nil, Schedule: "none"}
		if as, err := manager.ScanArchives(name); err == nil {
			row.Archives = len(as)
			for _, a := range as {
				if a.Err == nil {
					row.Newest = a.CreatedAt().Local().Format("2006-01-02 15:04")
					break
				}
			}
		}
		if b != nil && b.BackupSchedule != nil {
			if b.BackupSchedule.Enabled {
				row.Schedule = describePolicy(*b.BackupSchedule)
			} else {
				row.Schedule = "disabled"
			}
		}
		rows = append(rows, row)
	}
	for i := range benches {
		add(benches[i].Name, &benches[i])
	}
	for _, name := range archiveBenches() {
		if !seen[name] {
			add(name, nil)
		}
	}
	h.renderPage(w, r, "backups_overview_body", &backupsOverviewData{
		pageBase:  h.base(r, "Backups", "backups"),
		Rows:      rows,
		Scheduler: schedulerStatus(),
	})
}

// --- per-bench: GET /admin/benches/{name}/backups ---

type benchBackupsData struct {
	pageBase
	Name      string
	Archives  []archiveRow
	Policy    *state.BackupPolicy
	Status    *manager.ScheduleStatus
	LastRun   string
	NextDue   string
	LastOK    string
	Scheduler schedulerInfo
}

func (h *Handler) BenchBackups(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	b, err := h.Svc.GetBench(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rows, err := archiveRows(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	data := &benchBackupsData{
		pageBase:  h.base(r, "Backups — "+name, "backups"),
		Name:      name,
		Archives:  rows,
		Policy:    b.BackupSchedule,
		Scheduler: schedulerStatus(),
	}
	if b.BackupSchedule != nil {
		if statuses, err := h.Svc.ScheduleStatuses(); err == nil {
			for i := range statuses {
				if statuses[i].Bench == name {
					st := statuses[i]
					data.Status = &st
					data.LastOK = fmtTime(st.LastSuccess, "never")
					data.NextDue = fmtTime(st.NextDue, "next tick")
					if !st.Run.LastAttempt.IsZero() {
						data.LastRun = st.Run.Result + ", " + st.Run.LastAttempt.Local().Format("2006-01-02 15:04")
						if st.Run.Error != "" {
							data.LastRun += ": " + clip(firstLine(st.Run.Error), 200)
						}
					}
				}
			}
		}
	}
	h.renderPage(w, r, "bench_backups_body", data)
}

func fmtTime(t time.Time, zero string) string {
	if t.IsZero() {
		return zero
	}
	return t.Local().Format("2006-01-02 15:04")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// BackupRun starts a backup as a job.
func (h *Handler) BackupRun(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	if _, err := h.Svc.GetBench(name); err != nil {
		http.NotFound(w, r)
		return
	}
	label := strings.TrimSpace(r.FormValue("label"))
	if len([]rune(label)) > 200 {
		label = string([]rune(label)[:200])
	}
	in := manager.BackupInput{BenchName: name, NoFiles: r.FormValue("no_files") == "1", Label: label}
	h.runJob(w, r, manager.JobBackup, name, backupsPath(name), func(pw manager.ProgressWriter) error {
		return h.Svc.Backup(in, pw)
	})
}

func backupsPath(name string) string { return benchPath(name) + "/backups" }

// --- prune: preview (GET), then confirm (POST, job) ---

type prunePreviewData struct {
	pageBase
	Name     string
	Removed  []archiveRow
	Kept     []archiveRow
	Partials []string
	Ignored  int
}

func pruneRows(benchName string, as []manager.ArchiveInfo) []archiveRow {
	rows := make([]archiveRow, 0, len(as))
	for _, a := range as {
		rows = append(rows, rowOf(benchName, a))
	}
	return rows
}

// BackupPrunePreview shows what pruning would delete, deleting nothing.
func (h *Handler) BackupPrunePreview(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	if _, err := h.Svc.GetBench(name); err != nil {
		http.NotFound(w, r)
		return
	}
	res, err := h.Svc.PruneBackups(name, true)
	if err != nil {
		redirectWithFlash(w, r, backupsPath(name), "", err.Error())
		return
	}
	partials := make([]string, 0, len(res.Partials))
	for _, p := range res.Partials {
		partials = append(partials, filepath.Base(p))
	}
	h.renderPage(w, r, "backups_prune_body", &prunePreviewData{
		pageBase: h.base(r, "Prune backups — "+name, "backups"),
		Name:     name,
		Removed:  pruneRows(name, res.Removed),
		Kept:     pruneRows(name, res.Kept),
		Partials: partials,
		Ignored:  res.Ignored,
	})
}

// pruneSelection is what a dry run would delete, as sorted base names.
type pruneSelection struct{ Archives, Partials []string }

func selectionOf(res manager.PruneResult) pruneSelection {
	sel := pruneSelection{}
	for _, a := range res.Removed {
		sel.Archives = append(sel.Archives, filepath.Base(a.Path))
	}
	for _, p := range res.Partials {
		sel.Partials = append(sel.Partials, filepath.Base(p))
	}
	sort.Strings(sel.Archives)
	sort.Strings(sel.Partials)
	return sel
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func (s pruneSelection) equal(o pruneSelection) bool {
	return slices.Equal(s.Archives, o.Archives) && slices.Equal(s.Partials, o.Partials)
}

// describeDiff names what differs between the previewed selection and now.
func (s pruneSelection) describeDiff(now pruneSelection) string {
	var parts []string
	for _, n := range now.Archives {
		if !slices.Contains(s.Archives, n) {
			parts = append(parts, "now also "+n)
		}
	}
	for _, n := range s.Archives {
		if !slices.Contains(now.Archives, n) {
			parts = append(parts, "no longer "+n)
		}
	}
	for _, n := range now.Partials {
		if !slices.Contains(s.Partials, n) {
			parts = append(parts, "now also "+n+" (partial)")
		}
	}
	for _, n := range s.Partials {
		if !slices.Contains(now.Partials, n) {
			parts = append(parts, "no longer "+n+" (partial)")
		}
	}
	if len(parts) == 0 {
		return "the selection changed"
	}
	return strings.Join(parts, ", ")
}

// BackupPrune deletes what the preview listed, as a job. The form carries the
// base names of the previewed archives and partial files (repeated fields
// `expect` and `expect_partial`), not counts. The handler recomputes the dry
// run and refuses unless the two SETS are equal, and the job does it again
// right before pruning and fails without deleting if the selection changed.
//
// What this guarantees: a prune never starts from a selection that differs
// from the one the user saw. What it cannot: PruneBackups picks its victims
// itself, so an archive taken between that last check and the delete could
// change the selection. The job therefore compares what was actually removed
// with the preview afterwards and fails, naming the difference, if it does not
// match.
func (h *Handler) BackupPrune(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	if _, err := h.Svc.GetBench(name); err != nil {
		http.NotFound(w, r)
		return
	}
	previewPath := backupsPath(name) + "/prune"
	if err := r.ParseForm(); err != nil {
		redirectWithFlash(w, r, previewPath, "", "Preview the prune first, then confirm it.")
		return
	}
	want := pruneSelection{Archives: sortedCopy(r.PostForm["expect"]), Partials: sortedCopy(r.PostForm["expect_partial"])}
	if len(want.Archives) == 0 && len(want.Partials) == 0 {
		redirectWithFlash(w, r, previewPath, "", "Preview the prune first, then confirm it.")
		return
	}
	res, err := h.Svc.PruneBackups(name, true)
	if err != nil {
		redirectWithFlash(w, r, backupsPath(name), "", err.Error())
		return
	}
	if now := selectionOf(res); !want.equal(now) {
		redirectWithFlash(w, r, previewPath, "", "The selection changed since the preview ("+want.describeDiff(now)+"). Check it again.")
		return
	}
	h.runJob(w, r, manager.JobPrune, name, backupsPath(name), func(pw manager.ProgressWriter) error {
		pw.Step("Pruning scheduled backups of " + name)
		dry, err := h.Svc.PruneBackups(name, true)
		if err != nil {
			return err
		}
		if now := selectionOf(dry); !want.equal(now) {
			return fmt.Errorf("nothing was deleted: the selection changed since the preview (%s)", want.describeDiff(now))
		}
		res, err := h.Svc.PruneBackups(name, false)
		if err != nil {
			return err
		}
		for _, a := range res.Removed {
			pw.Println("removed " + filepath.Base(a.Path))
		}
		pw.Println(fmt.Sprintf("kept %d, removed %d, left alone %d", len(res.Kept), len(res.Removed), res.Ignored))
		if got := selectionOf(res); !want.equal(got) {
			return fmt.Errorf("the prune removed a different set than the preview showed (%s)", want.describeDiff(got))
		}
		return nil
	})
}

// --- schedule ---

// BackupScheduleSet saves (or removes) the bench's backup policy. It never
// installs or removes the hourly system job: that is `ffm backup scheduler
// install`, an explicit step, exactly as `ffm backup schedule --no-install`.
func (h *Handler) BackupScheduleSet(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	b, err := h.Svc.GetBench(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	back := backupsPath(name)
	const note = " The hourly system job was not touched; install it with 'ffm backup scheduler install'."
	if r.FormValue("action") == "off" {
		if err := h.Svc.SetBackupSchedule(name, nil); err != nil {
			redirectWithFlash(w, r, back, "", err.Error())
			return
		}
		redirectWithFlash(w, r, back, "Scheduled backups are off. Existing archives are kept."+note, "")
		return
	}

	var p state.BackupPolicy
	switch every := strings.TrimSpace(r.FormValue("every")); {
	case every != "":
		hours, err := manager.ParseEvery(every)
		if err != nil {
			redirectWithFlash(w, r, back, "", err.Error())
			return
		}
		p = manager.PresetPolicy(hours)
		if b.BackupSchedule != nil {
			// A new interval resets retention, not encryption or targets.
			p.Encrypt, p.Targets = b.BackupSchedule.Encrypt, b.BackupSchedule.Targets
		}
	case b.BackupSchedule != nil:
		p = *b.BackupSchedule
	default:
		p = manager.PresetPolicy(24)
	}
	p.Enabled = true
	// An ordered slice, not a map: map iteration is random, so with two bad
	// fields the message named a different one on each request.
	for _, k := range []struct {
		field string
		dst   *int
	}{{"keep_hourly", &p.KeepHourly}, {"keep_daily", &p.KeepDaily}, {"keep_weekly", &p.KeepWeekly}} {
		if strings.TrimSpace(r.FormValue(k.field)) == "" {
			continue
		}
		n, err := formInt(r, k.field)
		if err != nil {
			redirectWithFlash(w, r, back, "", err.Error())
			return
		}
		*k.dst = n
	}
	if f := r.FormValue("files"); f != "" {
		p.Files = f
	}
	// SetBackupSchedule runs the same ValidatePolicy as the CLI.
	if err := h.Svc.SetBackupSchedule(name, &p); err != nil {
		redirectWithFlash(w, r, back, "", err.Error())
		return
	}
	redirectWithFlash(w, r, back, "Scheduled backups for "+name+": "+describePolicy(p)+"."+note, "")
}
