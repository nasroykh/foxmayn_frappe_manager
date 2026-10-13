package manager

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/backuptarget"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestRestoreFromBackupTargetDryRun(t *testing.T) {
	isolatedDirs(t)
	fake := fakeexec.Install(t)
	ctx := context.Background()
	idFile := filepath.Join(t.TempDir(), "id")
	if _, _, err := InitBackupKey(idFile, false); err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	if err := AddTarget(ctx, backuptarget.Target{Name: "nas", Type: backuptarget.TypeLocal, Path: remote}, false, false); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	restorableArchive(t, work, "alpha")
	plain := filepath.Join(work, "alpha.ffm.tar")
	hraw, err := archive.PeekHeader(plain)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := encryptArchive(plain, hraw)
	if err != nil {
		t.Fatal(err)
	}
	if err := uploadToTargets([]string{"nas"}, "alpha", enc, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	s := New(false)
	in := RestoreInput{TargetName: "viatarget", Identity: idFile,
		DryRun: true, SkipSpaceCheck: true, WithFiles: true, AllowRemote: true}

	for _, src := range []string{"target://nas/alpha", "target://nas/alpha/alpha.ffm.tar.age"} {
		in.Archive = src
		pw := &BufferProgress{}
		if err := s.Restore(in, pw); err != nil {
			t.Fatalf("%s: %v\n%v", src, err, pw.Lines)
		}
		if out := strings.Join(pw.Lines, "\n"); !strings.Contains(out, "this restore would succeed") {
			t.Errorf("%s: no plan printed:\n%s", src, out)
		}
	}
	if left := downloadDirs(t); len(left) != 0 {
		t.Errorf("download left behind: %v", left)
	}
	if fake.Called("compose -p") {
		t.Error("a dry run touched compose")
	}

	for src, want := range map[string]string{
		"target://nas/alpha/nope.ffm.tar.age": "no archive",
		"target://nas/ghost":                  "no archives",
		"target://other/alpha":                "no backup target",
	} {
		in.Archive = src
		if err := s.Restore(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", src, err, want)
		}
	}

	// The size cap applies to a target too.
	in.Archive, in.MaxDownloadBytes = "target://nas/alpha", 10
	if err := s.Restore(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "download limit") {
		t.Errorf("cap: %v", err)
	}
	if left := downloadDirs(t); len(left) != 0 {
		t.Errorf("download left behind: %v", left)
	}
}
