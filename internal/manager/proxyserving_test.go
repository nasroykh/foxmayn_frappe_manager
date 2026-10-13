package manager

import (
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func proxyInspect(status string, attached bool) string {
	nets := `{}`
	if attached {
		nets = `{"` + proxy.NetworkName + `":{}}`
	}
	return `{"status":"` + status + `","image":"` + proxy.Image + `","networks":` + nets +
		`,"ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}]},"bindings":{},"args":[]}` + "\n"
}

func TestBrokenProxyIsNotServing(t *testing.T) {
	dev := state.Bench{Name: "a", Mode: "dev", WebPort: 8010, SiteName: "a.localhost"}

	// Healthy: host URL, no warning, view and SiteURL agree.
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect", Stdout: proxyInspect("running", true)})
	pw := &BufferProgress{}
	if !proxyServingWarn(pw) || len(pw.Lines) != 0 {
		t.Errorf("healthy: lines %v", pw.Lines)
	}
	if got := viewSiteURL(dev, proxy.Serving); got != "http://a.localhost" {
		t.Errorf("healthy view URL = %q", got)
	}

	// Running but detached: direct port, exactly one warning naming the fix.
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect", Stdout: proxyInspect("running", false)})
	pw = &BufferProgress{}
	if proxyServingWarn(pw) {
		t.Error("broken proxy reported as serving")
	}
	if len(pw.Lines) != 1 || !strings.Contains(pw.Lines[0], "ffm proxy start") {
		t.Errorf("want one warning naming 'ffm proxy start', got %v", pw.Lines)
	}
	if got := viewSiteURL(dev, proxy.Serving); got != "http://localhost:8010" {
		t.Errorf("broken view URL = %q", got)
	}
	if got := siteURL(dev, proxy.Serving()); got != "http://localhost:8010" {
		t.Errorf("broken siteURL = %q", got)
	}

	// Stopped: no warning (the caller's own hint covers it).
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect", Stdout: proxyInspect("exited", false)})
	pw = &BufferProgress{}
	if proxyServingWarn(pw) || len(pw.Lines) != 0 {
		t.Errorf("stopped: lines %v", pw.Lines)
	}
}

func TestListViewUsesServing(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect", Stdout: proxyInspect("running", false)})
	s, b := newTestBench(t, state.Bench{Name: "lv", Mode: "dev", SiteName: "lv.localhost", WebPort: 8030, SocketIOPort: 9030})
	if got := s.benchToView(b).URL; got != "http://localhost:8030" {
		t.Errorf("view URL with broken proxy = %q", got)
	}
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect", Stdout: proxyInspect("running", true)})
	if got := s.benchToView(b).URL; got != "http://lv.localhost" {
		t.Errorf("view URL with healthy proxy = %q", got)
	}
}
