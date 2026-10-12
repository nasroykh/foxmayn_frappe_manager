package dashboard

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// BenchDoctor runs the read-only health check as a job.
//
// It is a job rather than a synchronous page on purpose: Doctor makes a docker
// round trip, two HTTP probes of up to ten seconds each and a TLS check, and
// takes no context, so a request could hang for tens of seconds with nothing
// to cancel it. As a job the POST returns at once, the findings stream onto
// the job page, the run is audited, and it queues behind (not beside) a
// restart or update that is changing the same bench. A failed check fails the
// job, as it exits 1 in `ffm doctor`.
func (h *Handler) BenchDoctor(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	name := r.PathValue("name")
	if _, err := h.Svc.GetBench(name); err != nil {
		http.NotFound(w, r)
		return
	}
	h.runJob(w, r, manager.JobDoctor, name, benchPath(name), func(pw manager.ProgressWriter) error {
		report, err := h.Svc.Doctor(name)
		if err != nil {
			return err
		}
		pw.Step("Checking " + name)
		for _, c := range report.Checks {
			pw.Println(fmt.Sprintf("[%-4s] %s: %s", c.Status, c.Name, c.Detail))
		}
		if fails := report.Failures(); len(fails) > 0 {
			return fmt.Errorf("%d check(s) failed", len(fails))
		}
		return nil
	})
}

// BenchClone clones a bench into a new one as a job. The job is keyed by the
// new name (so the same clone cannot start twice); Clone itself takes the
// source's bench lock for its backup, so a delete or update of the source in
// the meantime is refused by the manager, not by this key.
func (h *Handler) BenchClone(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) || !h.requirePOST(w, r) {
		return
	}
	source := r.PathValue("name")
	src, err := h.Svc.GetBench(source)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	target := strings.TrimSpace(r.FormValue("target"))
	if err := bench.ValidateName(target); err != nil {
		redirectWithFlash(w, r, benchPath(source), "", err.Error())
		return
	}
	if err := bench.ValidateNewName(target); err != nil {
		redirectWithFlash(w, r, benchPath(source), "", err.Error())
		return
	}
	in := manager.CloneInput{
		Source:  source,
		Target:  target,
		NoFiles: r.FormValue("no_files") == "1",
		Domain:  strings.TrimSpace(r.FormValue("domain")),
		NoSSL:   r.FormValue("no_ssl") == "1",
	}
	// Keyed by the new name, which has no record, so the source's credentials
	// (the clone carries them over) are masked explicitly.
	id, err := h.Svc.StartActionSecrets(h.Jobs, manager.JobClone, target, manager.BenchSecrets(src), func(pw manager.ProgressWriter) error {
		return h.Svc.Clone(in, pw)
	})
	if err != nil {
		redirectWithFlash(w, r, benchPath(source), "", err.Error())
		return
	}
	http.Redirect(w, r, "/admin/jobs/"+id, http.StatusSeeOther)
}
