package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func renderFor(t *testing.T, d ComposeData) string {
	t.Helper()
	if d.DBType == "" {
		d.DBType = "mariadb"
	}
	out, err := RenderCompose(d)
	if err != nil {
		t.Fatalf("RenderCompose: %v", err)
	}
	return string(out)
}

// Frappe enqueues to "default" unless told otherwise, so a prod bench whose
// workers skip it never runs most scheduled jobs. Confirmed live on v0.8.0.
func TestProdWorkersConsumeEveryQueue(t *testing.T) {
	out := renderFor(t, ComposeData{Mode: "prod", Name: "x", Domain: "x.example.com"})
	consumed := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		_, after, ok := strings.Cut(line, "bench worker --queue ")
		if !ok {
			continue
		}
		queues, _, _ := strings.Cut(after, `"`)
		for _, q := range strings.Split(queues, ",") {
			consumed[q] = true
		}
	}
	for _, q := range []string{"short", "default", "long"} {
		if !consumed[q] {
			t.Errorf("no prod worker consumes the %q queue", q)
		}
	}
}

func TestPublishHost(t *testing.T) {
	cases := []struct {
		mode, host string
		want       []string
	}{
		{"prod", "127.0.0.1", []string{`"127.0.0.1:8000:8000"`, `"127.0.0.1:9000:9000"`}},
		{"prod", "", []string{`"8000:8000"`, `"9000:9000"`}},
		{"dev", "127.0.0.1", []string{`"127.0.0.1:8000-8005:8000-8005"`, `"127.0.0.1:9000-9005:9000-9005"`}},
		{"dev", "", []string{`"8000-8005:8000-8005"`, `"9000-9005:9000-9005"`}},
	}
	for _, c := range cases {
		out := renderFor(t, ComposeData{
			Mode: c.mode, Name: "x", Domain: "x.example.com", PublishHost: c.host,
			WebPort: 8000, WebPortEnd: 8005, SocketIOPort: 9000, SocketIOPortEnd: 9005,
		})
		for _, w := range c.want {
			if !strings.Contains(out, "- "+w) {
				t.Errorf("%s host=%q: missing port line %s", c.mode, c.host, w)
			}
		}
	}
}

// The agent socket must not make compose fail when SSH_AUTH_SOCK is unset
// (cron, sudo, the dashboard daemon).
func TestSSHAgentMountHasDefault(t *testing.T) {
	out := renderFor(t, ComposeData{Mode: "dev", Name: "x", ForwardSSHAgent: true})
	if !strings.Contains(out, "${SSH_AUTH_SOCK:-/dev/null}:/ssh-agent") {
		t.Fatalf("agent mount has no default:\n%s", out)
	}
	if strings.Contains(renderFor(t, ComposeData{Mode: "dev", Name: "x"}), "ssh-agent") {
		t.Fatal("agent forwarded although ForwardSSHAgent is false")
	}
}

func TestWriteComposeIsPrivate(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCompose(dir, ComposeData{Mode: "prod", Name: "x", DBType: "mariadb"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

// Compose only auto-loads an override file when no -f is given; ffm always
// passes -f, so the runner must add it, or hand edits are silently ignored.
func TestRunnerAddsOverrideFile(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner("x", dir, false)
	if strings.Contains(strings.Join(r.baseArgs(), " "), OverrideFile) {
		t.Fatal("override file passed although it does not exist")
	}
	if err := os.WriteFile(filepath.Join(dir, OverrideFile), []byte("services: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args := r.baseArgs()
	if args[len(args)-1] != filepath.Join(dir, OverrideFile) || args[len(args)-2] != "-f" {
		t.Fatalf("override file not passed last: %q", args)
	}
}

func TestWriteComposeKeepsPreviousVersion(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(dest, []byte("hand edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteCompose(dir, ComposeData{Mode: "dev", Name: "x", DBType: "mariadb"}); err != nil {
		t.Fatal(err)
	}
	bak, err := os.ReadFile(dest + ".bak")
	if err != nil || string(bak) != "hand edited\n" {
		t.Fatalf(".bak = %q, %v; want the previous content", bak, err)
	}
	if st, _ := os.Stat(dest + ".bak"); st.Mode().Perm() != 0o600 {
		t.Fatalf(".bak mode = %v, want 0600", st.Mode().Perm())
	}
}
