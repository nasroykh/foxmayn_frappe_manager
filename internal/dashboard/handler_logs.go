package dashboard

import (
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// LogsStream streams docker compose logs via SSE.
func (h *Handler) LogsStream(w http.ResponseWriter, r *http.Request) {
	if !h.auth(w, r) {
		return
	}
	name := r.PathValue("name")
	b, err := h.Svc.GetBench(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	service := r.URL.Query().Get("service")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	args := []string{"compose", "logs", "-f", "--tail", "100"}
	if service != "" {
		args = append(args, service)
	}
	cmd := exec.Command("docker", args...)
	cmd.Dir = b.Dir
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer func() { _ = cmd.Process.Kill() }()

	buf := make([]byte, 4096)
	go func() {
		io.CopyBuffer(io.Discard, stderr, buf)
	}()

	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			lines := string(buf[:n])
			for _, line := range splitLines(lines) {
				fmt.Fprintf(w, "data: %s\n\n", line)
				flusher.Flush()
			}
		}
		if readErr != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		default:
		}
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// LogsTail returns the last N lines (non-streaming helper).
func (h *Handler) LogsTail(name, service string, tail int) (string, error) {
	b, err := h.Svc.GetBench(name)
	if err != nil {
		return "", err
	}
	runner := bench.NewRunner(b.Name, b.Dir, false)
	_ = runner
	cmd := exec.Command("docker", "compose", "logs", "--tail", fmt.Sprintf("%d", tail))
	if service != "" {
		cmd.Args = append(cmd.Args, service)
	}
	cmd.Dir = filepath.Clean(b.Dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
