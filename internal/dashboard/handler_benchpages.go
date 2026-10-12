package dashboard

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// existingBench loads {name} or answers 404.
func (h *Handler) existingBench(w http.ResponseWriter, r *http.Request) (state.Bench, bool) {
	b, err := h.Svc.GetBench(r.PathValue("name"))
	if err != nil {
		http.NotFound(w, r)
		return state.Bench{}, false
	}
	return b, true
}

// --- domains ---

type domainsPageData struct {
	pageBase
	Name    string
	Mode    string
	Primary string
	Aliases []string
	Err     string
}

func domainsPath(name string) string { return benchPath(name) + "/domains" }

// BenchDomains lists the primary host and the aliases. A read of the bench
// record, so it is synchronous.
func (h *Handler) BenchDomains(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	primary, aliases, err := h.Svc.DomainList(b.Name)
	data := &domainsPageData{
		pageBase: h.base(r, "Domains — "+b.Name, "benches"),
		Name:     b.Name, Mode: b.Mode, Primary: primary, Aliases: aliases,
	}
	if err != nil {
		data.Err = err.Error()
	}
	h.renderPage(w, r, "domains_body", data)
}

// DomainAdd adds an alias. Applying it re-renders the compose file and runs
// `up -d`, so it is a job.
func (h *Handler) DomainAdd(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	in := manager.DomainInput{Name: b.Name, Domain: strings.TrimSpace(r.FormValue("domain")), TLS: r.FormValue("tls") == "1"}
	h.runJob(w, r, manager.JobDomainAdd, b.Name, domainsPath(b.Name), func(pw manager.ProgressWriter) error {
		return h.Svc.DomainAdd(in, pw)
	})
}

// DomainRemove removes an alias, as a job for the same reason.
func (h *Handler) DomainRemove(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	in := manager.DomainInput{Name: b.Name, Domain: strings.TrimSpace(r.FormValue("domain"))}
	h.runJob(w, r, manager.JobDomainRemove, b.Name, domainsPath(b.Name), func(pw manager.ProgressWriter) error {
		return h.Svc.DomainRemove(in, pw)
	})
}

// --- app update ---

type updatePageData struct {
	pageBase
	Name   string
	Branch string
	Apps   []string
}

func updatePath(name string) string { return benchPath(name) + "/update" }

// AppUpdatePage shows the update form: preview first, then run.
func (h *Handler) AppUpdatePage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	h.renderPage(w, r, "update_body", &updatePageData{
		pageBase: h.base(r, "Update apps — "+b.Name, "benches"),
		Name:     b.Name, Branch: b.FrappeBranch, Apps: b.Apps,
	})
}

func (h *Handler) updateInput(r *http.Request, name string, dry bool) manager.UpdateInput {
	return manager.UpdateInput{
		Name:       name,
		Apps:       splitList(r.FormValue("apps")),
		ToBranch:   strings.TrimSpace(r.FormValue("to_branch")),
		DryRun:     dry,
		NoRollback: r.FormValue("no_rollback") == "1",
	}
}

// AppUpdatePreview runs Update with DryRun: it prints the plan and changes
// nothing. It execs into the bench (git state per app), so it is a job.
func (h *Handler) AppUpdatePreview(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	in := h.updateInput(r, b.Name, true)
	h.runJob(w, r, manager.JobUpdate, b.Name, updatePath(b.Name), func(pw manager.ProgressWriter) error {
		return h.Svc.Update(in, pw)
	})
}

// AppUpdateRun runs the update (backup, snapshot, maintenance mode, pull,
// migrate, build, rollback on failure) as a job. Unlike `ffm app update` it
// does not run the project's ffm.yaml post-update hooks: those need the
// trust prompt, which a web request cannot answer.
func (h *Handler) AppUpdateRun(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	in := h.updateInput(r, b.Name, false)
	h.runJob(w, r, manager.JobUpdate, b.Name, updatePath(b.Name), func(pw manager.ProgressWriter) error {
		return h.Svc.Update(in, pw)
	})
}

// --- snapshots ---

type snapshotRow struct {
	Name    string
	Created string
	Files   bool
	Size    string
	Apps    int
}

type snapshotsPageData struct {
	pageBase
	Name      string
	Snapshots []snapshotRow
	Err       string
}

func snapshotsPath(name string) string { return benchPath(name) + "/snapshots" }

// BenchSnapshots lists the bench's snapshots (a directory listing).
func (h *Handler) BenchSnapshots(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	data := &snapshotsPageData{pageBase: h.base(r, "Snapshots — "+b.Name, "benches"), Name: b.Name}
	snaps, err := h.Svc.ListSnapshots(b.Name)
	if err != nil {
		data.Err = err.Error()
	}
	for _, s := range snaps {
		data.Snapshots = append(data.Snapshots, snapshotRow{
			Name: s.Name, Created: s.CreatedAt.Local().Format(time.DateTime),
			Files: s.Files, Size: humanSize(s.Size), Apps: len(s.Commits),
		})
	}
	h.renderPage(w, r, "snapshots_body", data)
}

// SnapshotCreate takes a snapshot as a job (bench backup in the container).
func (h *Handler) SnapshotCreate(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	in := manager.SnapshotInput{Bench: b.Name, Name: strings.TrimSpace(r.FormValue("name")), Files: r.FormValue("files") == "1"}
	h.runJob(w, r, manager.JobSnapshotCreate, b.Name, snapshotsPath(b.Name), func(pw manager.ProgressWriter) error {
		_, err := h.Svc.CreateSnapshot(in, pw)
		return err
	})
}

// knownSnapshot checks the name against the bench's own snapshot listing, so
// only an existing snapshot is ever acted on.
func (h *Handler) knownSnapshot(bench, name string) error {
	snaps, err := h.Svc.ListSnapshots(bench)
	if err != nil {
		return err
	}
	for _, s := range snaps {
		if s.Name == name {
			return nil
		}
	}
	return fmt.Errorf("bench %q has no snapshot %q", bench, name)
}

// SnapshotRestore replaces the live site's database in place, as a job.
func (h *Handler) SnapshotRestore(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	name := r.FormValue("snapshot")
	if err := h.knownSnapshot(b.Name, name); err != nil {
		redirectWithFlash(w, r, snapshotsPath(b.Name), "", err.Error())
		return
	}
	in := manager.RestoreSnapshotInput{Bench: b.Name, Name: name, Migrate: r.FormValue("migrate") == "1"}
	h.runJob(w, r, manager.JobSnapshotRestore, b.Name, snapshotsPath(b.Name), func(pw manager.ProgressWriter) error {
		_, err := h.Svc.RestoreSnapshot(in, pw)
		return err
	})
}

// SnapshotDelete removes a snapshot directory. It is quick, so synchronous.
func (h *Handler) SnapshotDelete(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	b, ok := h.existingBench(w, r)
	if !ok {
		return
	}
	name := r.FormValue("snapshot")
	if err := h.Svc.DeleteSnapshot(b.Name, name); err != nil {
		redirectWithFlash(w, r, snapshotsPath(b.Name), "", err.Error())
		return
	}
	redirectWithFlash(w, r, snapshotsPath(b.Name), fmt.Sprintf("Snapshot %q deleted", name), "")
}
