package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/help.golden")

// TestHelpGolden pins the CLI surface: every command's usage, flags and help
// text. An intended change is recorded with
//
//	go test ./internal/cli -run TestHelpGolden -update
//
// and the diff of testdata/help.golden is reviewed with the change.
func TestHelpGolden(t *testing.T) {
	root := NewRootCmd()
	var names []string
	cmds := map[string]*cobra.Command{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Hidden || c.Name() == "help" || c.Name() == "completion" && c.Parent() == root {
			return
		}
		names = append(names, c.CommandPath())
		cmds[c.CommandPath()] = c
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
	sort.Strings(names)

	var buf bytes.Buffer
	for _, n := range names {
		c := cmds[n]
		buf.WriteString("=== " + n + "\n")
		buf.WriteString(c.UsageString())
		buf.WriteString("\n")
	}
	got := buf.String()

	path := filepath.Join("testdata", "help.golden")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		gl, wl := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := 0; i < len(gl) && i < len(wl); i++ {
			if gl[i] != wl[i] {
				t.Fatalf("CLI help changed at line %d:\n  golden: %q\n  now:    %q\n(run with -update if intended)", i+1, wl[i], gl[i])
			}
		}
		t.Fatalf("CLI help changed in length (golden %d lines, now %d); run with -update if intended", len(wl), len(gl))
	}
}
