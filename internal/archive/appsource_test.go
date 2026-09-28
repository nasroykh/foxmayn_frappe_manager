package archive

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func gzipTar(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(buildTar(t, entries)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestValidateAppSourceAcceptsAnApp(t *testing.T) {
	data := gzipTar(t, []entry{
		{name: "my_app/", typeflag: tar.TypeDir},
		{name: "my_app/pyproject.toml", body: "[project]\n"},
		{name: "my_app/my_app/__init__.py", body: "__version__ = '1.0'\n"},
		// Links that stay inside the app are ordinary in a repository.
		{name: "my_app/README", typeflag: tar.TypeSymlink, link: "docs/README.md"},
		{name: "my_app/docs/up", typeflag: tar.TypeSymlink, link: "../pyproject.toml"},
		{name: "my_app/copy.py", typeflag: tar.TypeLink, link: "my_app/my_app/__init__.py"},
	})
	if err := validateAppSource(bytes.NewReader(data), "my_app", DefaultAppSourceLimits()); err != nil {
		t.Fatalf("valid app source rejected: %v", err)
	}
}

// The tarball is unpacked inside the frappe container, whose ./workspace is a
// bind mount of the host, so each of these would write outside the app.
func TestValidateAppSourceRejectsHostileTars(t *testing.T) {
	tests := []struct {
		name    string
		entries []entry
		wantSub string
	}{
		{"absolute path", []entry{{name: "/etc/passwd", body: "x"}}, "absolute path"},
		{"parent traversal", []entry{{name: "my_app/../../x", body: "x"}}, "escapes"},
		{"another app's directory", []entry{{name: "frappe/hooks.py", body: "x"}}, "outside the app"},
		{"prefix that is not a directory boundary", []entry{{name: "my_app2/x", body: "x"}}, "outside the app"},
		{"symlink out of the app", []entry{{name: "my_app/x", typeflag: tar.TypeSymlink, link: "../../../etc"}}, "outside the app"},
		{"absolute symlink", []entry{{name: "my_app/x", typeflag: tar.TypeSymlink, link: "/etc"}}, "absolute path"},
		{"hard link out of the app", []entry{{name: "my_app/x", typeflag: tar.TypeLink, link: "frappe/hooks.py"}}, "outside the app"},
		{"device node", []entry{{name: "my_app/dev", typeflag: tar.TypeChar}}, "unsupported type"},
		{"fifo", []entry{{name: "my_app/fifo", typeflag: tar.TypeFifo}}, "unsupported type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAppSource(bytes.NewReader(gzipTar(t, tt.entries)), "my_app", DefaultAppSourceLimits())
			if err == nil || !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantSub)
			}
		})
	}
}

func TestValidateAppSourceLimits(t *testing.T) {
	entries := []entry{
		{name: "my_app/a", body: strings.Repeat("a", 600)},
		{name: "my_app/b", body: strings.Repeat("b", 600)},
	}
	data := gzipTar(t, entries)
	if err := validateAppSource(bytes.NewReader(data), "my_app", AppSourceLimits{MaxEntries: 1, MaxBytes: 1 << 20}); err == nil ||
		!strings.Contains(err.Error(), "entries") {
		t.Fatalf("entry cap not enforced: %v", err)
	}
	if err := validateAppSource(bytes.NewReader(data), "my_app", AppSourceLimits{MaxEntries: 10, MaxBytes: 1000}); err == nil ||
		!strings.Contains(err.Error(), "bytes") {
		t.Fatalf("size cap not enforced: %v", err)
	}
}

func TestValidateAppSourceRejectsJunk(t *testing.T) {
	if err := validateAppSource(strings.NewReader("not gzip"), "my_app", DefaultAppSourceLimits()); err == nil {
		t.Fatal("non-gzip data accepted")
	}
	if err := validateAppSource(bytes.NewReader(gzipTar(t, nil)), "my_app", DefaultAppSourceLimits()); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty tar accepted: %v", err)
	}
	for _, name := range []string{"", ".", "..", "a/b"} {
		if err := validateAppSource(bytes.NewReader(gzipTar(t, nil)), name, DefaultAppSourceLimits()); err == nil {
			t.Fatalf("app name %q accepted", name)
		}
	}
}
