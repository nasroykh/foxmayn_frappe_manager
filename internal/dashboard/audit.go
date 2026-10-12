package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// auditEntry is one line of the dashboard audit log. Only fixed fields are
// recorded: form values such as passwords and tokens never are. The exec
// command is recorded verbatim, on purpose.
type auditEntry struct {
	Time    time.Time `json:"time"`
	IP      string    `json:"ip"`
	Action  string    `json:"action"`
	Bench   string    `json:"bench,omitempty"`
	Service string    `json:"service,omitempty"`
	Command string    `json:"command,omitempty"`
	Field   string    `json:"field,omitempty"`
	Status  int       `json:"status,omitempty"`
	Result  string    `json:"result"`
}

// auditLog appends JSON lines to config.DashboardAuditFile (0600). The path is
// resolved per write so a changed FFM_CONFIG_DIR is honoured.
type auditLog struct {
	mu sync.Mutex
}

func (a *auditLog) write(e auditEntry) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := config.EnsureDataDir(); err != nil {
		return err
	}
	path := config.DashboardAuditFile()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// OpenFile applies the mode only when it creates the file.
	_ = os.Chmod(path, 0o600)
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

type auditCtxKey struct{}

// auditNote lets a handler attach the outcome to the request's audit line.
func auditNote(r *http.Request, result string) {
	if e, ok := r.Context().Value(auditCtxKey{}).(*auditEntry); ok {
		e.Result = result
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Audited wraps a POST route so every request, accepted or refused, leaves an
// audit line. It runs after the handler and reads only form values the handler
// already parsed, so an unauthenticated request never has its body read for
// the audit's sake.
func (h *Handler) Audited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entry := &auditEntry{}
		sw := &statusWriter{ResponseWriter: w}
		next(sw, r.WithContext(context.WithValue(r.Context(), auditCtxKey{}, entry)))

		action := r.Pattern
		if i := strings.Index(action, " "); i >= 0 {
			action = action[i+1:]
		}
		if action == "" {
			action = r.URL.Path
		}
		entry.Time = time.Now().UTC()
		entry.IP = clientIP(r)
		entry.Action = action
		entry.Bench = r.PathValue("name")
		entry.Status = sw.status
		if r.Form != nil {
			if strings.HasSuffix(action, "/exec") {
				entry.Service = r.Form.Get("service")
				entry.Command = r.Form.Get("command")
			}
			if strings.HasSuffix(action, "/reveal") {
				entry.Field = r.Form.Get("field")
			}
		}
		if entry.Result == "" {
			switch {
			case sw.status == http.StatusUnauthorized, sw.status == http.StatusTooManyRequests,
				sw.status == http.StatusForbidden:
				entry.Result = "denied"
			case sw.status >= 400:
				entry.Result = "error"
			default:
				entry.Result = "ok"
			}
		}
		if err := h.audit.write(*entry); err != nil {
			h.Logger.Error("dashboard: audit write failed", "err", err)
		}
	}
}
