package archive

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Limits bound an extraction so that a hostile or corrupt archive cannot
// exhaust the host before anything has been validated. Extraction happens
// during restore preflight, i.e. before the user has been asked to trust the
// archive, so the caps are enforced unconditionally rather than behind a flag.
type Limits struct {
	// MaxEntries caps the number of members. Zero means DefaultLimits' value.
	MaxEntries int
	// MaxBytes caps the total extracted size. Zero means DefaultLimits' value.
	MaxBytes int64
}

// DefaultLimits are generous enough for a real bench (a large ERPNext site with
// years of attachments) and still far below "fills the disk".
func DefaultLimits() Limits {
	return Limits{MaxEntries: 100_000, MaxBytes: 256 << 30} // 256 GiB
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxEntries <= 0 {
		l.MaxEntries = d.MaxEntries
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	return l
}

// ExtractResult reports what an extraction actually observed, as opposed to
// what the archive's manifest claims. The caller compares the two.
type ExtractResult struct {
	// Members maps member path to its observed size and digest.
	Members map[string]Member
	// Order is the member paths in the order they appeared. The caller uses it
	// to verify that the header came first and the manifest last.
	Order []string
	// TotalBytes is the sum of all extracted member sizes.
	TotalBytes int64
}

// ErrNoManifest reports an archive whose last member is not the manifest, which
// means the writer did not finish. It is a distinct error because the remedy —
// take the backup again — differs from every other failure here.
var ErrNoManifest = errors.New("archive is truncated: it has no trailing manifest, " +
	"which means the backup that produced it did not finish")

// Extract unpacks src into destDir, hashing every member as it goes.
func Extract(src, destDir string, lim Limits) (*ExtractResult, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ExtractReader(f, destDir, lim)
}

// ExtractReader unpacks a tar stream into destDir.
//
// Only regular files and directories are extracted. Symlinks, hardlinks, device
// nodes, FIFOs and sockets are rejected outright rather than skipped: ffm never
// writes them, so their presence means the archive is not one ffm produced, and
// a skipped-with-a-warning symlink is exactly how directory-escape bugs ship.
func ExtractReader(r io.Reader, destDir string, lim Limits) (*ExtractResult, error) {
	lim = lim.withDefaults()

	root, err := filepath.Abs(destDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}

	res := &ExtractResult{Members: make(map[string]Member)}
	entries := 0
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// An unexpected EOF mid-stream is the signature of a truncated
			// archive, which ErrNoManifest describes far better than
			// "unexpected EOF" would.
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, ErrNoManifest
			}
			return nil, fmt.Errorf("read archive: %w", err)
		}

		// Counted before the type switch, so directory members are subject to the
		// cap too. They carry no bytes, so a tar of nothing but directory
		// headers would otherwise slip past both limits and still create
		// millions of inodes during a preflight that has not yet decided
		// whether to trust the archive at all.
		entries++
		if entries > lim.MaxEntries {
			return nil, fmt.Errorf("archive has more than %d members — refusing to extract", lim.MaxEntries)
		}

		rel, err := safeName(hdr.Name)
		if err != nil {
			return nil, err
		}
		target := filepath.Join(root, filepath.FromSlash(rel))
		if !withinRoot(root, target) {
			return nil, fmt.Errorf("archive member %q escapes the extraction directory", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return nil, err
			}
			continue
		case tar.TypeReg:
			// handled below
		default:
			return nil, fmt.Errorf("archive member %q has unsupported type %q — "+
				"ffm archives contain only regular files and directories",
				hdr.Name, string(rune(hdr.Typeflag)))
		}

		if hdr.Size < 0 {
			return nil, fmt.Errorf("archive member %q declares a negative size", hdr.Name)
		}
		if res.TotalBytes+hdr.Size > lim.MaxBytes {
			return nil, fmt.Errorf("archive expands to more than %d bytes — refusing to extract", lim.MaxBytes)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return nil, err
		}
		m, err := writeMember(target, tr, hdr.Size, rel)
		if err != nil {
			return nil, err
		}
		res.Members[rel] = m
		res.Order = append(res.Order, rel)
		res.TotalBytes += hdr.Size
	}

	if len(res.Order) == 0 {
		return nil, fmt.Errorf("archive is empty")
	}
	if res.Order[len(res.Order)-1] != ManifestName {
		return nil, ErrNoManifest
	}
	if res.Order[0] != HeaderName {
		return nil, fmt.Errorf("archive is malformed: first member is %q, expected %q",
			res.Order[0], HeaderName)
	}
	return res, nil
}

// writeMember copies exactly size bytes into target, hashing as it goes.
func writeMember(target string, r io.Reader, size int64, rel string) (Member, error) {
	f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return Member{}, err
	}
	defer f.Close()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, size))
	if err != nil {
		// The stream ending inside a member means the archive was cut short,
		// which ErrNoManifest explains — and tells the user what to do about
		// it — far better than a bare "unexpected EOF".
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return Member{}, ErrNoManifest
		}
		return Member{}, fmt.Errorf("extract %s: %w", rel, err)
	}
	if n != size {
		return Member{}, ErrNoManifest
	}
	return Member{
		Path:   rel,
		Size:   n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// safeName validates a tar member path and returns it in slash form.
//
// Rejected: absolute paths, Windows drive letters, any ".." component, and
// anything outside the archive's Prefix directory. Checking for ".." on the
// CLEANED path is what closes the "a/../../b" class of bypass.
func safeName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("archive contains a member with an empty name")
	}
	// Normalise separators before any check so a Windows-style path cannot slip
	// past a check that only looks for '/'.
	n := strings.ReplaceAll(name, `\`, "/")
	if strings.HasPrefix(n, "/") {
		return "", fmt.Errorf("archive member %q is an absolute path", name)
	}
	if len(n) >= 2 && n[1] == ':' {
		return "", fmt.Errorf("archive member %q is an absolute path", name)
	}
	clean := path.Clean(n)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("archive member %q escapes the extraction directory", name)
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive member %q escapes the extraction directory", name)
		}
	}
	if clean != Prefix && !strings.HasPrefix(clean, Prefix+"/") {
		return "", fmt.Errorf("archive member %q is outside the %q directory — "+
			"this is not an ffm archive", name, Prefix)
	}
	return clean, nil
}

// withinRoot reports whether target is inside root. Both must already be
// absolute and lexically clean.
func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
