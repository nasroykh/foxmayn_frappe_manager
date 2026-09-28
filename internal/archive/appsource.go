package archive

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// AppSourceLimits bound the validation of one archived app. An app's source
// without node_modules is tens of megabytes; frappe itself with a full git
// history is under 2 GiB.
type AppSourceLimits struct {
	MaxEntries int
	MaxBytes   int64
}

// DefaultAppSourceLimits are the caps ValidateAppSource applies when none are given.
func DefaultAppSourceLimits() AppSourceLimits {
	return AppSourceLimits{MaxEntries: 500_000, MaxBytes: 8 << 30}
}

// ValidateAppSource checks a gzipped tar of one app before it is unpacked
// inside a bench.
//
// The tarball is untrusted — it came out of an archive that moved between
// machines — and it is unpacked by tar inside the frappe container, whose
// ./workspace is a bind mount of the host. So every entry must stay inside
// the app's own directory: no absolute paths, no ".." components, no link
// whose target leaves the app, and nothing but files, directories and links.
// Links are allowed, unlike in the archive itself, because app repositories
// legitimately contain them.
//
// This does not make the app's code trustworthy. Restoring it installs it and
// runs it; validation only guarantees that unpacking it writes where it says.
func ValidateAppSource(file, app string, lim AppSourceLimits) error {
	d := DefaultAppSourceLimits()
	if lim.MaxEntries <= 0 {
		lim.MaxEntries = d.MaxEntries
	}
	if lim.MaxBytes <= 0 {
		lim.MaxBytes = d.MaxBytes
	}

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return validateAppSource(f, app, lim)
}

func validateAppSource(r io.Reader, app string, lim AppSourceLimits) error {
	if app == "" || strings.ContainsAny(app, `/\`) || app == "." || app == ".." {
		return fmt.Errorf("invalid app name %q", app)
	}
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("source of app %s is not gzip data: %w", app, err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	var entries int
	var total int64
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("source of app %s is damaged: %w", app, err)
		}
		entries++
		if entries > lim.MaxEntries {
			return fmt.Errorf("source of app %s has more than %d entries — refusing to unpack it", app, lim.MaxEntries)
		}

		name, err := appEntryName(hdr.Name, app)
		if err != nil {
			return err
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
		case tar.TypeReg:
			if hdr.Size < 0 {
				return fmt.Errorf("source of app %s: %q declares a negative size", app, hdr.Name)
			}
			total += hdr.Size
			if total > lim.MaxBytes {
				return fmt.Errorf("source of app %s expands to more than %d bytes — refusing to unpack it", app, lim.MaxBytes)
			}
		case tar.TypeSymlink:
			// Resolved against the link's own directory, as the filesystem will.
			if path.IsAbs(hdr.Linkname) {
				return fmt.Errorf("source of app %s: link %q points at the absolute path %q", app, hdr.Name, hdr.Linkname)
			}
			if !withinApp(path.Join(path.Dir(name), hdr.Linkname), app) {
				return fmt.Errorf("source of app %s: link %q points outside the app (%q)", app, hdr.Name, hdr.Linkname)
			}
		case tar.TypeLink:
			// Hard link targets are archive paths, not relative ones.
			if _, err := appEntryName(hdr.Linkname, app); err != nil {
				return fmt.Errorf("hard link %q: %w", hdr.Name, err)
			}
		default:
			return fmt.Errorf("source of app %s: %q has unsupported type %q", app, hdr.Name, string(rune(hdr.Typeflag)))
		}
	}
	if entries == 0 {
		return fmt.Errorf("source of app %s is empty", app)
	}
	return nil
}

// appEntryName validates one tar path and returns it cleaned. It must lie at
// or under the app's own directory.
func appEntryName(name, app string) (string, error) {
	n := strings.ReplaceAll(name, `\`, "/")
	if n == "" {
		return "", fmt.Errorf("source of app %s contains an entry with an empty name", app)
	}
	if strings.HasPrefix(n, "/") || (len(n) >= 2 && n[1] == ':') {
		return "", fmt.Errorf("source of app %s: %q is an absolute path", app, name)
	}
	for _, part := range strings.Split(n, "/") {
		if part == ".." {
			return "", fmt.Errorf("source of app %s: %q escapes the app directory", app, name)
		}
	}
	clean := path.Clean(n)
	if !withinApp(clean, app) {
		return "", fmt.Errorf("source of app %s: %q is outside the app directory", app, name)
	}
	return clean, nil
}

// withinApp reports whether a cleaned relative path is the app directory or
// lies under it.
func withinApp(p, app string) bool {
	p = path.Clean(p)
	return p == app || strings.HasPrefix(p, app+"/")
}
