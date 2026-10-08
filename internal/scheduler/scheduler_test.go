package scheduler

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testJob() Job {
	return Job{
		Minute:  17,
		FFM:     "/home/nas/.local/bin/ffm",
		PathEnv: "/usr/bin:/bin",
		Env:     map[string]string{"FFM_BACKUPS_DIR": "/srv/ffm backups"},
		LogFile: "/home/nas/.config/ffm/backup-scheduler.log",
	}
}

func TestLineQuotesAndCarriesEnv(t *testing.T) {
	line := testJob().Line()
	for _, want := range []string{
		"17 * * * * ",
		"PATH='/usr/bin:/bin'",
		"FFM_BACKUPS_DIR='/srv/ffm backups'",
		"'/home/nas/.local/bin/ffm' backup run-due --non-interactive",
		Tag,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q lacks %q", line, want)
		}
	}
	j := testJob()
	j.FFM = "/opt/100%/ffm"
	if l := j.Line(); !strings.Contains(l, `100\%`) {
		t.Errorf("%% not escaped for cron: %q", l)
	}
	j.FFM = "/opt/it's/ffm"
	if l := j.Line(); !strings.Contains(l, `'/opt/it'\''s/ffm'`) {
		t.Errorf("single quote not escaped: %q", l)
	}
}

func TestMergeAndRemovePreserveForeignLines(t *testing.T) {
	foreign := "MAILTO=me@example.com\n0 3 * * * /usr/bin/certbot renew\n"
	line := testJob().Line()

	once := Merge(foreign, line)
	if !strings.HasPrefix(once, foreign) || Find(once) != line {
		t.Fatalf("merge into foreign crontab:\n%s", once)
	}
	twice := Merge(once, line)
	if twice != once {
		t.Fatalf("merge is not idempotent:\n%s", twice)
	}
	updated := Merge(once, strings.Replace(line, "17 *", "42 *", 1))
	if strings.Count(updated, Tag) != 1 || !strings.Contains(updated, "42 *") {
		t.Fatalf("update left %d ffm lines:\n%s", strings.Count(updated, Tag), updated)
	}
	if got := Remove(updated); got != foreign {
		t.Fatalf("remove = %q, want the foreign lines untouched %q", got, foreign)
	}
	if got := Merge("", line); got != line+"\n" {
		t.Fatalf("merge into empty crontab = %q", got)
	}
	noNewline := "0 3 * * * x"
	if got := Merge(noNewline, line); got != noNewline+"\n"+line+"\n" {
		t.Fatalf("merge after a line without newline = %q", got)
	}
}

func TestMinuteIsStable(t *testing.T) {
	if minuteFor("vps-1") != minuteFor("vps-1") {
		t.Fatal("minute is not deterministic")
	}
	if m := minuteFor("anything"); m < 0 || m > 59 {
		t.Fatalf("minute %d out of range", m)
	}
}

// TestInstallAgainstFakeCrontab drives Install/Uninstall through a stand-in
// crontab binary that keeps its table in a file, as the real one would.
func TestInstallAgainstFakeCrontab(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("crontab is not used on Windows")
	}
	dir := t.TempDir()
	table := filepath.Join(dir, "table")
	script := `#!/bin/sh
case "$1" in
  -l) if [ -f "` + table + `" ]; then cat "` + table + `"; else echo "no crontab for tester" >&2; exit 1; fi ;;
  -) cat > "` + table + `" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "crontab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	t.Setenv("PATH", dir+":"+filepath.Dir(sh))

	j := testJob()
	changed, err := Install(j, false)
	if err != nil || !changed {
		t.Fatalf("first install = %v, %v", changed, err)
	}
	if changed, err = Install(j, false); err != nil || changed {
		t.Fatalf("second install = %v, %v, want no change", changed, err)
	}
	// A job another configuration installed is neither replaced nor removed.
	foreign := testJob()
	foreign.LogFile = "/elsewhere/backup-scheduler.log"
	os.WriteFile(table, []byte(foreign.Line()+"\n"), 0o600)
	if _, err := Install(j, false); err == nil {
		t.Fatal("a foreign job was replaced")
	}
	if _, err := Uninstall(j.LogFile, false); err == nil {
		t.Fatal("a foreign job was removed")
	}
	if got, _ := os.ReadFile(table); string(got) != foreign.Line()+"\n" {
		t.Fatalf("the foreign job changed: %q", got)
	}
	os.WriteFile(table, []byte("0 3 * * * certbot renew\n"+j.Line()+"\n"), 0o600)
	if changed, err = Uninstall(j.LogFile, false); err != nil || !changed {
		t.Fatalf("uninstall = %v, %v", changed, err)
	}
	got, _ := os.ReadFile(table)
	if string(got) != "0 3 * * * certbot renew\n" {
		t.Fatalf("after uninstall the table is %q", got)
	}
	if changed, err = Uninstall(j.LogFile, false); err != nil || changed {
		t.Fatalf("second uninstall = %v, %v, want no change", changed, err)
	}
}

func TestOwnershipOfTheJob(t *testing.T) {
	mine := Job{Minute: 3, FFM: "/bin/ffm", PathEnv: "/usr/bin", LogFile: "/home/u/.config/ffm/backup-scheduler.log"}
	other := Job{Minute: 3, FFM: "/tmp/ffm-next", PathEnv: "/usr/bin", LogFile: "/home/u/test's/cfg/backup-scheduler.log",
		Env: map[string]string{"FFM_CONFIG_DIR": "/home/u/test's/cfg"}}
	if got := LogOf(other.Line()); got != other.LogFile {
		t.Fatalf("LogOf = %q, want %q", got, other.LogFile)
	}
	crontab := "0 1 * * * echo hi\n" + other.Line() + "\n"
	if err := checkOwner(crontab, mine.LogFile, false); err == nil {
		t.Error("another configuration's job would be replaced")
	} else if _, ok := err.(ForeignJobError); !ok {
		t.Errorf("err = %T %v", err, err)
	}
	if err := checkOwner(crontab, mine.LogFile, true); err != nil {
		t.Errorf("--force refused: %v", err)
	}
	if err := checkOwner(crontab, other.LogFile, false); err != nil {
		t.Errorf("own job refused: %v", err)
	}
	if err := checkOwner("0 1 * * * echo hi\n", mine.LogFile, false); err != nil {
		t.Errorf("no job yet: %v", err)
	}
}
