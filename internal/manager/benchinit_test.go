package manager

import (
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

func TestBenchInitArgs(t *testing.T) {
	got := benchInitArgs("version-15", "", bench.ToolchainFor("version-15"))
	if want := "--frappe-branch 'version-15' --python 'python3.12'"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = benchInitArgs("main", "https://example.com/f.git", bench.Toolchain{Python: "3.14", Node: "24"})
	if want := "--frappe-branch 'main' --python 'python3.14' --frappe-path 'https://example.com/f.git'"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
