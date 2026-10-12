package proxy

import (
	"errors"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }

const (
	rootlessInfo = `["name=seccomp,profile=builtin","name=rootless","name=cgroupns"]`
	rootfulInfo  = `["name=seccomp,profile=builtin","name=cgroupns"]`
)

// host fakes the environment, the unprivileged-port sysctl and docker.
func host(t *testing.T, env map[string]string, portStart string, rules ...fakeexec.Rule) *fakeexec.Fake {
	t.Helper()
	oldGetenv, oldRead := getenv, readFile
	t.Cleanup(func() { getenv, readFile = oldGetenv, oldRead })
	getenv = func(k string) string { return env[k] }
	readFile = func(string) ([]byte, error) {
		if portStart == "" {
			return nil, errors.New("no such file")
		}
		return []byte(portStart + "\n"), nil
	}
	return fakeexec.Install(t, rules...)
}

func TestDockerSocket(t *testing.T) {
	ctx := func(host string) fakeexec.Rule {
		return fakeexec.Rule{Match: "docker context inspect", Stdout: host + "\n"}
	}
	rootless := fakeexec.Rule{Match: "docker info", Stdout: rootlessInfo}
	rootful := fakeexec.Rule{Match: "docker info", Stdout: rootfulInfo}
	cases := []struct {
		name    string
		env     map[string]string
		rules   []fakeexec.Rule
		want    string
		wantErr string
	}{
		{"rootful ignores DOCKER_HOST", map[string]string{"DOCKER_HOST": "unix:///run/user/1000/docker.sock"},
			[]fakeexec.Rule{rootful}, "/var/run/docker.sock", ""},
		{"rootful with a Desktop context", nil,
			[]fakeexec.Rule{rootful, ctx("unix:///home/u/.docker/desktop/docker.sock")}, "/var/run/docker.sock", ""},
		{"rootless DOCKER_HOST wins over the context", map[string]string{"DOCKER_HOST": "unix:///run/user/1000/docker.sock"},
			[]fakeexec.Rule{rootless, ctx("unix:///elsewhere.sock")}, "/run/user/1000/docker.sock", ""},
		{"rootless context", nil,
			[]fakeexec.Rule{rootless, ctx("unix:///run/user/1000/docker.sock")}, "/run/user/1000/docker.sock", ""},
		{"rootless, nothing set", nil, []fakeexec.Rule{rootless}, "/var/run/docker.sock", ""},
		{"rootless, tcp DOCKER_HOST", map[string]string{"DOCKER_HOST": "tcp://10.0.0.5:2375"},
			[]fakeexec.Rule{rootless}, "", "unix socket"},
		{"docker info fails", nil, []fakeexec.Rule{{Match: "docker info", Exit: 1}}, "/var/run/docker.sock", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host(t, tc.env, "", tc.rules...)
			got, err := dockerSocket()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("dockerSocket() = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

func TestUnprivilegedPortPreflight(t *testing.T) {
	rootless := fakeexec.Rule{Match: "docker info", Stdout: rootlessInfo}
	rootful := fakeexec.Rule{Match: "docker info", Stdout: rootfulInfo}
	cases := []struct {
		name    string
		start   string
		rule    fakeexec.Rule
		ports   []int
		wantErr bool
	}{
		{"rootful never checks", "1024", rootful, []int{80}, false},
		{"rootless, default sysctl", "1024", rootless, []int{80}, true},
		{"rootless, lowered to 80", "80", rootless, []int{80}, false},
		{"rootless, 443 needs no more than 443", "443", rootless, []int{443}, false},
		{"rootless HTTPS, sysctl 100", "100", rootless, []int{80, 443}, true},
		{"rootless, sysctl unreadable", "", rootless, []int{80}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host(t, nil, tc.start, tc.rule)
			err := checkUnprivilegedPorts(tc.ports...)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "sysctl -w net.ipv4.ip_unprivileged_port_start=") {
				t.Errorf("no fix in the message: %v", err)
			}
		})
	}
}

func TestCreateContainerMountsTheRightSocket(t *testing.T) {
	fake := host(t, map[string]string{"DOCKER_HOST": "unix:///run/user/1000/docker.sock"}, "80",
		fakeexec.Rule{Match: "docker info", Stdout: rootlessInfo})
	if err := createContainer(); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("docker run", "-v /run/user/1000/docker.sock:/var/run/docker.sock:ro") {
		t.Errorf("calls: %v", fake.Calls())
	}

	fake = host(t, nil, "", fakeexec.Rule{Match: "docker info", Stdout: rootfulInfo})
	if err := createContainerHTTPS("me@example.com"); err != nil {
		t.Fatal(err)
	}
	if !fake.Called("docker run", "-v /var/run/docker.sock:/var/run/docker.sock:ro", "-v ffm-letsencrypt:/letsencrypt") {
		t.Errorf("calls: %v", fake.Calls())
	}
}

func TestCreateContainerStopsBeforeDockerRunOnPrivilegedPorts(t *testing.T) {
	fake := host(t, nil, "1024", fakeexec.Rule{Match: "docker info", Stdout: rootlessInfo})
	err := createContainer()
	if err == nil || !strings.Contains(err.Error(), "ip_unprivileged_port_start") {
		t.Fatalf("err = %v", err)
	}
	if fake.Called("docker run") {
		t.Error("docker run was attempted")
	}
	fake = host(t, nil, "1024", fakeexec.Rule{Match: "docker info", Stdout: rootlessInfo})
	if err := createContainerHTTPS("a@b.c"); err == nil || fake.Called("docker run") {
		t.Fatalf("HTTPS: err = %v, calls %v", err, fake.Calls())
	}
}

func TestPortHint(t *testing.T) {
	for msg, want := range map[string]string{
		"listen tcp4 0.0.0.0:80: bind: address already in use":     "in use",
		"cannot expose privileged port 80":                         "privileged",
		"listen tcp 0.0.0.0:80: bind: permission denied":           "privileged",
		"Error response from daemon: no such image":                "",
		"driver failed programming external connectivity: bind: x": "in use",
	} {
		if got := portHint(msg); got != want {
			t.Errorf("portHint(%q) = %q, want %q", msg, got, want)
		}
	}
}
