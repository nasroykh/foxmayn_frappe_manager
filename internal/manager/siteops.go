package manager

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// resultPrefix marks the line of a site script's output that carries its
// result. Frappe prints warnings and deprecation notices of its own, so the
// result is found by prefix, never by position.
const resultPrefix = "FFM_RESULT="

// runningBench returns the bench when its frappe container runs, and
// ErrBenchStopped (with the command that fixes it) when it does not.
func (s *Service) runningBench(name string) (state.Bench, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return b, err
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return b, fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}
	return b, nil
}

// runSiteRequest runs a Python script in the frappe container with req, JSON
// encoded, on its stdin, and returns the document the script printed after
// resultPrefix.
//
// This is how every site administration command hands over values. Anything
// on the script's argument list would sit on the process list of the container
// and, through docker, of the host, for as long as it runs; req can carry a
// password, an API key or an SMTP login. Only the site name, which is not a
// secret, goes on the command line. secrets are masked in any error text.
func runSiteRequest(runner *bench.Runner, site, script string, req any, secrets ...string) (json.RawMessage, error) {
	payload, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	r := *runner
	r.Redact = append(append([]string{}, runner.Redact...), secrets...)
	cmd := "cd /workspace/frappe-bench/sites && /workspace/frappe-bench/env/bin/python -c " +
		bench.ShellQuote(script) + " " + bench.ShellQuote(site)
	out, err := r.ExecStdinOut("frappe", strings.NewReader(string(payload)+"\n"), "bash", "-c", cmd)
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, lastLines(out, 8))
	}
	var result json.RawMessage
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), resultPrefix); ok {
			result = json.RawMessage(v)
		}
	}
	if result == nil {
		return nil, fmt.Errorf("the site script printed no result\n%s", lastLines(out, 5))
	}
	return result, nil
}

// singleLine rejects values that cannot travel as one line of input.
func singleLine(what, v string) error {
	if v == "" {
		return fmt.Errorf("%s cannot be empty", what)
	}
	if strings.ContainsAny(v, "\x00\n\r") {
		return fmt.Errorf("%s cannot contain line breaks or NUL", what)
	}
	return nil
}
