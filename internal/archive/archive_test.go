package archive

import (
	"archive/tar"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// entry is a member spec for the in-memory tars the tests build. Using raw
// tar.Header values (rather than the Writer) is deliberate: these tests exist to
// prove the READER rejects archives ffm would never write.
type entry struct {
	name     string
	body     string
	typeflag byte
	link     string
	size     int64 // when non-zero, overrides len(body) to build a truncated member
}

func buildTar(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		tf := e.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		size := int64(len(e.body))
		if tf != tar.TypeReg {
			size = 0
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:     e.name,
			Mode:     0o600,
			Size:     size,
			Typeflag: tf,
			Linkname: e.link,
		}); err != nil {
			t.Fatalf("write header %q: %v", e.name, err)
		}
		if tf == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				t.Fatalf("write body %q: %v", e.name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	return buf.Bytes()
}

// wrap returns entries framed by the reserved header and manifest members, so a
// test case only has to describe the member it is actually about.
func wrap(middle ...entry) []entry {
	out := []entry{{name: HeaderName, body: `{"kind":"ffm-backup"}`}}
	out = append(out, middle...)
	return append(out, entry{name: ManifestName, body: `{"members":[]}`})
}

func TestExtractRejectsHostileMembers(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
		wantErr string
	}{
		{
			name:    "parent traversal",
			entries: wrap(entry{name: "ffm-backup/../../escape.txt", body: "x"}),
			wantErr: "escapes the extraction directory",
		},
		{
			name:    "traversal hidden mid-path",
			entries: wrap(entry{name: "ffm-backup/a/../../../escape.txt", body: "x"}),
			wantErr: "escapes the extraction directory",
		},
		{
			name:    "absolute path",
			entries: wrap(entry{name: "/etc/passwd", body: "x"}),
			wantErr: "absolute path",
		},
		{
			name:    "windows drive letter",
			entries: wrap(entry{name: `C:\windows\system32\drivers\etc\hosts`, body: "x"}),
			wantErr: "absolute path",
		},
		{
			name:    "backslash traversal",
			entries: wrap(entry{name: `ffm-backup\..\..\escape.txt`, body: "x"}),
			wantErr: "escapes the extraction directory",
		},
		{
			name:    "outside the archive prefix",
			entries: wrap(entry{name: "somewhere-else/db.sql", body: "x"}),
			wantErr: "is not an ffm archive",
		},
		{
			name:    "empty name",
			entries: wrap(entry{name: "", body: "x"}),
			wantErr: "empty name",
		},
		{
			name:    "symlink",
			entries: wrap(entry{name: "ffm-backup/link", typeflag: tar.TypeSymlink, link: "/etc/passwd"}),
			wantErr: "unsupported type",
		},
		{
			name:    "hardlink",
			entries: wrap(entry{name: "ffm-backup/link", typeflag: tar.TypeLink, link: "/etc/passwd"}),
			wantErr: "unsupported type",
		},
		{
			name:    "fifo",
			entries: wrap(entry{name: "ffm-backup/pipe", typeflag: tar.TypeFifo}),
			wantErr: "unsupported type",
		},
		{
			name:    "character device",
			entries: wrap(entry{name: "ffm-backup/dev", typeflag: tar.TypeChar}),
			wantErr: "unsupported type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := buildTar(t, tt.entries)
			dest := t.TempDir()
			_, err := ExtractReader(bytes.NewReader(data), dest, Limits{})
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, tt.wantErr)
			}
			// Nothing may have escaped, whatever the failure mode.
			if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "escape.txt")); statErr == nil {
				t.Fatalf("a member escaped the extraction directory")
			}
		})
	}
}

func TestExtractRejectsTruncatedArchive(t *testing.T) {
	t.Run("no trailing manifest", func(t *testing.T) {
		data := buildTar(t, []entry{
			{name: HeaderName, body: `{"kind":"ffm-backup"}`},
			{name: "ffm-backup/db/database.sql.gz", body: "payload"},
		})
		_, err := ExtractReader(bytes.NewReader(data), t.TempDir(), Limits{})
		if !errors.Is(err, ErrNoManifest) {
			t.Fatalf("error = %v, want ErrNoManifest", err)
		}
	})

	// Cut at several depths: the stream can end between members (caught when
	// reading the next header) or part-way through one (caught mid-copy). Both
	// have to report the same thing, or a half-written backup looks like a
	// mysterious I/O error instead of a backup to take again.
	t.Run("stream cut mid-member", func(t *testing.T) {
		full := buildTar(t, wrap(entry{name: "ffm-backup/db/database.sql.gz", body: strings.Repeat("a", 40960)}))
		for _, cut := range []int{2048, 4096, 8192, 20000, len(full) - 2048} {
			if cut <= 0 || cut >= len(full) {
				continue
			}
			_, err := ExtractReader(bytes.NewReader(full[:cut]), t.TempDir(), Limits{})
			if !errors.Is(err, ErrNoManifest) {
				t.Errorf("cut at %d: error = %v, want ErrNoManifest", cut, err)
			}
		}
	})
}

func TestExtractRequiresHeaderFirst(t *testing.T) {
	data := buildTar(t, []entry{
		{name: "ffm-backup/db/database.sql.gz", body: "payload"},
		{name: HeaderName, body: `{}`},
		{name: ManifestName, body: `{}`},
	})
	_, err := ExtractReader(bytes.NewReader(data), t.TempDir(), Limits{})
	if err == nil || !strings.Contains(err.Error(), "first member") {
		t.Fatalf("error = %v, want a complaint about the first member", err)
	}
}

func TestExtractEnforcesLimits(t *testing.T) {
	t.Run("entry count", func(t *testing.T) {
		var middle []entry
		for i := 0; i < 10; i++ {
			middle = append(middle, entry{name: "ffm-backup/f" + string(rune('0'+i)), body: "x"})
		}
		data := buildTar(t, wrap(middle...))
		_, err := ExtractReader(bytes.NewReader(data), t.TempDir(), Limits{MaxEntries: 4})
		if err == nil || !strings.Contains(err.Error(), "more than 4 members") {
			t.Fatalf("error = %v, want an entry-count refusal", err)
		}
	})

	t.Run("total bytes", func(t *testing.T) {
		data := buildTar(t, wrap(entry{name: "ffm-backup/big", body: strings.Repeat("a", 5000)}))
		_, err := ExtractReader(bytes.NewReader(data), t.TempDir(), Limits{MaxBytes: 1000})
		if err == nil || !strings.Contains(err.Error(), "refusing to extract") {
			t.Fatalf("error = %v, want a size refusal", err)
		}
	})
}

func TestWriteThenExtractRoundTrip(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bkptest_20260826T120000Z.ffm.tar")

	w, err := Create(dest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.AddBytes(HeaderName, []byte(`{"kind":"ffm-backup"}`), EncodingNone); err != nil {
		t.Fatalf("AddBytes header: %v", err)
	}
	payload := strings.Repeat("frappe", 1000)
	dbMember, err := w.AddStream("ffm-backup/db/database.sql.gz", int64(len(payload)),
		strings.NewReader(payload), EncodingGzip)
	if err != nil {
		t.Fatalf("AddStream: %v", err)
	}
	if _, err := w.AddBytes(ManifestName, []byte(`{"members":[]}`), EncodingNone); err != nil {
		t.Fatalf("AddBytes manifest: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := os.Stat(dest + ".partial"); !os.IsNotExist(err) {
		t.Fatalf(".partial file still present after Close")
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("archive mode = %o, want 600", got)
	}

	hdr, err := PeekHeader(dest)
	if err != nil {
		t.Fatalf("PeekHeader: %v", err)
	}
	if string(hdr) != `{"kind":"ffm-backup"}` {
		t.Errorf("PeekHeader = %q", hdr)
	}

	res, err := Extract(dest, filepath.Join(dir, "out"), Limits{})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	got, ok := res.Members["ffm-backup/db/database.sql.gz"]
	if !ok {
		t.Fatalf("db member missing from %v", res.Order)
	}
	if got.SHA256 != dbMember.SHA256 {
		t.Errorf("digest mismatch: wrote %s, read %s", dbMember.SHA256, got.SHA256)
	}
	if got.Size != int64(len(payload)) {
		t.Errorf("size = %d, want %d", got.Size, len(payload))
	}
	if res.Order[0] != HeaderName || res.Order[len(res.Order)-1] != ManifestName {
		t.Errorf("member order = %v", res.Order)
	}
}

func TestAddStreamRejectsSizeMismatch(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a.ffm.tar")
	w, err := Create(dest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer w.Abort()

	// Declare more than the reader will yield — a file that shrank mid-backup.
	_, err = w.AddStream("ffm-backup/db/database.sql.gz", 100, strings.NewReader("short"), EncodingGzip)
	if err == nil || !strings.Contains(err.Error(), "declared 100 bytes but yielded 5") {
		t.Fatalf("error = %v, want a size-mismatch complaint", err)
	}
}

func TestAbortLeavesNoArtifacts(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "a.ffm.tar")
	w, err := Create(dest)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := w.AddBytes(HeaderName, []byte(`{}`), EncodingNone); err != nil {
		t.Fatalf("AddBytes: %v", err)
	}
	w.Abort()

	for _, p := range []string{dest, dest + ".partial"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s exists after Abort", p)
		}
	}
}

func TestPeekHeaderRejectsForeignArchives(t *testing.T) {
	dir := t.TempDir()

	notTar := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notTar, []byte("this is not a tar file at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := PeekHeader(notTar); err == nil {
		t.Errorf("PeekHeader accepted a non-tar file")
	}

	foreign := filepath.Join(dir, "other.tar")
	if err := os.WriteFile(foreign, buildTar(t, []entry{{name: "some/file", body: "x"}}), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PeekHeader(foreign)
	if err == nil || !strings.Contains(err.Error(), "not an ffm archive") {
		t.Fatalf("error = %v, want a not-an-ffm-archive complaint", err)
	}
}

// Directories carry no bytes, so they slip past a size cap; they must still be
// caught by the entry cap, or an archive of nothing but directory headers can
// create millions of inodes during a preflight that has not yet decided whether
// the archive is trustworthy.
func TestExtractCountsDirectoryMembers(t *testing.T) {
	var middle []entry
	for i := 0; i < 500; i++ {
		middle = append(middle, entry{name: "ffm-backup/d" + strconv.Itoa(i), typeflag: tar.TypeDir})
	}
	data := buildTar(t, wrap(middle...))

	dest := t.TempDir()
	_, err := ExtractReader(bytes.NewReader(data), dest, Limits{MaxEntries: 10, MaxBytes: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "more than 10 members") {
		t.Fatalf("error = %v, want an entry-count refusal", err)
	}
	made, _ := os.ReadDir(dest)
	if len(made) > 12 {
		t.Errorf("extraction created %d entries before giving up", len(made))
	}
}
