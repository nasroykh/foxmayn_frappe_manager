package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestCreateAgentRefusals(t *testing.T) {
	fake := fakeexec.Install(t)
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	t.Setenv("FFM_BENCHES_DIR", t.TempDir())
	s := New(false)
	base := CreateInput{Name: "ag", Mode: "dev", AdminPassword: "admin", DBPassword: "ffm123456", Agent: true}
	for name, in := range map[string]CreateInput{
		"prod":      {Name: "ag", Mode: "prod", Domain: "x.example.com", AdminPassword: "Str0ng-pass", DBPassword: "ffm123456", Agent: true},
		"lan":       func() CreateInput { c := base; c.Bind = state.BindLAN; c.AdminPassword = "Str0ng-pass"; return c }(),
		"ssh-agent": func() CreateInput { c := base; c.SSHAgent = true; return c }(),
	} {
		err := s.Create(in, DiscardProgress{})
		if err == nil || !strings.Contains(err.Error(), "agent") {
			t.Errorf("%s: err = %v, want an --agent refusal", name, err)
		}
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("work started before the refusal: %v", fake.Calls())
	}
}

func TestSetAgentOnAndOff(t *testing.T) {
	fake := fakeexec.Install(t,
		fakeexec.Rule{Match: "add_roles", Stdout: `FFM_KEYS={"api_key": "agentkey", "api_secret": "agent-s3cret"}` + "\n"},
		fakeexec.Rule{Match: "generate_keys --args", Stdout: `{"api_key": "adminkey", "api_secret": "admin-s3cret"}` + "\n"},
	)
	s, b := newTestBench(t, state.Bench{Name: "ag", Mode: "dev", SiteName: "ag.localhost", WebPort: 8000, SocketIOPort: 9000,
		Bind: state.BindLoopback, AdminPassword: "admin", FrappeBranch: "version-16"})
	os.MkdirAll(filepath.Join(b.Dir, "workspace", "frappe-bench"), 0o755)

	if err := s.SetAgent(SetAgentInput{Name: "ag", On: true, ReadOnly: true}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	rec, _ := s.GetBench("ag")
	if !rec.Agent || !rec.AgentReadOnly || rec.AdminPassword == "admin" || len(rec.AdminPassword) != 24 {
		t.Errorf("record after on: agent=%v ro=%v admin=%q", rec.Agent, rec.AgentReadOnly, rec.AdminPassword)
	}
	if !fake.Called(`set-admin-password "$p"`) || !fake.Called("'agent@ag.localhost'") ||
		!fake.Called("ffc site add --force --no-input --name 'ag'", "ffc init --no-input --name 'ag'", "--api-key 'agentkey' --api-secret-stdin") {
		t.Errorf("calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	for _, c := range fake.Calls() {
		if strings.Contains(c, "s3cret") || strings.Contains(c, rec.AdminPassword) {
			t.Errorf("a secret reached a command line: %s", c)
		}
	}
	fb := filepath.Join(b.Dir, "workspace", "frappe-bench")
	mcp, _ := os.ReadFile(filepath.Join(fb, ".mcp.json"))
	if !strings.Contains(string(mcp), `"--read-only"`) {
		t.Errorf(".mcp.json not read-only: %s", mcp)
	}
	md, _ := os.ReadFile(filepath.Join(fb, "AGENTS.md"))
	if !strings.HasPrefix(string(md), agentsMDMarker) || !strings.Contains(string(md), "agent@ag.localhost") || strings.Contains(string(md), rec.AdminPassword) {
		t.Errorf("AGENTS.md:\n%s", md)
	}
	if c, _ := os.ReadFile(filepath.Join(fb, "CLAUDE.md")); string(c) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q", c)
	}

	if err := s.SetAgent(SetAgentInput{Name: "ag", On: false}, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	rec, _ = s.GetBench("ag")
	if rec.Agent || rec.AgentReadOnly {
		t.Error("agent still on after off")
	}
	if !fake.Called(`"enabled", 0`) || !fake.Called("--api-key 'adminkey'") {
		t.Errorf("off did not disable the agent user or go back to Administrator; calls:\n%s", strings.Join(fake.Calls(), "\n"))
	}
	if mcp, _ := os.ReadFile(filepath.Join(fb, ".mcp.json")); strings.Contains(string(mcp), "--read-only") {
		t.Error(".mcp.json still read-only after off")
	}
}

func TestSetAgentRefusesExposedBench(t *testing.T) {
	fake := fakeexec.Install(t)
	s, _ := newTestBench(t, state.Bench{Name: "lan", Mode: "dev", SiteName: "lan.localhost", WebPort: 8000, SocketIOPort: 9000, Bind: state.BindLAN})
	if err := s.SetAgent(SetAgentInput{Name: "lan", On: true}, DiscardProgress{}); err == nil {
		t.Error("a LAN bench was made agent-ready")
	}
	if err := s.AddBench(state.Bench{Name: "ssh", Mode: "dev", SiteName: "ssh.localhost", WebPort: 8010, SocketIOPort: 9010, Bind: state.BindLoopback, SSHAgent: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgent(SetAgentInput{Name: "ssh", On: true}, DiscardProgress{}); err == nil {
		t.Error("a bench forwarding the SSH agent was made agent-ready")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("work started before the refusal: %v", fake.Calls())
	}
}

func TestAgentsMDLeavesUserFilesAlone(t *testing.T) {
	b := state.Bench{Name: "u", Mode: "dev", SiteName: "u.localhost", Dir: t.TempDir()}
	fb := filepath.Join(b.Dir, "workspace", "frappe-bench")
	os.MkdirAll(fb, 0o755)
	os.WriteFile(filepath.Join(fb, "AGENTS.md"), []byte("# mine\n"), 0o644)
	os.WriteFile(filepath.Join(fb, "CLAUDE.md"), []byte("# mine too\n"), 0o644)
	if err := writeAgentsMD(b); err != nil {
		t.Fatal(err)
	}
	if md, _ := os.ReadFile(filepath.Join(fb, "AGENTS.md")); string(md) != "# mine\n" {
		t.Error("user's AGENTS.md overwritten")
	}
	if c, _ := os.ReadFile(filepath.Join(fb, "CLAUDE.md")); string(c) != "# mine too\n" {
		t.Error("user's CLAUDE.md overwritten")
	}
}
