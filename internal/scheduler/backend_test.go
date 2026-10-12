package scheduler

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestHelperProcess(t *testing.T) { fakeexec.HelperMain() }

// env is an isolated host: its own config dir, home, /proc, a PATH holding
// only the programs a test lists, and a fake for every external command.
type env struct {
	t     *testing.T
	home  string
	proc  string
	fake  *fakeexec.Fake
	xdg   string
	progs map[string]bool
}

// isolate builds an env that cannot reach the real crontab, systemd, launchd
// or Task Scheduler: lookPath is a stub, and every command goes to fakeexec.
func isolate(t *testing.T, platform string, progs []string, rules ...fakeexec.Rule) *env {
	t.Helper()
	e := &env{t: t, home: t.TempDir(), proc: t.TempDir(), progs: map[string]bool{}}
	t.Setenv("FFM_CONFIG_DIR", t.TempDir())
	for _, p := range progs {
		e.progs[p] = true
	}
	oldGoos, oldLook, oldHome, oldGetenv, oldProc, oldUID := goos, lookPath, homeDir, getenv, procDir, uid
	t.Cleanup(func() {
		goos, lookPath, homeDir, getenv, procDir, uid = oldGoos, oldLook, oldHome, oldGetenv, oldProc, oldUID
	})
	goos = platform
	lookPath = func(file string) (string, error) {
		if e.progs[file] {
			return "/usr/bin/" + file, nil
		}
		return "", errors.New(file + ": not found")
	}
	homeDir = func() (string, error) { return e.home, nil }
	getenv = func(k string) string {
		if k == "XDG_CONFIG_HOME" {
			return e.xdg
		}
		return ""
	}
	procDir = e.proc
	uid = func() int { return 501 }
	e.fake = fakeexec.Install(t, rules...)
	return e
}

func (e *env) cronDaemon() {
	e.t.Helper()
	dir := filepath.Join(e.proc, "4242")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comm"), []byte("cron\n"), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) callIndex(substr string) int {
	for i, c := range e.fake.Calls() {
		if strings.Contains(c, substr) {
			return i
		}
	}
	return -1
}

// writeCrontab is the one call that rewrites the table: `crontab -`.
func (e *env) crontabWrites() int {
	n := 0
	for _, c := range e.fake.Calls() {
		if c == "crontab -" {
			n++
		}
	}
	return n
}

func (e *env) callCount() int { return len(e.fake.Calls()) }

func nastyJob() Job {
	return Job{
		Minute:  17,
		FFM:     `/opt/it's 100%/$HOME/ffm`,
		PathEnv: "/usr/bin:/bin",
		Env:     map[string]string{"FFM_BACKUPS_DIR": `/srv/ffm "backups" 50%`},
		LogFile: `/home/u/.config/ffm dir/backup-scheduler.log`,
	}
}

var hashed = regexp.MustCompile(`^ffm-backup-[0-9a-f]{8}$`)

func TestJobNameCarriesAHashOnlyWithConfigDir(t *testing.T) {
	t.Setenv("FFM_CONFIG_DIR", "")
	if got := JobName(); got != "ffm-backup" {
		t.Fatalf("JobName() without FFM_CONFIG_DIR = %q", got)
	}
	if launchdLabel() != "dev.foxmayn.ffm.backup" || taskName() != `\ffm\backup` {
		t.Fatalf("plain names: %q %q", launchdLabel(), taskName())
	}
	a, b := t.TempDir(), t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", a)
	nameA := JobName()
	if !hashed.MatchString(nameA) {
		t.Fatalf("JobName() = %q, want ffm-backup-<8 hex>", nameA)
	}
	suffix := strings.TrimPrefix(nameA, "ffm-backup")
	if launchdLabel() != "dev.foxmayn.ffm.backup."+suffix[1:] || taskName() != `\ffm\backup`+suffix {
		t.Fatalf("label %q / task %q do not share the hash %q", launchdLabel(), taskName(), suffix)
	}
	t.Setenv("FFM_CONFIG_DIR", b)
	if JobName() == nameA {
		t.Fatal("two configuration directories share a job name")
	}
	t.Setenv("FFM_CONFIG_DIR", a)
	if JobName() != nameA {
		t.Fatal("job name is not stable")
	}
}

func TestSystemdUnitsEscape(t *testing.T) {
	isolate(t, "linux", nil)
	out := systemdBackend{}.Render(nastyJob())
	for _, want := range []string{
		`Environment="PATH=/usr/bin:/bin"`,
		`Environment="FFM_BACKUPS_DIR=/srv/ffm \"backups\" 50%%"`,
		`ExecStart="/opt/it's 100%%/$$HOME/ffm" "backup" "run-due" "--non-interactive" "--log" "/home/u/.config/ffm dir/backup-scheduler.log"`,
		"StandardOutput=append:/home/u/.config/ffm dir/backup-scheduler.log\n",
		"Type=oneshot\n",
		"OnCalendar=*-*-* *:17:00\n",
		"Persistent=true\n",
		"WantedBy=timers.target\n",
		"Unit=" + JobName() + ".service\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("render lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "$HOME") && !strings.Contains(out, "$$HOME") {
		t.Errorf("a bare $ reached an Exec line:\n%s", out)
	}
}

func TestLaunchdPlistEscapes(t *testing.T) {
	isolate(t, "darwin", nil)
	out := launchdBackend{}.Render(nastyJob())
	for _, want := range []string{
		"<string>" + launchdLabel() + "</string>",
		"<string>/opt/it&#39;s 100%/$HOME/ffm</string>",
		"<string>/srv/ffm &#34;backups&#34; 50%</string>",
		"<key>Minute</key>\n\t\t<integer>17</integer>",
		"<key>StandardOutPath</key>\n\t<string>/home/u/.config/ffm dir/backup-scheduler.log</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plist lacks %q:\n%s", want, out)
		}
	}
	j := nastyJob()
	j.FFM = "/a&b<c>/ffm"
	if got := (launchdBackend{}).Render(j); !strings.Contains(got, "/a&amp;b&lt;c&gt;/ffm") {
		t.Errorf("markup not escaped:\n%s", got)
	}
}

func TestSchtasksCmdFile(t *testing.T) {
	isolate(t, "windows", nil)
	j := nastyJob()
	j.FFM = `C:\Program Files\ffm 100%\ffm.exe`
	j.PathEnv = `C:\Program Files\Docker\resources\bin`
	j.LogFile = `C:\Users\u\AppData\ffm\backup-scheduler.log`
	out := schtasksBackend{}.Render(j)
	want := "@echo off\r\n" +
		"rem ffm backup job " + taskName() + ", hourly at minute 17\r\n" +
		`set "PATH=C:\Program Files\Docker\resources\bin;%PATH%"` + "\r\n" +
		`set "FFM_BACKUPS_DIR=/srv/ffm "backups" 50%%"` + "\r\n" +
		`"C:\Program Files\ffm 100%%\ffm.exe" backup run-due --non-interactive --log "C:\Users\u\AppData\ffm\backup-scheduler.log" >> "C:\Users\u\AppData\ffm\backup-scheduler.log" 2>&1` + "\r\n"
	if out != want {
		t.Errorf("cmd file:\n%q\nwant\n%q", out, want)
	}
	if !strings.Contains(schtasksBackend{}.Print(j), `schtasks /Create /TN "`+taskName()+`" /SC HOURLY /ST 00:17 /F /TR "\"`) {
		t.Errorf("print lacks the create command:\n%s", schtasksBackend{}.Print(j))
	}
}

func TestSystemdInstallUninstall(t *testing.T) {
	e := isolate(t, "linux", []string{"systemctl", "loginctl"},
		fakeexec.Rule{Match: "loginctl show-user 501 -p Linger", Stdout: "Linger=no\n"})
	e.xdg = filepath.Join(e.home, "xdg")
	j := nastyJob()

	res, err := Install(j, BackendSystemd, false)
	if err != nil || !res.Changed || res.Backend != BackendSystemd {
		t.Fatalf("install = %+v, %v", res, err)
	}
	timer := filepath.Join(e.xdg, "systemd", "user", JobName()+".timer")
	service := filepath.Join(e.xdg, "systemd", "user", JobName()+".service")
	for _, p := range []string{timer, service} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("unit not written: %v", err)
		}
	}
	if e.callIndex("systemctl --user daemon-reload") < 0 || e.callIndex("systemctl --user enable --now "+JobName()+".timer") < 0 {
		t.Errorf("calls: %v", e.fake.Calls())
	}
	if e.fake.Called("enable-linger") {
		t.Error("linger was enabled; ffm must only warn")
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "loginctl enable-linger") {
		t.Errorf("warnings = %v", res.Warnings)
	}
	if st, ok, _ := LoadState(); !ok || st.Backend != BackendSystemd || st.FFM != j.FFM || st.Name != JobName() {
		t.Errorf("state = %+v %v", st, ok)
	}

	before := e.callCount()
	res, err = Install(j, "", false)
	if err != nil || res.Changed {
		t.Fatalf("second install = %+v, %v, want no change", res, err)
	}
	for _, c := range e.fake.Calls()[before:] {
		if strings.Contains(c, "daemon-reload") {
			t.Errorf("an unchanged install ran %q", c)
		}
	}

	status, err := CheckStatus(&j)
	if err != nil || !status.Installed || status.Drift || status.Backend != BackendSystemd {
		t.Fatalf("status = %+v, %v", status, err)
	}
	j2 := j
	j2.Minute = 5
	if status, _ = CheckStatus(&j2); !status.Drift {
		t.Error("a changed minute is not drift")
	}

	res, err = Uninstall(j.LogFile, false)
	if err != nil || !res.Changed {
		t.Fatalf("uninstall = %+v, %v", res, err)
	}
	for _, p := range []string{timer, service} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived uninstall", p)
		}
	}
	if st, ok, _ := LoadState(); !ok || st.Backend != BackendSystemd {
		t.Errorf("uninstall forgot the chosen backend: %+v %v", st, ok)
	}
	if e.callIndex("systemctl --user disable --now "+JobName()+".timer") < 0 {
		t.Errorf("calls: %v", e.fake.Calls())
	}
	if res, err = Uninstall(j.LogFile, false); err != nil || res.Changed {
		t.Fatalf("second uninstall = %+v, %v", res, err)
	}
}

func TestLaunchdInstallUninstall(t *testing.T) {
	e := isolate(t, "darwin", []string{"launchctl"})
	j := nastyJob()
	res, err := Install(j, "", false)
	if err != nil || !res.Changed || res.Backend != BackendLaunchd {
		t.Fatalf("install = %+v, %v", res, err)
	}
	plist := filepath.Join(e.home, "Library", "LaunchAgents", launchdLabel()+".plist")
	if _, err := os.Stat(plist); err != nil {
		t.Fatal(err)
	}
	if e.callIndex("launchctl bootout gui/501/"+launchdLabel()) < 0 ||
		e.callIndex("launchctl bootstrap gui/501 "+plist) < 0 {
		t.Errorf("calls: %v", e.fake.Calls())
	}
	if e.callIndex("bootout") > e.callIndex("bootstrap") {
		t.Error("bootstrap ran before bootout")
	}
	before := e.callCount()
	if res, err = Install(j, "", false); err != nil || res.Changed || e.callCount() != before {
		t.Fatalf("unchanged install = %+v, %v, calls %v", res, err, e.fake.Calls()[before:])
	}
	if res, err = Uninstall(j.LogFile, false); err != nil || !res.Changed {
		t.Fatalf("uninstall = %+v, %v", res, err)
	}
	if _, err := os.Stat(plist); err == nil {
		t.Error("plist survived uninstall")
	}
}

func TestSchtasksInstallUninstall(t *testing.T) {
	missing := fakeexec.Rule{Match: "schtasks /Query", Exit: 1}
	e := isolate(t, "windows", []string{"schtasks"}, missing)
	j := nastyJob()
	res, err := Install(j, "", false)
	if err != nil || !res.Changed || res.Backend != BackendSchtasks {
		t.Fatalf("install = %+v, %v", res, err)
	}
	if !e.fake.Called("schtasks /Create /TN " + taskName() + " /SC HOURLY /ST 00:17 /F /TR") {
		t.Errorf("calls: %v", e.fake.Calls())
	}
	data, err := os.ReadFile(cmdPath())
	if err != nil || !strings.HasPrefix(string(data), "@echo off\r\n") {
		t.Fatalf("cmd file = %q, %v", data, err)
	}

	// The task exists and the file is current: nothing to do.
	e2 := isolate(t, "windows", []string{"schtasks"})
	if err := os.MkdirAll(config.ConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cmdPath(), []byte(schtasksBackend{}.Render(j)), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err = Install(j, BackendSchtasks, false); err != nil || res.Changed || e2.fake.Called("/Create") {
		t.Fatalf("unchanged install = %+v, %v, calls %v", res, err, e2.fake.Calls())
	}
	if res, err = Uninstall(j.LogFile, false); err != nil || !res.Changed {
		t.Fatalf("uninstall = %+v, %v", res, err)
	}
	if !e2.fake.Called("schtasks /Delete /TN " + taskName() + " /F") {
		t.Errorf("calls: %v", e2.fake.Calls())
	}
	if _, err := os.Stat(cmdPath()); err == nil {
		t.Error("cmd file survived uninstall")
	}
}

func TestResolveOrder(t *testing.T) {
	isolate(t, "linux", []string{"crontab", "systemctl"},
		fakeexec.Rule{Match: "crontab -l", Stdout: "0 3 * * * x\n" + ownJob().Line() + "\n"})

	// legacy crontab line beats the default (and the default is not even
	// asked: no cron daemon runs here).
	b, src, err := Resolve("")
	if err != nil || b.Name() != BackendCron || src != SourceLegacy {
		t.Fatalf("legacy: %v %q %v", b, src, err)
	}
	// the recorded backend beats the legacy line
	if err := SaveState(State{Backend: BackendSystemd}); err != nil {
		t.Fatal(err)
	}
	if b, src, err = Resolve(""); err != nil || b.Name() != BackendSystemd || src != SourceState {
		t.Fatalf("state: %v %q %v", b, src, err)
	}
	// the flag beats the recorded backend
	if b, src, err = Resolve("launchd"); err != nil || b.Name() != BackendLaunchd || src != SourceFlag {
		t.Fatalf("flag: %v %q %v", b, src, err)
	}
	if _, _, err = Resolve("at"); err == nil || !strings.Contains(err.Error(), "cron, systemd, launchd, schtasks") {
		t.Fatalf("unknown backend: %v", err)
	}
	// a damaged state file is reported, not guessed around
	if err := os.WriteFile(config.BackupSchedulerFile(), []byte("{nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Resolve(""); err == nil || !strings.Contains(err.Error(), "backup-scheduler.json") {
		t.Fatalf("corrupt state: %v", err)
	}
	// no state, no crontab line: the default
	e2 := isolate(t, "linux", []string{"crontab", "systemctl"})
	e2.cronDaemon()
	if b, src, err = Resolve(""); err != nil || b.Name() != BackendCron || src != SourceDefault {
		t.Fatalf("default: %v %q %v", b, src, err)
	}
}

func TestDefaultBackendMatrix(t *testing.T) {
	cases := []struct {
		name   string
		goos   string
		progs  []string
		daemon bool
		rules  []fakeexec.Rule
		want   string // "" = ErrNoBackend
	}{
		{"windows", "windows", nil, false, nil, BackendSchtasks},
		{"macos", "darwin", nil, false, nil, BackendLaunchd},
		{"linux cron running", "linux", []string{"crontab", "systemctl"}, true, nil, BackendCron},
		{"linux cron installed, not running, systemd ok", "linux", []string{"crontab", "systemctl"}, false, nil, BackendSystemd},
		{"linux no crontab, systemd ok", "linux", []string{"systemctl"}, false, nil, BackendSystemd},
		{"linux cron not running, systemd down", "linux", []string{"crontab", "systemctl"}, false,
			[]fakeexec.Rule{{Match: "show-environment", Exit: 1}}, ""},
		{"linux nothing", "linux", nil, false, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := isolate(t, tc.goos, tc.progs, tc.rules...)
			if tc.daemon {
				e.cronDaemon()
			}
			b, err := Default()
			if tc.want == "" {
				if !errors.Is(err, ErrNoBackend) || !strings.Contains(err.Error(), "scheduler print --backend cron") {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if err != nil || b.Name() != tc.want {
				t.Fatalf("Default() = %v, %v, want %s", b, err, tc.want)
			}
		})
	}
}

func TestCronDaemonDetection(t *testing.T) {
	e := isolate(t, "linux", nil)
	if cronDaemonRunning() {
		t.Fatal("empty /proc reported a cron daemon")
	}
	other := filepath.Join(e.proc, "1")
	os.MkdirAll(other, 0o755)
	os.WriteFile(filepath.Join(other, "comm"), []byte("systemd\n"), 0o644)
	os.MkdirAll(filepath.Join(e.proc, "self"), 0o755) // not a pid
	os.WriteFile(filepath.Join(e.proc, "self", "comm"), []byte("cron\n"), 0o644)
	if cronDaemonRunning() {
		t.Fatal("a non-pid entry counted")
	}
	crond := filepath.Join(e.proc, "77")
	os.MkdirAll(crond, 0o755)
	os.WriteFile(filepath.Join(crond, "comm"), []byte("crond\n"), 0o644)
	if !cronDaemonRunning() {
		t.Fatal("crond not found")
	}
}

func TestSwitchingBackendInstallsBeforeRemoving(t *testing.T) {
	j := testJob()
	e := isolate(t, "linux", []string{"crontab", "systemctl"},
		fakeexec.Rule{Match: "crontab -l", Stdout: "0 3 * * * x\n" + j.Line() + "\n"})
	e.xdg = filepath.Join(e.home, "xdg")
	if err := SaveState(State{Backend: BackendCron, Name: JobName(), FFM: j.FFM}); err != nil {
		t.Fatal(err)
	}

	res, err := Install(j, BackendSystemd, false)
	if err != nil {
		t.Fatal(err)
	}
	enable := e.callIndex("enable --now")
	drop := -1
	for i, c := range e.fake.Calls() {
		if c == "crontab -" {
			drop = i
		}
	}
	if enable < 0 || drop < 0 || enable > drop {
		t.Errorf("want systemd enabled before the crontab line is removed (%d, %d): %v", enable, drop, e.fake.Calls())
	}
	if len(res.Removed) != 1 || res.Removed[0] != BackendCron {
		t.Errorf("removed = %v", res.Removed)
	}
	if st, _, _ := LoadState(); st.Backend != BackendSystemd {
		t.Errorf("state = %+v", st)
	}
}

func TestSwitchKeepsForeignCronLine(t *testing.T) {
	j := testJob()
	foreign := testJob()
	foreign.LogFile = "/elsewhere/backup-scheduler.log"
	e := isolate(t, "linux", []string{"crontab", "systemctl"},
		fakeexec.Rule{Match: "crontab -l", Stdout: foreign.Line() + "\n"})
	e.xdg = filepath.Join(e.home, "xdg")
	if err := SaveState(State{Backend: BackendCron}); err != nil {
		t.Fatal(err)
	}
	res, err := Install(j, BackendSystemd, false)
	if err != nil {
		t.Fatal(err)
	}
	if e.crontabWrites() != 0 {
		t.Error("another configuration's crontab line was rewritten")
	}
	if len(res.Removed) != 0 || len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "another ffm configuration") {
		t.Errorf("result = %+v", res)
	}
}

func TestUninstallWithNoSchedulerIsQuiet(t *testing.T) {
	isolate(t, "linux", nil)
	res, err := Uninstall("/log", false)
	if err != nil || res.Changed {
		t.Fatalf("uninstall = %+v, %v", res, err)
	}
}

func TestStatusReportsMissingBinary(t *testing.T) {
	isolate(t, "darwin", []string{"launchctl"})
	j := testJob()
	j.FFM = filepath.Join(t.TempDir(), "gone", "ffm")
	if _, err := Install(j, "", false); err != nil {
		t.Fatal(err)
	}
	st, err := CheckStatus(&j)
	if err != nil || !st.Installed || !st.BinaryMissing || st.Binary != j.FFM {
		t.Fatalf("status = %+v, %v", st, err)
	}
	if st, _ = CheckStatus(nil); st.Drift {
		t.Error("drift without a wanted job")
	}
}

func TestPrintInstructions(t *testing.T) {
	isolate(t, "linux", nil)
	j := testJob()
	if got := (systemdBackend{}).Print(j); !strings.Contains(got, "systemctl --user enable --now "+JobName()+".timer") {
		t.Errorf("systemd print:\n%s", got)
	}
	if got := (launchdBackend{}).Print(j); !strings.Contains(got, "launchctl bootstrap gui/501 ") {
		t.Errorf("launchd print:\n%s", got)
	}
	if got := (cronBackend{}).Print(j); got != j.Line() {
		t.Errorf("cron print = %q", got)
	}
}

// Another configuration's crontab line is neither a legacy install of this
// configuration nor its job: status must not report it as installed.
func TestForeignCronLineIsNotThisJob(t *testing.T) {
	foreign := testJob()
	foreign.LogFile = "/elsewhere/backup-scheduler.log"
	isolate(t, "linux", []string{"crontab", "systemctl"},
		fakeexec.Rule{Match: "crontab -l", Stdout: foreign.Line() + "\n"})
	if legacyCronJob() {
		t.Error("another configuration's line counted as this configuration's legacy install")
	}
	if err := SaveState(State{Backend: BackendCron}); err != nil {
		t.Fatal(err)
	}
	st, err := CheckStatus(nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Installed || len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "/elsewhere/backup-scheduler.log") {
		t.Errorf("status = %+v", st)
	}
}

// ownJob is testJob logging where this (isolated) configuration logs, so its
// crontab line belongs to this configuration. Call it after isolate.
func ownJob() Job {
	j := testJob()
	j.LogFile = config.BackupSchedulerLogFile()
	return j
}
