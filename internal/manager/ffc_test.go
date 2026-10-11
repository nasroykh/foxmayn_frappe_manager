package manager

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestParseFfcVersion(t *testing.T) {
	for in, want := range map[string]string{
		"ffc version 1.19.0 (64b4021, 2026-10-09T11:34:26Z)\n": "1.19.0",
		"ffc version v1.2.3-rc1 (abc, date)":                   "v1.2.3-rc1",
		"bash: ffc: command not found":                         "",
		"":                                                     "",
	} {
		if got := parseFfcVersion(in); got != want {
			t.Errorf("parseFfcVersion(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFfcVersionRunningDevBench(t *testing.T) {
	s, _ := newTestBench(t, state.Bench{Name: "v", Mode: "dev", SiteName: "v.localhost", WebPort: 8000, SocketIOPort: 9000})
	f := fakeexec.Install(t,
		fakeexec.Rule{Match: "ffc --version", Stdout: "ffc version 1.19.0 (64b4021, 2026-10-09T11:34:26Z)\n"},
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
	)
	if got := s.FfcVersion("v"); got != "1.19.0" {
		t.Fatalf("FfcVersion = %q, want 1.19.0", got)
	}
	if !f.Called("exec", "frappe", "ffc --version") {
		t.Errorf("no exec of ffc --version in the frappe container: %v", f.Calls())
	}
}

func TestFfcVersionEmptyWhenNotAnswerable(t *testing.T) {
	s, _ := newTestBench(t, state.Bench{Name: "v", Mode: "dev", SiteName: "v.localhost", WebPort: 8000, SocketIOPort: 9000})
	// Stopped bench: no ps row, so no exec is attempted.
	f := fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: ""})
	if got := s.FfcVersion("v"); got != "" {
		t.Errorf("stopped bench: FfcVersion = %q, want empty", got)
	}
	if f.Called("ffc --version") {
		t.Errorf("exec attempted on a stopped bench: %v", f.Calls())
	}
	// Running, but the exec fails: still empty, no error surfaces.
	fakeexec.Install(t,
		fakeexec.Rule{Match: "ffc --version", Exit: 126},
		fakeexec.Rule{Match: " ps ", Stdout: psRunning},
	)
	if got := s.FfcVersion("v"); got != "" {
		t.Errorf("failing exec: FfcVersion = %q, want empty", got)
	}
	// Prod bench: never probed.
	p, _ := newTestBench(t, state.Bench{Name: "p", Mode: "prod", SiteName: "p.example.com", WebPort: 8000, SocketIOPort: 9000})
	f = fakeexec.Install(t, fakeexec.Rule{Match: " ps ", Stdout: psRunning})
	if got := p.FfcVersion("p"); got != "" || len(f.Calls()) != 0 {
		t.Errorf("prod bench: FfcVersion = %q, calls %v", got, f.Calls())
	}
}

const hostTestConfig = `default_site: hb
sites:
  other:
    url: http://localhost:9999
    api_key: OTHERKEY
    api_secret: OTHERSECRET
  hb:
    url: http://localhost:8000
    api_key: KEY123
    api_secret: SECRET456
`

// hostFfcBench is a dev bench whose in-container ffc config exists on disk.
func hostFfcBench(t *testing.T, config string) (*Service, state.Bench) {
	t.Helper()
	s, b := newTestBench(t, state.Bench{Name: "hb", Mode: "dev", SiteName: "hb.localhost", WebPort: 8010, SocketIOPort: 9010})
	if config != "" {
		dir := filepath.Join(b.Dir, "home", "ffc")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return s, b
}

func withHostFfc(t *testing.T, present bool) {
	t.Helper()
	orig := execx.LookPath
	execx.LookPath = func(string) (string, error) {
		if !present {
			return "", exec.ErrNotFound
		}
		return "/usr/bin/ffc", nil
	}
	t.Cleanup(func() { execx.LookPath = orig })
}

func TestFfcHostAddsSiteWithSecretOnStdin(t *testing.T) {
	s, _ := hostFfcBench(t, hostTestConfig)
	withHostFfc(t, true)
	stdin := filepath.Join(t.TempDir(), "stdin")
	f := fakeexec.Install(t, fakeexec.Rule{Match: "ffc site add", StdinTo: stdin})
	var out BufferProgress
	if err := s.FfcHost("hb", false, &out); err != nil {
		t.Fatalf("FfcHost: %v", err)
	}
	var add string
	for _, c := range f.Calls() {
		if strings.Contains(c, "ffc site add") {
			add = c
		}
	}
	for _, want := range []string{"--no-input", "--name hb", "--url http://localhost:8010", "--api-key KEY123", "--api-secret-stdin"} {
		if !strings.Contains(add, want) {
			t.Errorf("site add missing %q: %s", want, add)
		}
	}
	if strings.Contains(add, "--force") {
		t.Errorf("--force passed without being asked: %s", add)
	}
	for _, c := range f.Calls() {
		if strings.Contains(c, "SECRET456") || strings.Contains(c, "OTHERSECRET") {
			t.Errorf("a secret reached an argv: %s", c)
		}
	}
	got, err := os.ReadFile(stdin)
	if err != nil || strings.TrimSpace(string(got)) != "SECRET456" {
		t.Errorf("stdin = %q (%v), want the bench's secret", got, err)
	}
	if !f.Called("ffc ping --site hb") {
		t.Errorf("host ping not run: %v", f.Calls())
	}
	if text := strings.Join(out.Lines, "\n"); !strings.Contains(text, "ffc mcp --site hb") || !strings.Contains(text, "ffc mcp install --client") {
		t.Errorf("next steps missing:\n%s", text)
	}
}

func TestFfcHostForceOnlyWhenAsked(t *testing.T) {
	s, _ := hostFfcBench(t, hostTestConfig)
	withHostFfc(t, true)
	f := fakeexec.Install(t)
	if err := s.FfcHost("hb", true, &BufferProgress{}); err != nil {
		t.Fatal(err)
	}
	if !f.Called("ffc site add", "--force") {
		t.Errorf("--force not passed: %v", f.Calls())
	}
}

func TestFfcHostExistingSiteNamesForce(t *testing.T) {
	s, _ := hostFfcBench(t, hostTestConfig)
	withHostFfc(t, true)
	fakeexec.Install(t, fakeexec.Rule{Match: "ffc site add", Exit: 2,
		Stdout: `Error: site "hb" already exists: pass --force to replace it`})
	err := s.FfcHost("hb", false, &BufferProgress{})
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("err = %v, want one naming --force", err)
	}
}

func TestFfcHostInitsWhenHostHasNoConfig(t *testing.T) {
	s, _ := hostFfcBench(t, hostTestConfig)
	withHostFfc(t, true)
	f := fakeexec.Install(t, fakeexec.Rule{Match: "ffc site add", Exit: 1,
		Stdout: "no config found at /h/.config/ffc/config.yaml — run 'ffc init' to create one"})
	if err := s.FfcHost("hb", false, &BufferProgress{}); err != nil {
		t.Fatal(err)
	}
	if !f.Called("ffc init", "--api-secret-stdin", "--name hb") {
		t.Errorf("ffc init not run: %v", f.Calls())
	}
}

func TestFfcHostPingFailureIsWarning(t *testing.T) {
	s, _ := hostFfcBench(t, hostTestConfig)
	withHostFfc(t, true)
	fakeexec.Install(t, fakeexec.Rule{Match: "ffc ping", Exit: 1, Stdout: "connection refused"})
	var out BufferProgress
	if err := s.FfcHost("hb", false, &out); err != nil {
		t.Fatalf("ping failure must not be an error: %v", err)
	}
	if !strings.Contains(strings.Join(out.Lines, "\n"), "warning") {
		t.Errorf("no warning printed:\n%s", strings.Join(out.Lines, "\n"))
	}
}

func TestFfcHostSetsUpBenchWhenKeysMissing(t *testing.T) {
	s, b := hostFfcBench(t, "")
	withHostFfc(t, true)
	cfg := filepath.Join(b.Dir, "home", "ffc", "config.yaml")
	// Minting keys is what, in real life, leaves the config in the bind mount.
	f := fakeexec.Install(t, fakeexec.Rule{
		Match:  "generate_keys --args",
		Stdout: `{"api_key": "KEY123", "api_secret": "SECRET456"}` + "\n",
		Write:  map[string]string{cfg: hostTestConfig},
	})
	if err := s.FfcHost("hb", false, &BufferProgress{}); err != nil {
		t.Fatalf("FfcHost: %v", err)
	}
	calls := f.Calls()
	gen, add := -1, -1
	for i, c := range calls {
		if strings.Contains(c, "generate_keys --args") {
			gen = i
		}
		if strings.HasPrefix(c, "ffc site add") {
			add = i
		}
	}
	if gen < 0 || add < 0 || gen > add {
		t.Errorf("want key minting (%d) before the host site add (%d): %v", gen, add, calls)
	}
}

func TestFfcHostRefusals(t *testing.T) {
	t.Run("prod", func(t *testing.T) {
		s, _ := newTestBench(t, state.Bench{Name: "pb", Mode: "prod", SiteName: "pb.example.com", WebPort: 8020, SocketIOPort: 9020})
		withHostFfc(t, true)
		f := fakeexec.Install(t)
		err := s.FfcHost("pb", false, &BufferProgress{})
		if err == nil || !strings.Contains(err.Error(), "dev benches only") {
			t.Fatalf("err = %v", err)
		}
		if len(f.Calls()) != 0 {
			t.Errorf("commands ran for a prod bench: %v", f.Calls())
		}
	})
	t.Run("no host ffc", func(t *testing.T) {
		s, _ := hostFfcBench(t, hostTestConfig)
		withHostFfc(t, false)
		f := fakeexec.Install(t)
		err := s.FfcHost("hb", false, &BufferProgress{})
		if err == nil || !strings.Contains(err.Error(), "install.sh") {
			t.Fatalf("err = %v, want the install command", err)
		}
		if len(f.Calls()) != 0 {
			t.Errorf("commands ran without a host ffc: %v", f.Calls())
		}
	})
	t.Run("unknown bench", func(t *testing.T) {
		s, _ := hostFfcBench(t, hostTestConfig)
		withHostFfc(t, true)
		if err := s.FfcHost("nope", false, &BufferProgress{}); !errors.Is(err, state.ErrNotFound) {
			t.Fatalf("err = %v, want state.ErrNotFound", err)
		}
	})
}
