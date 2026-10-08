package manager

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/agecrypt"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/backuptarget"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

type targetsFile struct {
	Targets []backuptarget.Target `json:"targets"`
}

// LoadTargets reads backup-targets.json; a missing file is no targets.
func LoadTargets() ([]backuptarget.Target, error) {
	raw, err := os.ReadFile(config.BackupTargetsFile())
	if os.IsNotExist(err) {
		return []backuptarget.Target{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f targetsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", config.BackupTargetsFile(), err)
	}
	return f.Targets, nil
}

func saveTargets(ts []backuptarget.Target) error {
	sort.Slice(ts, func(i, j int) bool { return ts[i].Name < ts[j].Name })
	raw, err := json.MarshalIndent(targetsFile{Targets: ts}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(config.BackupTargetsFile()), 0o700); err != nil {
		return err
	}
	tmp := config.BackupTargetsFile() + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, config.BackupTargetsFile())
}

// GetTarget returns one target by name.
func GetTarget(name string) (backuptarget.Target, error) {
	ts, err := LoadTargets()
	if err != nil {
		return backuptarget.Target{}, err
	}
	for _, t := range ts {
		if t.Name == name {
			return t, nil
		}
	}
	return backuptarget.Target{}, fmt.Errorf("no backup target %q (see: ffm backup target list)", name)
}

// AddTarget saves a target after checking it can store, list, read back and
// delete an object. replace allows overwriting a target of the same name.
func AddTarget(ctx context.Context, t backuptarget.Target, replace, skipTest bool) error {
	if err := t.Validate(); err != nil {
		return err
	}
	ts, err := LoadTargets()
	if err != nil {
		return err
	}
	kept := ts[:0]
	for _, e := range ts {
		if e.Name == t.Name {
			if !replace {
				return fmt.Errorf("backup target %q exists (pass --replace)", t.Name)
			}
			continue
		}
		kept = append(kept, e)
	}
	if !skipTest {
		if err := testTarget(ctx, t); err != nil {
			return fmt.Errorf("target %q failed its test, not saved: %w", t.Name, err)
		}
	}
	return saveTargets(append(kept, t))
}

// RemoveTarget forgets a target; nothing stored on it is deleted.
func RemoveTarget(name string, s *Service) error {
	ts, err := LoadTargets()
	if err != nil {
		return err
	}
	if benches, err := s.LoadBenches(); err == nil {
		for _, b := range benches {
			if b.BackupSchedule != nil && containsString(b.BackupSchedule.Targets, name) {
				return fmt.Errorf("bench %q uploads its scheduled backups to %q; change that first (ffm backup schedule %s --to …)", b.Name, name, b.Name)
			}
		}
	}
	kept := ts[:0]
	found := false
	for _, t := range ts {
		if t.Name == name {
			found = true
			continue
		}
		kept = append(kept, t)
	}
	if !found {
		return fmt.Errorf("no backup target %q", name)
	}
	return saveTargets(kept)
}

func containsString(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// TestTarget runs testTarget on a saved target.
func TestTarget(ctx context.Context, name string) error {
	t, err := GetTarget(name)
	if err != nil {
		return err
	}
	return testTarget(ctx, t)
}

// testTarget stores, lists, reads back and deletes a small object.
func testTarget(ctx context.Context, t backuptarget.Target) error {
	d, err := backuptarget.Open(ctx, t)
	if err != nil {
		return err
	}
	defer d.Close()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	key := ".ffm-test/" + hex.EncodeToString(b)
	payload := []byte("ffm target test " + hex.EncodeToString(b))
	if err := d.Put(ctx, key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	defer d.Delete(ctx, key) //nolint:errcheck // best effort; reported below if it matters
	objs, err := d.List(ctx, ".ffm-test/")
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	listed := false
	for _, o := range objs {
		listed = listed || (o.Key == key && o.Size == int64(len(payload)))
	}
	if !listed {
		return fmt.Errorf("list: the test object is missing or has the wrong size")
	}
	var got bytes.Buffer
	if err := d.Get(ctx, key, &got); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	if !bytes.Equal(got.Bytes(), payload) {
		return fmt.Errorf("read: got different bytes back")
	}
	if err := d.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	return nil
}

// remoteKey is where an archive of a bench lives on a target.
func remoteKey(bench, file string) string { return bench + "/" + file }

// uploadArchive copies an encrypted archive and its sidecar to a target, then
// checks the target lists both at the right size. Only then has the upload
// happened.
func uploadArchive(ctx context.Context, d backuptarget.Destination, bench, archivePath string) error {
	// The last guard: whatever the caller did, a plaintext archive (it holds
	// the database and every credential of the site) is never uploaded.
	if !agecrypt.IsEncrypted(archivePath) {
		return fmt.Errorf("refusing to upload %s: only age-encrypted archives leave the host", filepath.Base(archivePath))
	}
	files := []string{archivePath, sidecarPath(archivePath)}
	for _, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			return err
		}
		in, err := os.Open(f)
		if err != nil {
			return err
		}
		err = d.Put(ctx, remoteKey(bench, filepath.Base(f)), in, st.Size())
		in.Close()
		if err != nil {
			return fmt.Errorf("upload %s: %w", filepath.Base(f), err)
		}
	}
	objs, err := d.List(ctx, bench+"/")
	if err != nil {
		return fmt.Errorf("confirm the upload: %w", err)
	}
	sizes := map[string]int64{}
	for _, o := range objs {
		sizes[o.Key] = o.Size
	}
	for _, f := range files {
		st, _ := os.Stat(f)
		if got, ok := sizes[remoteKey(bench, filepath.Base(f))]; !ok || got != st.Size() {
			return fmt.Errorf("confirm the upload: %s is missing or has %d bytes instead of %d", filepath.Base(f), got, st.Size())
		}
	}
	return nil
}

// uploadToTargets uploads one archive to every named target.
func uploadToTargets(names []string, bench, archivePath string, pw ProgressWriter) error {
	for _, name := range names {
		t, err := GetTarget(name)
		if err != nil {
			return err
		}
		pw.Step("Uploading to " + name + " (" + t.Describe() + ")")
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
		d, err := backuptarget.Open(ctx, t)
		if err == nil {
			err = uploadArchive(ctx, d, bench, archivePath)
			d.Close()
		}
		cancel()
		if err != nil {
			return fmt.Errorf("target %s: %w", name, err)
		}
	}
	return nil
}

// RemoteArchive is an archive stored on a target.
type RemoteArchive struct {
	Key    string
	Size   int64
	Header Header
	Err    error
}

// ListRemoteArchives lists a bench's encrypted archives on a target, newest
// first, reading each header from its sidecar.
func ListRemoteArchives(ctx context.Context, targetName, bench string) ([]RemoteArchive, error) {
	t, err := GetTarget(targetName)
	if err != nil {
		return nil, err
	}
	d, err := backuptarget.Open(ctx, t)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return listRemote(ctx, d, bench)
}

func listRemote(ctx context.Context, d backuptarget.Destination, bench string) ([]RemoteArchive, error) {
	objs, err := d.List(ctx, bench+"/")
	if err != nil {
		return nil, err
	}
	var out []RemoteArchive
	for _, o := range objs {
		if !strings.HasSuffix(o.Key, ".ffm.tar"+agecrypt.Ext) {
			continue
		}
		ra := RemoteArchive{Key: o.Key, Size: o.Size}
		var buf bytes.Buffer
		if err := d.Get(ctx, sidecarPath(o.Key), &buf); err != nil {
			ra.Err = fmt.Errorf("no header sidecar: %w", err)
		} else {
			ra.Header, ra.Err = ParseHeader(buf.Bytes())
		}
		out = append(out, ra)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Header.CreatedAt.After(out[j].Header.CreatedAt) })
	return out, nil
}

// pruneRemote applies a schedule's retention to a bench's archives on a
// target: the same tiers and floor as locally, scheduled archives of this
// bench only.
func pruneRemote(ctx context.Context, targetName, bench string, p state.BackupPolicy, loc *time.Location) (int, error) {
	t, err := GetTarget(targetName)
	if err != nil {
		return 0, err
	}
	d, err := backuptarget.Open(ctx, t)
	if err != nil {
		return 0, err
	}
	defer d.Close()
	remote, err := listRemote(ctx, d, bench)
	if err != nil {
		return 0, err
	}
	var candidates []ArchiveInfo
	for _, r := range remote {
		a := ArchiveInfo{Path: r.Key, Size: r.Size, Header: r.Header, Err: r.Err, Encrypted: true}
		if prunable(a, bench) {
			candidates = append(candidates, a)
		}
	}
	_, drop := selectRetained(candidates, p, loc)
	for _, a := range drop {
		if err := d.Delete(ctx, a.Path); err != nil {
			return 0, fmt.Errorf("delete %s: %w", path.Base(a.Path), err)
		}
		_ = d.Delete(ctx, sidecarPath(a.Path))
	}
	return len(drop), nil
}

// PullArchive downloads an archive (the newest when file is empty) and its
// sidecar from a target into the bench's local backups directory.
func PullArchive(ctx context.Context, targetName, bench, file string, pw ProgressWriter) (string, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	t, err := GetTarget(targetName)
	if err != nil {
		return "", err
	}
	d, err := backuptarget.Open(ctx, t)
	if err != nil {
		return "", err
	}
	defer d.Close()
	remote, err := listRemote(ctx, d, bench)
	if err != nil {
		return "", err
	}
	var pick *RemoteArchive
	for i := range remote {
		if (file == "" && remote[i].Err == nil) || path.Base(remote[i].Key) == file {
			pick = &remote[i]
			break
		}
	}
	if pick == nil {
		if file == "" {
			return "", fmt.Errorf("no archives of %q on %s", bench, targetName)
		}
		return "", fmt.Errorf("no archive %q of %q on %s", file, bench, targetName)
	}
	dir, err := config.EnsureBenchBackupsDir(bench)
	if err != nil {
		return "", err
	}
	local := filepath.Join(dir, path.Base(pick.Key))
	if _, err := os.Stat(local); err == nil {
		return "", fmt.Errorf("%s already exists locally", local)
	}
	pw.Step(fmt.Sprintf("Downloading %s (%s)", path.Base(pick.Key), humanBytes(pick.Size)))
	for _, pair := range [][2]string{{pick.Key, local}, {sidecarPath(pick.Key), sidecarPath(local)}} {
		if err := download(ctx, d, pair[0], pair[1]); err != nil {
			os.Remove(local)
			return "", err
		}
	}
	if st, err := os.Stat(local); err != nil || st.Size() != pick.Size {
		os.Remove(local)
		os.Remove(sidecarPath(local))
		return "", fmt.Errorf("download of %s is incomplete", path.Base(pick.Key))
	}
	return local, nil
}

func download(ctx context.Context, d backuptarget.Destination, key, dst string) error {
	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := d.Get(ctx, key, f); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("download %s: %w", path.Base(key), err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
