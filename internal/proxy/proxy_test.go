package proxy

import (
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

// inspects makes successive inspect calls answer with infos; the last one
// repeats.
func inspects(t *testing.T, infos ...containerInfo) {
	t.Helper()
	orig := inspectContainer
	n := 0
	inspectContainer = func() containerInfo {
		i := infos[min(n, len(infos)-1)]
		n++
		return i
	}
	t.Cleanup(func() { inspectContainer = orig })
}

func installDocker(t *testing.T) *fakeexec.Fake {
	t.Helper()
	return fakeexec.Install(t,
		fakeexec.Rule{Match: "network inspect", Stdout: NetworkName + "\n"},
	)
}

func healthyInfo() containerInfo {
	return containerInfo{Status: "running", Image: Image, Attached: true, Published80: true}
}

func firstCall(f *fakeexec.Fake, prefix string) string {
	for _, c := range f.Calls() {
		if strings.HasPrefix(c, prefix) {
			return c
		}
	}
	return ""
}

func TestStartRunningHealthyIsLeftAlone(t *testing.T) {
	f := installDocker(t)
	inspects(t, healthyInfo())
	err := Start()
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v", err)
	}
	if f.Called("docker rm") || f.Called("docker run") {
		t.Errorf("a healthy proxy must not be touched: %v", f.Calls())
	}
}

func TestStartRecreatesARunningProxyWithoutNetwork(t *testing.T) {
	f := installDocker(t)
	broken := healthyInfo()
	broken.Attached = false
	broken.Published80 = false
	inspects(t, broken)
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	run := firstCall(f, "docker run")
	if !f.Called("docker rm -f "+ContainerName) || run == "" {
		t.Fatalf("calls: %v", f.Calls())
	}
	if !strings.Contains(run, "-p 0.0.0.0:80:80") || strings.Contains(run, ":443") || !strings.Contains(run, Image) {
		t.Errorf("run: %s", run)
	}
}

func TestStartUpdatesAnOutdatedStoppedProxy(t *testing.T) {
	f := installDocker(t)
	inspects(t, containerInfo{Status: "exited", Image: "traefik:3"})
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	if f.Called("docker start") {
		t.Errorf("an outdated container must not be started: %v", f.Calls())
	}
	if !f.Called("docker rm -f "+ContainerName) || !strings.Contains(firstCall(f, "docker run"), " "+Image+" ") {
		t.Errorf("calls: %v", f.Calls())
	}
}

func TestStartRestartsAStoppedCurrentProxy(t *testing.T) {
	f := installDocker(t)
	stopped := healthyInfo()
	stopped.Status = "exited"
	inspects(t, stopped, healthyInfo())
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	if !f.Called("docker start "+ContainerName) || f.Called("docker rm") || f.Called("docker run") {
		t.Errorf("calls: %v", f.Calls())
	}
}

func TestStartRecreatesWhenARestartedProxyDoesNotServe(t *testing.T) {
	f := installDocker(t)
	stopped := healthyInfo()
	stopped.Status = "exited"
	still := healthyInfo()
	still.Attached = false
	inspects(t, stopped, still)
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	start, rm, run := -1, -1, -1
	for i, c := range f.Calls() {
		switch {
		case strings.HasPrefix(c, "docker start"):
			start = i
		case strings.HasPrefix(c, "docker rm -f"):
			rm = i
		case strings.HasPrefix(c, "docker run"):
			run = i
		}
	}
	if start < 0 || rm < start || run < rm {
		t.Errorf("want start, rm, run in order: %v", f.Calls())
	}
}

func TestStartKeepsHTTPSAndTheACMEEmail(t *testing.T) {
	f := installDocker(t)
	broken := healthyInfo()
	broken.Published80 = false
	broken.HTTPS = true
	broken.AcmeEmail = "ops@example.com"
	inspects(t, broken)
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	run := firstCall(f, "docker run")
	for _, want := range []string{"0.0.0.0:443:443", LetsEncryptVolume, acmeEmailFlag + "ops@example.com"} {
		if !strings.Contains(run, want) {
			t.Errorf("run lacks %q: %s", want, run)
		}
	}
}

func TestStartRefusesToDowngradeHTTPS(t *testing.T) {
	f := installDocker(t)
	broken := healthyInfo()
	broken.Attached = false
	broken.HTTPS = true
	inspects(t, broken)
	err := Start()
	if err == nil || !strings.Contains(err.Error(), "docker rm -f "+ContainerName) || !strings.Contains(err.Error(), "--acme-email") {
		t.Fatalf("err = %v", err)
	}
	if f.Called("docker rm") || f.Called("docker run") {
		t.Errorf("nothing may be removed: %v", f.Calls())
	}
}

func TestStatusReportsBrokenAndOutdated(t *testing.T) {
	inspects(t, healthyInfo())
	if got := Status(); got != "running" {
		t.Errorf("healthy: %q", got)
	}
	broken := healthyInfo()
	broken.Attached = false
	inspects(t, broken)
	if got := Status(); !strings.HasPrefix(got, "running but broken: it is not attached to the ffm-proxy network") ||
		!strings.Contains(got, "'ffm proxy start' to recreate it") {
		t.Errorf("broken: %q", got)
	}
	old := healthyInfo()
	old.Image = "traefik:3"
	inspects(t, old)
	if got := Status(); !strings.Contains(got, "(image traefik:3, ffm pins "+Image+": 'ffm proxy stop && ffm proxy start' updates it)") {
		t.Errorf("outdated: %q", got)
	}
	if ok, why := Healthy(); !ok || why != "" {
		t.Errorf("an old image alone is still healthy: %v %q", ok, why)
	}
}

// The user's broken container: running, HostConfig right, no endpoint, no ports.
func TestInspectParsesTheBrokenContainer(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect " + ContainerName, Stdout: `{"status":"running","image":"traefik:3",` +
		`"networks":{},"ports":{},"bindings":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}],` +
		`"443/tcp":[{"HostIp":"0.0.0.0","HostPort":"443"}]},` +
		`"args":["--api.dashboard=true","--certificatesresolvers.letsencrypt.acme.email=a@b.co"]}` + "\n"})
	i := realInspect()
	if i.Status != "running" || i.Image != "traefik:3" || i.Attached || i.Published80 || !i.HTTPS || i.AcmeEmail != "a@b.co" {
		t.Errorf("%+v", i)
	}
	if ok, why := Healthy(); ok || !strings.Contains(why, "not attached") {
		t.Errorf("Healthy = %v %q", ok, why)
	}

	fakeexec.Install(t, fakeexec.Rule{Match: "inspect " + ContainerName, Stdout: `{"status":"running","image":"traefik:v3.7",` +
		`"networks":{"ffm-proxy":{}},"ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}],"8080/tcp":null},` +
		`"bindings":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}]},"args":[]}` + "\n"})
	i = realInspect()
	if !i.Attached || !i.Published80 || i.HTTPS || i.problem() != "" {
		t.Errorf("%+v", i)
	}
}

func TestMissingContainerIsNotInstalled(t *testing.T) {
	fakeexec.Install(t, fakeexec.Rule{Match: "inspect " + ContainerName, Exit: 1})
	if got := Status(); !strings.HasPrefix(got, "not installed") {
		t.Errorf("Status = %q", got)
	}
}
