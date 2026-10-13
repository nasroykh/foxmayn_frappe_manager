package proxy

import (
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

// inspectJSON is what realInspect parses from one `docker inspect`.
func inspectJSON(status string, networks, ports string) string {
	return `{"status":"` + status + `","image":"` + Image + `","networks":` + networks +
		`,"ports":` + ports + `,"bindings":{},"args":[]}` + "\n"
}

func TestServing(t *testing.T) {
	good := inspectJSON("running", `{"`+NetworkName+`":{}}`, `{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}]}`)
	noNet := inspectJSON("running", `{}`, `{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"80"}]}`)
	noPort := inspectJSON("running", `{"`+NetworkName+`":{}}`, `{}`)
	stopped := inspectJSON("exited", `{}`, `{}`)

	for _, tc := range []struct {
		name       string
		rule       fakeexec.Rule
		serving    bool
		wantBroken bool
	}{
		{"healthy", fakeexec.Rule{Match: "inspect", Stdout: good}, true, false},
		{"no network", fakeexec.Rule{Match: "inspect", Stdout: noNet}, false, true},
		{"no published port", fakeexec.Rule{Match: "inspect", Stdout: noPort}, false, true},
		{"stopped is not broken", fakeexec.Rule{Match: "inspect", Stdout: stopped}, false, false},
		{"inspect error", fakeexec.Rule{Match: "inspect", Exit: 1}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeexec.Install(t, tc.rule)
			serving, broken := Probe()
			if serving != tc.serving || (broken != "") != tc.wantBroken {
				t.Errorf("Probe = %v, %q", serving, broken)
			}
			if Serving() != tc.serving {
				t.Errorf("Serving = %v, want %v", !tc.serving, tc.serving)
			}
			n := 0
			for _, c := range f.Calls() {
				if len(c) >= 13 && c[:13] == "docker inspec" {
					n++
				}
			}
			if n != 2 { // one per call above: each asks docker exactly once
				t.Errorf("docker inspect calls = %d, want 2: %v", n, f.Calls())
			}
		})
	}
}
