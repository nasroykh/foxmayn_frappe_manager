package dashboard

import (
	"net/http"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type restorePageData struct {
	pageBase
	Archives []archiveRow
	Selected string
}

// RestorePage lists every archive under the backups directory, per bench
// (deleted benches included), and the restore form.
func (h *Handler) RestorePage(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	var rows []archiveRow
	for _, name := range archiveBenches() {
		bs, err := archiveRows(name)
		if err != nil {
			continue
		}
		rows = append(rows, bs...)
	}
	h.renderPage(w, r, "restore_body", &restorePageData{
		pageBase: h.base(r, "Restore", "restore"),
		Archives: rows,
		Selected: r.URL.Query().Get("archive"),
	})
}

// RestoreRun restores an archive into a NEW bench as a job.
//
// There is no path, upload or free-text source: the form names an archive as
// "<bench>/<file>" and findArchive accepts it only if ScanArchives lists it
// under the backups directory. The target must be a valid, new bench name;
// Restore refuses an existing one again under its own lock.
func (h *Handler) RestoreRun(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	back := "/admin/restore"
	a, err := findArchive(r.FormValue("archive"))
	if err != nil {
		redirectWithFlash(w, r, back, "", err.Error())
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	if err := bench.ValidateName(target); err != nil {
		redirectWithFlash(w, r, back, "", err.Error())
		return
	}
	if err := bench.ValidateNewName(target); err != nil {
		redirectWithFlash(w, r, back, "", err.Error())
		return
	}
	web, err1 := formInt(r, "web_port")
	sock, err2 := formInt(r, "socketio_port")
	if err1 != nil || err2 != nil {
		redirectWithFlash(w, r, back, "", "ports must be whole numbers")
		return
	}
	in := manager.RestoreInput{
		Archive:         a.Path,
		TargetName:      target,
		WithFiles:       r.FormValue("no_files") != "1",
		DryRun:          r.FormValue("dry_run") == "1",
		Domain:          strings.TrimSpace(r.FormValue("domain")),
		NoSSL:           r.FormValue("no_ssl") == "1",
		AcmeEmail:       strings.TrimSpace(r.FormValue("acme_email")),
		ReallocatePorts: r.FormValue("reallocate_ports") == "1",
		WebPort:         web,
		SocketIOPort:    sock,
		SkipMigrate:     r.FormValue("skip_migrate") == "1",
		PinApps:         r.FormValue("pin_apps") == "1",
		LAN:             r.FormValue("lan") == "1",
	}
	// The job is keyed by the new target, which has no record yet, so
	// StartAction would mask nothing. The archive's own credentials are read
	// from its manifest instead (none for an age-encrypted archive: its
	// manifest is inside the ciphertext, and the dashboard does not decrypt it
	// just to learn them; Restore scrubs its own command output either way).
	id, err := h.Svc.StartActionSecrets(h.Jobs, manager.JobRestore, target, manager.ArchiveSecrets(a.Path), func(pw manager.ProgressWriter) error {
		return h.Svc.Restore(in, pw)
	})
	if err != nil {
		redirectWithFlash(w, r, back, "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+id, http.StatusSeeOther)
}
