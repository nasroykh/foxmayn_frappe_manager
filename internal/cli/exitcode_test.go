package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{errors.New("boom"), ExitFailure},
		{fmt.Errorf("%w: x", state.ErrNotFound), ExitNotFound},
		{fmt.Errorf("%w — create one", state.ErrNoBenches), ExitNotFound},
		{fmt.Errorf("wrap: %w", manager.ErrBenchBusy), ExitBusy},
		{fmt.Errorf("wrap: %w", manager.ErrBenchStopped), ExitWrongState},
		{mustNotPrompt("a bench name", "pass it"), ExitUsage},
		{errors.New(`unknown command "x" for "ffm"`), ExitUsage},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// run executes the real command tree and returns stdout and the exit code.
func run(t *testing.T, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", dir+"/config")
	t.Setenv("FFM_BENCHES_DIR", dir+"/benches")
	t.Setenv("FFM_BACKUPS_DIR", dir+"/backups")
	t.Setenv("FFM_NO_UPDATE_CHECK", "1")
	t.Setenv("FFM_NON_INTERACTIVE", "1")

	r, w, _ := os.Pipe()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	root := NewRootCmd()
	markUsageErrors(root)
	root.SetArgs(args)
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	w.Close()
	os.Stdout, os.Stderr = stdout, stderr
	out, _ := io.ReadAll(r)
	return string(out), ExitCode(err)
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"list", "--bogus"},
		{"start", "a", "b"},
		{"frobnicate"},
	} {
		if _, code := run(t, args...); code != ExitUsage {
			t.Errorf("ffm %v exit %d, want %d", args, code, ExitUsage)
		}
	}
	if _, code := run(t, "status", "nope"); code != ExitNotFound {
		t.Errorf("status of a missing bench exit %d, want %d", code, ExitNotFound)
	}
}

// Every --json document is a single object with its schema name.
func TestJSONDocumentsCarryTheirSchema(t *testing.T) {
	for args, schema := range map[string]string{
		"list":            "ffm.list/v1",
		"version":         "ffm.version/v1",
		"backup list":     "ffm.backups/v1",
		"backup schedule": "ffm.schedules/v1",
		"tunnel server":   "ffm.tunnel-servers/v1",
	} {
		a := append(strings.Fields(args), "--json")
		out, code := run(t, a...)
		if code != ExitOK {
			t.Errorf("ffm %s --json exit %d", args, code)
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Errorf("ffm %s --json is not one JSON object: %v\n%s", args, err, out)
			continue
		}
		if doc["schema"] != schema {
			t.Errorf("ffm %s --json schema = %v, want %s", args, doc["schema"], schema)
		}
	}
}

func TestParseComposePS(t *testing.T) {
	ndjson := `{"Service":"frappe","Name":"ffm-x-frappe-1","State":"running","Status":"Up 2 hours","Health":""}
{"Service":"mariadb","Name":"ffm-x-mariadb-1","State":"running","Status":"Up 2 hours (healthy)","Health":"healthy"}`
	array := `[{"Service":"frappe","Name":"ffm-x-frappe-1","State":"exited","Status":"Exited (0)"}]`
	if got := parseComposePS(ndjson); len(got) != 2 || got[1].Health != "healthy" {
		t.Errorf("NDJSON: %+v", got)
	}
	if got := parseComposePS(array); len(got) != 1 || got[0].State != "exited" {
		t.Errorf("array: %+v", got)
	}
	if got := parseComposePS(""); got == nil || len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
}
