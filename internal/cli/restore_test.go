package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestRestoreFlagValidation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	t.Setenv("FFM_NON_INTERACTIVE", "1")
	fake := fakeexec.Install(t)
	dump := filepath.Join(root, "db.sql.gz")
	os.WriteFile(dump, []byte("x"), 0o600)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"frappe flag without --frappe-backup", []string{"a.ffm.tar", "--files", "f.tar"}, "--files only applies to --frappe-backup"},
		{"--apps without --frappe-backup", []string{"a.ffm.tar", "--apps", "erpnext"}, "--apps only applies"},
		{"--mode without --frappe-backup", []string{"a.ffm.tar", "--mode", "prod"}, "--mode only applies"},
		{"frappe import needs exactly the name", []string{"--frappe-backup", dump}, "exactly the new bench's name"},
		{"frappe import with two args", []string{"--frappe-backup", dump, "a", "b"}, "exactly the new bench's name"},
		{"identity is meaningless for a frappe import", []string{"--frappe-backup", dump, "n", "--identity", "x"}, "--identity does not apply"},
		{"no-files contradicts --files", []string{"--frappe-backup", dump, "n", "--no-files", "--files", "f.tar"}, "contradicts"},
		{"http is refused", []string{"http://example.com/a.ffm.tar"}, "plain http"},
		{"unknown scheme", []string{"s3://b/a.ffm.tar"}, "unsupported source scheme"},
	}
	for _, tt := range tests {
		_, _, err := runCaptured(t, newRestoreCmd(), tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.want)
			continue
		}
		if strings.HasPrefix(tt.name, "frappe") || strings.Contains(tt.name, "without") || strings.Contains(tt.name, "contradicts") ||
			strings.Contains(tt.name, "identity") {
			var ue usageError
			if !errors.As(err, &ue) {
				t.Errorf("%s: %T is not a usageError (exit 2)", tt.name, err)
			}
		}
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("docker was reached: %v", fake.Calls())
	}
}
