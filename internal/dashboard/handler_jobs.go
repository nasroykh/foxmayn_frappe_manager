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

	lastLines := 0
	for i := 0; i < 3600; i++ {
		job, ok := h.Jobs.GetJob(id)
		if !ok {
			fmt.Fprintf(w, "event: error\ndata: job not found\n\n")
			flusher.Flush()
			return
		}
		payload := map[string]any{
			"status": job.Status,
			"error":  job.Error,
			"lines":  job.Lines,
		}
		b, _ := json.Marshal(payload)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()

		if job.Status == manager.JobSucceeded || job.Status == manager.JobFailed {
			return
		}
		if len(job.Lines) > lastLines {
			lastLines = len(job.Lines)
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Second):
		}
	}
}
