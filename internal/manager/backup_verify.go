package manager

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/agecrypt"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/backuptarget"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// VerifyInput checks that an archive can be restored.
type VerifyInput struct {
	// Archive is a local archive path. Alternatively Target and Bench name
	// the newest archive of a bench on a backup target.
	Archive string
	Target  string
	Bench   string
	// Identity decrypts an encrypted archive (default $FFM_AGE_IDENTITY_FILE).
	Identity string
	// Restore also restores the archive into a throwaway bench, checks that
	// its site answers, and deletes it. Minutes instead of seconds.
	Restore bool
}

// VerifyResult describes a verified archive.
type VerifyResult struct {
	Archive   string
	Header    Header
	Members   int
	Bytes     int64
	Encrypted bool
	// DumpChecked is false when the dump is encrypted by Frappe itself
	// (GPG), which ffm cannot read without the site's key.
	DumpChecked bool
	// Restored names the throwaway bench when Restore ran.
	Restored string
	Took     time.Duration
}

// VerifyBackup proves an archive is restorable: it decrypts it (age
// authenticates every byte), unpacks it with the same guards a restore uses,
// checks every member against the manifest's SHA-256, and checks that the
// database dump holds Frappe's __Auth table. With Restore it then restores
// the archive into a throwaway bench, asks the site for /api/method/ping and
// deletes the bench again.
func (s *Service) VerifyBackup(in VerifyInput, pw ProgressWriter) (VerifyResult, error) {
	if pw == nil {
		pw = CLIProgress{}
	}
	start := s.clock()
	res := VerifyResult{}
	if err := os.MkdirAll(config.BackupsDir(), 0o700); err != nil {
		return res, err
	}
	work, err := os.MkdirTemp(config.BackupsDir(), ".verify-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)

	src := in.Archive
	if in.Target != "" {
		if in.Bench == "" {
			return res, fmt.Errorf("verifying from a target needs the bench")
		}
		pw.Step(fmt.Sprintf("Fetching the newest archive of %q from %s", in.Bench, in.Target))
		src, err = fetchNewest(in.Target, in.Bench, work)
		if err != nil {
			return res, err
		}
	}
	if src == "" {
		return res, fmt.Errorf("an archive (or --target with a bench) is required")
	}
	res.Archive = src
	plain := src
	if agecrypt.IsEncrypted(src) {
		res.Encrypted = true
		pw.Step("Decrypting (age checks every chunk)")
		p, cleanup, err := decryptForRestore(src, in.Identity)
		if err != nil {
			return res, err
		}
		defer cleanup()
		plain = p
	}

	pw.Step("Unpacking and checking every member against the manifest")
	raw, err := archive.PeekHeader(plain)
	if err != nil {
		return res, err
	}
	if res.Header, err = ParseHeader(raw); err != nil {
		return res, err
	}
	staging := filepath.Join(work, "x")
	ex, err := archive.Extract(plain, staging, archive.Limits{})
	if err != nil {
		return res, err
	}
	manifestRaw, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(archive.ManifestName)))
	if err != nil {
		return res, fmt.Errorf("no manifest: the archive is truncated: %w", err)
	}
	m, err := ParseManifest(manifestRaw)
	if err != nil {
		return res, err
	}
	if err := verifyMembers(m, ex); err != nil {
		return res, err
	}
	res.Members = len(m.Members)
	for _, mem := range m.Members {
		res.Bytes += mem.Size
	}

	// gzip is how the member is stored, not a reason to skip it; only a dump
	// Frappe encrypted itself (GPG) cannot be read here.
	if dbMemberEncoding(m) != archive.EncodingGPG {
		pw.Step("Checking that the database dump holds Frappe's __Auth table")
		ok, err := dumpHasAuthTable(staging, m)
		if err != nil {
			return res, err
		}
		if !ok {
			return res, fmt.Errorf("the database dump does not contain Frappe's __Auth table: it is empty or truncated")
		}
		res.DumpChecked = true
	}

	if in.Restore {
		name, err := s.verifyByRestore(plain, res.Header, pw)
		res.Restored = name
		if err != nil {
			return res, err
		}
	}
	res.Took = s.clock().Sub(start)
	return res, nil
}

// dumpHasAuthTable scans the gzip dump for "__Auth" without unpacking it.
func dumpHasAuthTable(staging string, m Manifest) (bool, error) {
	for _, mem := range m.Members {
		if !strings.HasPrefix(mem.Path, archive.Prefix+"/db/") {
			continue
		}
		f, err := os.Open(filepath.Join(staging, filepath.FromSlash(mem.Path)))
		if err != nil {
			return false, err
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			return false, fmt.Errorf("the database dump is not gzip: %w", err)
		}
		return streamContains(zr, []byte("__Auth"))
	}
	return false, fmt.Errorf("the archive has no database dump")
}

// streamContains reports whether needle occurs in r, reading it in chunks.
func streamContains(r io.Reader, needle []byte) (bool, error) {
	buf := make([]byte, 1<<20)
	carry := []byte{}
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append(carry, buf[:n]...)
			if bytes.Contains(chunk, needle) {
				return true, nil
			}
			keep := len(needle) - 1
			if len(chunk) < keep {
				keep = len(chunk)
			}
			carry = append([]byte{}, chunk[len(chunk)-keep:]...)
		}
		if err == io.EOF {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
}

// fetchNewest downloads a bench's newest archive and sidecar from a target
// into dir.
func fetchNewest(targetName, bench, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
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
	for _, r := range remote {
		if r.Err != nil {
			continue
		}
		local := filepath.Join(dir, path.Base(r.Key))
		if err := download(ctx, d, r.Key, local); err != nil {
			return "", err
		}
		if st, err := os.Stat(local); err != nil || st.Size() != r.Size {
			return "", fmt.Errorf("download of %s is incomplete", path.Base(r.Key))
		}
		return local, nil
	}
	return "", fmt.Errorf("no archives of %q on %s", bench, targetName)
}

// verifyByRestore restores the archive into a throwaway bench, checks that
// the site answers /api/method/ping, and deletes the bench whatever happened.
func (s *Service) verifyByRestore(plain string, h Header, pw ProgressWriter) (string, error) {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	name := "verify-" + hex.EncodeToString(b)
	in := RestoreInput{Archive: plain, TargetName: name, WithFiles: true, ReallocatePorts: true}
	if h.Mode == "prod" {
		// A production archive restores as production; it never asks for a
		// certificate for a name nobody resolves.
		in.Domain, in.NoSSL = name+".verify.invalid", true
	}
	pw.Step(fmt.Sprintf("Restoring into the throwaway bench %q", name))
	defer func() {
		if _, err := s.GetBench(name); err == nil {
			pw.Step(fmt.Sprintf("Deleting %q", name))
			if err := s.Delete(DeleteInput{Name: name, NoBackup: true}, pw); err != nil {
				fmt.Fprintf(pw.Stderr(), "warning: could not delete the throwaway bench %q: %v\n", name, err)
			}
		}
	}()
	if err := s.Restore(in, pw); err != nil {
		return name, fmt.Errorf("restore: %w", err)
	}
	rb, err := s.GetBench(name)
	if err != nil {
		return name, err
	}
	pw.Step("Asking the restored site for /api/method/ping")
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://localhost:%d/api/method/ping", rb.WebPort), nil)
	if rb.IsProd() {
		req.Host = rb.Domain
	}
	client := &http.Client{Timeout: 15 * time.Second}
	var last error
	for i := 0; i < 12; i++ {
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("pong")) {
				return name, nil
			}
			err = fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		last = err
		time.Sleep(5 * time.Second)
	}
	return name, fmt.Errorf("the restored site does not answer /api/method/ping: %w", last)
}
