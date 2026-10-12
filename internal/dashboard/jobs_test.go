package dashboard

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

func waitJob(t *testing.T, h *Handler, id string) *manager.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if j, ok := h.Jobs.GetJob(id); ok && j.Status.Finished() {
			return j
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never finished", id)
	return nil
}

func jobIDFromLocation(t *testing.T, rr *httptest.ResponseRecorder) string {
	t.Helper()
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303; body %q", rr.Code, rr.Body.String())
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/jobs/") {
		t.Fatalf("Location = %q, want /admin/jobs/<id>", loc)
	}
	return strings.TrimPrefix(loc, "/admin/jobs/")
}

// Every heavy action is a job: the POST returns at once with a redirect to the
// job page instead of holding the request open for the whole operation.
func TestHeavyActionsRunAsJobs(t *testing.T) {
	// docker fails fast, so each job ends at once (a real start would wait
	// for the web server); what is checked is that a job exists and the
	// request did not wait for it.
	h, _ := newTestHandler(t, fakeexec.Rule{Match: "docker", Exit: 1})
	addBench(t, h, "alpha")
	cases := []struct {
		name    string
		handler http.HandlerFunc
		path    string
		typ     manager.JobType
		form    url.Values
	}{
		{"start", h.BenchStart, "/admin/benches/alpha/start", manager.JobStart, nil},
		{"stop", h.BenchStop, "/admin/benches/alpha/stop", manager.JobStop, nil},
		{"restart", h.BenchRestart, "/admin/benches/alpha/restart", manager.JobRestart, nil},
		{"ffc", h.BenchFFC, "/admin/benches/alpha/ffc", manager.JobFFC, nil},
		{"set-proxy", h.BenchSetProxy, "/admin/benches/alpha/set-proxy", manager.JobSetProxy, url.Values{"reset": {"1"}}},
		{"tunnel-off", h.BenchTunnelDisable, "/admin/benches/alpha/tunnel/disable", manager.JobTunnelOff, nil},
		{"clean-logs", h.BenchCleanLogs, "/admin/benches/alpha/clean-logs", manager.JobCleanLogs, url.Values{"dry_run": {"1"}}},
		{"proxy-start", h.ProxyStart, "/admin/proxy/start", manager.JobProxyStart, nil},
		{"proxy-stop", h.ProxyStop, "/admin/proxy/stop", manager.JobProxyStop, nil},
		{"delete", h.BenchDelete, "/admin/benches/alpha/delete", manager.JobDelete, url.Values{"no_backup": {"1"}}},
	}
	for _, c := range cases {
		form := c.form
		if form == nil {
			form = url.Values{}
		}
		name := "alpha"
		if strings.HasPrefix(c.name, "proxy") {
			name = ""
		}
		rr := postForm(h, c.handler, c.path, name, form, true)
		id := jobIDFromLocation(t, rr)
		j := waitJob(t, h, id)
		if j.Type != c.typ {
			t.Errorf("%s: job type = %q, want %q", c.name, j.Type, c.typ)
		}
	}
}

// Without a CSRF token no job is created.
func TestHeavyActionNeedsCSRF(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	rr := postForm(h, h.BenchStop, "/admin/benches/alpha/stop", "alpha", url.Values{}, false)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if n := len(h.Jobs.ListJobs()); n != 0 {
		t.Fatalf("%d jobs created without a token", n)
	}
}

// A second action on a bench with a running job goes back to the bench page
// with the reason; no second job is created.
func TestActionOnBusyBenchIsRefused(t *testing.T) {
	h, _ := newTestHandler(t)
	addBench(t, h, "alpha")
	release := make(chan struct{})
	if _, err := h.Svc.StartAction(h.Jobs, manager.JobRestart, "alpha", func(manager.ProgressWriter) error {
		<-release
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	defer close(release)
	rr := postForm(h, h.BenchStop, "/admin/benches/alpha/stop", "alpha", url.Values{}, true)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rr.Code)
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/admin/benches/alpha") || strings.Contains(loc, "/jobs/") {
		t.Fatalf("Location = %q, want the bench page with an error", loc)
	}
	if n := len(h.Jobs.ListJobs()); n != 1 {
		t.Fatalf("%d jobs, want 1", n)
	}
}

// The compose failure's stderr is in the job (not in the daemon's log), with
// the bench passwords masked.
func TestFailedRebuildStderrIsInTheJobMasked(t *testing.T) {
	h, _ := newTestHandler(t, fakeexec.Rule{
		Match: " build", Exit: 1,
		Stdout: "failed to solve: process did not complete\nuser db-secret-pw-77 denied\n",
	})
	addBench(t, h, "alpha")
	rr := postForm(h, h.BenchRestart, "/admin/benches/alpha/restart", "alpha", url.Values{"rebuild": {"1"}}, true)
	j := waitJob(t, h, jobIDFromLocation(t, rr))
	if j.Status != manager.JobFailed {
		t.Fatalf("status = %v, want failed", j.Status)
	}
	all := strings.Join(j.Lines, "\n")
	if !strings.Contains(all, "failed to solve: process did not complete") {
		t.Errorf("stderr text missing from the job:\n%s", all)
	}
	if strings.Contains(all, "db-secret-pw-77") || !strings.Contains(all, "user *** denied") {
		t.Errorf("secret not masked:\n%s", all)
	}
	// The job page shows it too.
	req := httptest.NewRequest(http.MethodGet, "/admin/jobs/"+j.ID, nil)
	req.SetBasicAuth("u", "secret")
	req.SetPathValue("id", j.ID)
	page := httptest.NewRecorder()
	h.JobDetail(page, req)
	if !strings.Contains(page.Body.String(), "process did not complete") || strings.Contains(page.Body.String(), "db-secret-pw-77") {
		t.Errorf("job page does not show the masked failure")
	}
}

func TestNewJobLines(t *testing.T) {
	job := &manager.Job{Lines: []string{"c", "d", "e"}, LinesDropped: 2} // absolute lines 2..4
	cases := []struct {
		sent     int
		want     string
		wantNext int
	}{
		{0, "c d e", 5}, // 0 and 1 were dropped before delivery
		{2, "c d e", 5},
		{3, "d e", 5},
		{5, "", 5},
		{9, "", 5},
	}
	for _, c := range cases {
		got, next := newJobLines(job, c.sent)
		if strings.Join(got, " ") != c.want || next != c.wantNext {
			t.Errorf("sent=%d: got %q next=%d, want %q next=%d", c.sent, got, next, c.want, c.wantNext)
		}
	}
}

type sseEvent struct {
	Status string   `json:"status"`
	Lines  []string `json:"lines"`
	Reset  bool     `json:"reset"`
}

// The stream sends the whole log once, then only lines that are new.
func TestJobEventsSendOnlyNewLines(t *testing.T) {
	h, _ := newTestHandler(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/jobs/{id}/events", h.JobEvents)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	proceed := make(chan struct{})
	finish := make(chan struct{})
	id, err := h.Svc.StartAction(h.Jobs, manager.JobStart, "alpha", func(pw manager.ProgressWriter) error {
		pw.Println("one")
		pw.Println("two")
		<-proceed
		pw.Println("three")
		<-finish
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Wait until the first two lines are recorded.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if j, _ := h.Jobs.GetJob(id); len(j.Lines) == 2 {
			break
		}
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/admin/jobs/"+id+"/events", nil)
	req.SetBasicAuth("u", "secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	next := func() sseEvent {
		t.Helper()
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				var ev sseEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					t.Fatal(err)
				}
				return ev
			}
		}
		t.Fatal("stream ended")
		return sseEvent{}
	}

	ev := next()
	if !ev.Reset || strings.Join(ev.Lines, ",") != "one,two" {
		t.Fatalf("first event = %+v, want reset with one,two", ev)
	}
	ev = next() // a second later, nothing new
	if ev.Reset || len(ev.Lines) != 0 {
		t.Fatalf("idle event = %+v, want no lines", ev)
	}
	close(proceed)
	for i := 0; i < 5; i++ {
		ev = next()
		if len(ev.Lines) > 0 {
			break
		}
	}
	if ev.Reset || strings.Join(ev.Lines, ",") != "three" {
		t.Fatalf("event = %+v, want only the new line three", ev)
	}
	close(finish)
	for i := 0; i < 5; i++ {
		ev = next()
		if ev.Status == "succeeded" {
			break
		}
	}
	if ev.Status != "succeeded" || len(ev.Lines) != 0 {
		t.Fatalf("final event = %+v, want succeeded with no lines", ev)
	}
}
