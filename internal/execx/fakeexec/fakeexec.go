// Package fakeexec replaces execx.Command in tests with a scripted fake, so
// code that drives docker can run without Docker.
//
// It uses the helper-process pattern: every faked command re-runs the test
// binary, which answers from the installed rules and records its argv. Each
// test package using it must contain
//
//	func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }
package fakeexec

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// Rule answers every command whose space-joined argv (program included)
// contains Match. The first matching rule wins; unmatched commands succeed
// with no output.
type Rule struct {
	Match  string
	Stdout string
	// StdoutBytes is written after Stdout, for binary output: the rules
	// reach the helper as JSON, which would mangle bytes that are not UTF-8.
	StdoutBytes []byte
	Exit        int
}

// Fake is an installed fake.
type Fake struct {
	logPath string
}

// Install replaces execx.Command for the duration of the test.
func Install(t *testing.T, rules ...Rule) *Fake {
	t.Helper()
	dir := t.TempDir()
	f := &Fake{logPath: filepath.Join(dir, "calls.log")}
	encoded, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	orig := execx.Command
	execx.Command = func(name string, args ...string) *exec.Cmd {
		cs := append([]string{"-test.run=TestHelperProcess", "--", name}, args...)
		cmd := exec.Command(os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"FAKEEXEC=1",
			"FAKEEXEC_LOG="+f.logPath,
			"FAKEEXEC_RULES="+string(encoded),
		)
		return cmd
	}
	t.Cleanup(func() { execx.Command = orig })
	return f
}

// Calls returns every faked command line, in order.
func (f *Fake) Calls() []string {
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		return nil
	}
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// Called reports whether any call contains every one of parts.
func (f *Fake) Called(parts ...string) bool {
	for _, c := range f.Calls() {
		all := true
		for _, p := range parts {
			if !strings.Contains(c, p) {
				all = false
				break
			}
		}
		if all {
			return true
		}
	}
	return false
}

// HelperMain is the fake command. It does nothing unless run by Install.
func HelperMain() {
	if os.Getenv("FAKEEXEC") != "1" {
		return
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	line := strings.ReplaceAll(strings.Join(args, " "), "\n", "\\n")
	if lf, err := os.OpenFile(os.Getenv("FAKEEXEC_LOG"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		fmt.Fprintln(lf, line)
		lf.Close()
	}
	var rules []Rule
	_ = json.Unmarshal([]byte(os.Getenv("FAKEEXEC_RULES")), &rules)
	for _, r := range rules {
		if strings.Contains(line, r.Match) {
			fmt.Print(r.Stdout)
			os.Stdout.Write(r.StdoutBytes)
			os.Exit(r.Exit)
		}
	}
	os.Exit(0)
}
