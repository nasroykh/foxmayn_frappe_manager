// Package archive reads and writes ffm backup archives.
//
// An archive is a plain uncompressed tar. Large members are compressed
// individually by whoever produced them (Frappe already gzips its database dump
// and file tarballs), so the container itself stays streamable: a reader can
// reach the first member without inflating anything, and a writer never has to
// buffer a multi-gigabyte member in memory.
//
// Two ordering rules give the format its integrity properties, and both are
// enforced here rather than by convention:
//
//   - The FIRST member is the header, so a reader can decide whether an archive
//     is worth touching in O(header) instead of O(archive).
//   - The LAST member is the manifest. A writer that crashed cannot have written
//     it, so its absence IS the definition of a truncated archive — no sentinel
//     value, no length prefix, no heuristic.
package archive

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Prefix is the directory every member path sits under, so that extracting an
// ffm archive with plain `tar xf` produces one directory rather than scattering
// files into the working directory.
const Prefix = "ffm-backup"

// HeaderName and ManifestName are the reserved first and last members.
const (
	HeaderName   = Prefix + "/header.json"
	ManifestName = Prefix + "/manifest.json"
)

// Encoding values recorded for a member. Readers MUST dispatch on these rather
// than on the file extension: a Frappe backup taken with System Settings
// encrypt_backup on is GPG-encrypted while keeping the name "…-database.sql.gz",
// so the extension actively lies.
const (
	EncodingNone = ""
	EncodingGzip = "gzip"
	EncodingGPG  = "gpg"
)

// Member describes one file stored in an archive.
type Member struct {
	// Path is the member's path inside the archive, including Prefix.
	Path string `json:"path"`
	// Size is the stored byte count (after any Encoding was applied).
	Size int64 `json:"size"`
	// SHA256 is the hex digest of the stored bytes.
	SHA256 string `json:"sha256"`
	// Encoding is how the bytes are encoded: EncodingNone, EncodingGzip or
	// EncodingGPG. Never inferred from Path.
	Encoding string `json:"encoding,omitempty"`
}

// Writer builds an archive at a destination path.
//
// Everything is written to "<dest>.partial" and renamed into place by Close, so
// an interrupted backup never leaves a file that looks complete. The temporary
// lives in the destination directory rather than the system temp dir because
// rename is only atomic within a filesystem.
type Writer struct {
	dest    string
	partial string
	f       *os.File
	tw      *tar.Writer
	closed  bool
}

// Create opens a Writer for dest. The caller must call Close on success or
// Abort on failure; a Writer left open leaks the .partial file.
func Create(dest string) (*Writer, error) {
	if dir := filepath.Dir(dest); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create archive dir: %w", err)
		}
	}
	partial := dest + ".partial"
	// 0o600 from the moment of creation, not chmod'ed afterwards: the archive
	// holds the database root password and the site encryption key, so there
	// must be no window in which it is world-readable.
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", partial, err)
	}
	return &Writer{dest: dest, partial: partial, f: f, tw: tar.NewWriter(f)}, nil
}

// Path returns the final destination path.
func (w *Writer) Path() string { return w.dest }

// AddBytes stores an in-memory member. Used for the small JSON members
// (header, manifest, bench record, site config).
func (w *Writer) AddBytes(name string, data []byte, encoding string) (Member, error) {
	return w.AddStream(name, int64(len(data)), strings.NewReader(string(data)), encoding)
}

// AddStream stores exactly size bytes read from r.
//
// tar requires the length up front, so callers streaming a file out of a
// container must stat it there first. A stream that turns out to be a different
// length than declared is a hard error rather than a silently corrupt member:
// the usual cause is the source file changing underneath the backup.
func (w *Writer) AddStream(name string, size int64, r io.Reader, encoding string) (Member, error) {
	if w.closed {
		return Member{}, fmt.Errorf("archive: write after close")
	}
	if size < 0 {
		return Member{}, fmt.Errorf("archive: negative size for %s", name)
	}
	hdr := &tar.Header{
		Name:     name,
		Mode:     0o600,
		Size:     size,
		Typeflag: tar.TypeReg,
		// Format is left at FormatUnknown so archive/tar picks PAX by itself
		// for anything USTAR cannot represent (>8 GiB members, long paths).
	}
	if err := w.tw.WriteHeader(hdr); err != nil {
		return Member{}, fmt.Errorf("archive: write header %s: %w", name, err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w.tw, h), io.LimitReader(r, size))
	if err != nil {
		return Member{}, fmt.Errorf("archive: write %s: %w", name, err)
	}
	if n != size {
		return Member{}, fmt.Errorf("archive: %s declared %d bytes but yielded %d "+
			"(did the file change while it was being read?)", name, size, n)
	}
	return Member{Path: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil)), Encoding: encoding}, nil
}

// Close flushes the tar, fsyncs the file, renames it over the destination and
// fsyncs the parent directory so the rename itself survives a crash.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.tw.Close(); err != nil {
		w.f.Close()
		os.Remove(w.partial)
		return fmt.Errorf("archive: close tar: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		w.f.Close()
		os.Remove(w.partial)
		return fmt.Errorf("archive: sync: %w", err)
	}
	if err := w.f.Close(); err != nil {
		os.Remove(w.partial)
		return fmt.Errorf("archive: close: %w", err)
	}
	if err := os.Rename(w.partial, w.dest); err != nil {
		os.Remove(w.partial)
		return fmt.Errorf("archive: rename into place: %w", err)
	}
	syncDir(filepath.Dir(w.dest))
	return nil
}

// Abort closes and removes the partial file. Safe to call after Close.
func (w *Writer) Abort() {
	if w.closed {
		return
	}
	w.closed = true
	w.tw.Close()
	w.f.Close()
	os.Remove(w.partial)
}

// syncDir fsyncs a directory so a rename inside it is durable. Best-effort:
// some filesystems (and Windows) do not permit opening a directory for sync,
// and a backup is not worth failing over that.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// PeekHeader returns the bytes of the archive's first member without extracting
// anything, and errors if that member is not HeaderName. This is what makes
// preflight cheap on a multi-gigabyte archive.
func PeekHeader(src string) ([]byte, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	tr := tar.NewReader(f)
	hdr, err := tr.Next()
	if err == io.EOF {
		return nil, fmt.Errorf("%s is empty — not an ffm archive", filepath.Base(src))
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w (not a tar archive?)", filepath.Base(src), err)
	}
	if path.Clean(hdr.Name) != HeaderName {
		return nil, fmt.Errorf("%s is not an ffm archive: first member is %q, expected %q",
			filepath.Base(src), hdr.Name, HeaderName)
	}
	// The header is a small JSON document; cap the read so a hostile archive
	// cannot claim a 100 GB header and exhaust memory during preflight.
	return io.ReadAll(io.LimitReader(tr, 1<<20))
}
