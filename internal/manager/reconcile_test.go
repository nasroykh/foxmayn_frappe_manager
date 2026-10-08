package manager

import (
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestCheckReconcile(t *testing.T) {
	yes := true
	t.Setenv("SSH_AUTH_SOCK", "")
	cases := []struct {
		name    string
		b       state.Bench
		in      ReconcileInput
		wantErr string
	}{
		{"lan with default password", state.Bench{Name: "a", AdminPassword: "admin"}, ReconcileInput{Bind: state.BindLAN}, "default admin password"},
		{"lan with real password", state.Bench{Name: "a", AdminPassword: "s3cret-x"}, ReconcileInput{Bind: state.BindLAN}, ""},
		{"dev aliases on loopback", state.Bench{Name: "a", Bind: state.BindLoopback, DomainAliases: []string{"erp.internal"}}, ReconcileInput{}, "domain aliases"},
		{"legacy dev aliases stay on LAN", state.Bench{Name: "a", DomainAliases: []string{"erp.internal"}}, ReconcileInput{}, ""},
		{"ssh agent without socket", state.Bench{Name: "a"}, ReconcileInput{SSHAgent: &yes}, "SSH_AUTH_SOCK"},
	}
	for _, c := range cases {
		err := checkReconcile(c.b, c.in)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: error %v, want one containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestLineDiff(t *testing.T) {
	got := lineDiff("a\nb\nc", "a\nB\nc")
	want := []string{"- b", "+ B"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lineDiff = %q, want %q", got, want)
	}
	if d := lineDiff("x\ny", "x\ny"); len(d) != 0 {
		t.Fatalf("identical input produced %q", d)
	}
}

func TestEffectiveBindKeepsLegacyBehaviour(t *testing.T) {
	if got := effectiveBind(state.Bench{Mode: "dev"}); got != state.BindLAN {
		t.Errorf("legacy dev = %q, want lan", got)
	}
	if got := effectiveBind(state.Bench{Mode: "prod"}); got != state.BindLoopback {
		t.Errorf("legacy prod = %q, want loopback", got)
	}
}
