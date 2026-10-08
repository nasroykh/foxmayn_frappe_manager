package manager

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/notify"
)

func TestRunResultNotifications(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	var mu sync.Mutex
	var got []notify.Event
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.URL.Path)
		var e notify.Event
		if json.Unmarshal(b, &e) == nil {
			got = append(got, e)
		}
	}))
	defer srv.Close()
	old := notify.Default
	notify.Default = &notify.Client{HTTP: srv.Client()}
	defer func() { notify.Default = old }()

	if err := AddNotifier(notify.Notifier{Name: "hook", Type: notify.TypeWebhook, URL: srv.URL + "/hook"}, false); err != nil {
		t.Fatal(err)
	}
	if err := AddNotifier(notify.Notifier{Name: "hc", Type: notify.TypeHealthchecks, URL: srv.URL + "/hc"}, false); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	notifyRunStart(&log)
	notifyRunResult("kb", RunDueResult{Result: RunOK, Archive: "kb_x.auto.ffm.tar.age"}, &log)
	notifyRunResult("kb", RunDueResult{Result: RunFailed, Err: errors.New("target r2: connection refused")}, &log)
	notifyRunResult("kb", RunDueResult{Result: RunSkippedStopped}, &log)

	mu.Lock()
	defer mu.Unlock()
	// The webhook (failures only) got exactly the failure.
	if len(got) != 1 || got[0].OK || !strings.Contains(got[0].Message, "connection refused") || got[0].Bench != "kb" {
		t.Errorf("webhook events: %+v", got)
	}
	// healthchecks: start, success, fail, success (a stopped bench is not a failure).
	want := []string{"/hc/start", "/hc", "/hc/fail", "/hc"}
	var hc []string
	for _, p := range paths {
		if strings.HasPrefix(p, "/hc") {
			hc = append(hc, p)
		}
	}
	if strings.Join(hc, " ") != strings.Join(want, " ") {
		t.Errorf("healthchecks pings %v, want %v", hc, want)
	}
	if log.Len() != 0 {
		t.Errorf("warnings: %s", log.String())
	}
}
