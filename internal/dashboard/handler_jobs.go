package dashboard

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

// JobEvents streams job progress via Server-Sent Events.
func (h *Handler) JobEvents(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	id := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// sent counts lines already delivered, by absolute number: the job drops
	// its oldest lines past a cap, so an index into Lines would drift.
	sent := 0
	first := true
	for i := 0; i < 3600; i++ {
		job, ok := h.Jobs.GetJob(id)
		if !ok {
			fmt.Fprintf(w, "event: error\ndata: job not found\n\n")
			flusher.Flush()
			return
		}
		lines, next := newJobLines(job, sent)
		sent = next
		payload := map[string]any{
			"status": job.Status,
			"error":  job.Error,
			"lines":  lines,
			// reset: lines is the whole log (replace); otherwise only what is
			// new since the previous event (append).
			"reset": first,
		}
		first = false
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()

		if job.Status.Finished() {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}

// newJobLines returns the lines of job whose absolute number is at least sent,
// and the new sent count. Lines dropped from the front of a capped job before
// they were delivered are skipped.
func newJobLines(job *manager.Job, sent int) ([]string, int) {
	end := job.LinesDropped + len(job.Lines)
	start := sent - job.LinesDropped
	if start < 0 {
		start = 0
	}
	if start > len(job.Lines) {
		start = len(job.Lines)
	}
	return job.Lines[start:], end
}
