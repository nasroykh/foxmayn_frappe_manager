package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/manager"
)

type fakeWorktreeSvc struct {
	add    []manager.WorktreeAddInput
	remove []manager.WorktreeRemoveInput
	err    error
}

func (f *fakeWorktreeSvc) WorktreeAdd(in manager.WorktreeAddInput, pw manager.ProgressWriter) (manager.WorktreeAddResult, error) {
	f.add = append(f.add, in)
	pw.Println("cloning the bench...")
	pw.Step("adding the worktree")
	if f.err != nil {
		return manager.WorktreeAddResult{}, f.err
	}
	return manager.WorktreeAddResult{Bench: "app-feat", Path: "/work/app.worktrees/feat", URL: "http://feat.app.localhost"}, nil
}

func (f *fakeWorktreeSvc) WorktreeRemove(in manager.WorktreeRemoveInput, pw manager.ProgressWriter) (manager.WorktreeRemoveResult, error) {
	f.remove = append(f.remove, in)
	return manager.WorktreeRemoveResult{}, f.err
}

func TestCreateHookPrintsOnlyThePathOnStdout(t *testing.T) {
	svc := &fakeWorktreeSvc{}
	var out, errOut bytes.Buffer
	err := runWorktreeCreateHook(svc, strings.NewReader(`{"session_id":"s","cwd":"/work/app","hook_event_name":"WorktreeCreate","name":"feat"}`), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "/work/app.worktrees/feat\n" {
		t.Errorf("stdout = %q", out.String())
	}
	for _, want := range []string{"cloning the bench", "adding the worktree"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr lacks %q: %q", want, errOut.String())
		}
	}
	if len(svc.add) != 1 || svc.add[0].Branch != "feat" || svc.add[0].Cwd != "/work/app" || svc.add[0].From != "" {
		t.Errorf("add = %+v", svc.add)
	}
}

func TestHooksRefuseBadInputAndFailuresPrintNoPath(t *testing.T) {
	svc := &fakeWorktreeSvc{}
	for _, in := range []string{``, `not json`, `{"cwd":"/x"}`, `{"cwd":"/x","name":"  "}`} {
		var out, errOut bytes.Buffer
		if err := runWorktreeCreateHook(svc, strings.NewReader(in), &out, &errOut); err == nil {
			t.Errorf("create accepted %q", in)
		}
		if out.Len() != 0 {
			t.Errorf("create printed %q on stdout for %q", out.String(), in)
		}
	}
	for _, in := range []string{``, `{`, `{"cwd":"/x"}`} {
		if err := runWorktreeRemoveHook(svc, strings.NewReader(in), &bytes.Buffer{}); err == nil {
			t.Errorf("remove accepted %q", in)
		}
	}
	if len(svc.add)+len(svc.remove) != 0 {
		t.Errorf("the service was called for bad input: %+v %+v", svc.add, svc.remove)
	}

	svc.err = errors.New("clone failed")
	var out bytes.Buffer
	if err := runWorktreeCreateHook(svc, strings.NewReader(`{"cwd":"/x","name":"feat"}`), &out, &bytes.Buffer{}); err == nil || out.Len() != 0 {
		t.Errorf("failed create: err %v, stdout %q", err, out.String())
	}
}

func TestRemoveHookNeverForcesAndDeletesOnlyItsBranch(t *testing.T) {
	dir := t.TempDir() // the worktree path does not exist: the removal "worked"
	svc := &fakeWorktreeSvc{}
	err := runWorktreeRemoveHook(svc, strings.NewReader(`{"worktree_path":"`+filepath.Join(dir, "gone")+`"}`), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if len(svc.remove) != 1 {
		t.Fatalf("remove = %+v", svc.remove)
	}
	if in := svc.remove[0]; in.Force || in.KeepWorktree || !in.DeleteBranch || !in.PathOnly {
		t.Errorf("remove input = %+v", in)
	}

	// The service says yes but the directory is still there: not removed.
	if err := runWorktreeRemoveHook(svc, strings.NewReader(`{"worktree_path":"`+dir+`"}`), &bytes.Buffer{}); err == nil {
		t.Error("exit 0 while the worktree still exists")
	}
}

func TestRemoveHookUnknownWorktreeFailsAndCallsNothing(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	fake := fakeexec.Install(t)
	victim := filepath.Join(root, "some-worktree")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	err := runWorktreeRemoveHook(manager.New(false), strings.NewReader(`{"worktree_path":"`+victim+`"}`), &errOut)
	if err == nil || !errors.Is(err, manager.ErrWorktreeUnknown) {
		t.Fatalf("err = %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("commands ran: %v", fake.Calls())
	}
	if _, serr := os.Stat(victim); serr != nil {
		t.Error("the directory was touched")
	}
}

func TestHookPrintSnippetIsValidSettingsJSON(t *testing.T) {
	for _, want := range []string{`"WorktreeCreate"`, `"WorktreeRemove"`, `ffm worktree hook create`, `ffm worktree hook remove`, `"timeout": 1800`} {
		if !strings.Contains(worktreeHookSnippet, want) {
			t.Errorf("snippet lacks %s", want)
		}
	}
}
