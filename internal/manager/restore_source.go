package manager

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/agecrypt"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// DefaultMaxDownloadBytes caps a downloaded archive. It is a sanity limit
// against a server that streams forever, not a quota: a real archive is
// orders of magnitude smaller than the database it holds.
const DefaultMaxDownloadBytes int64 = 64 << 30

// TargetSourceScheme starts a restore source that names a file on a configured
// backup target: target://NAME/BENCH[/FILE]. A URL-style prefix, because a bare
// "name:path" would collide with a Windows drive ("C:\backups\x.ffm.tar") and a
// bare word with a bench name.
const TargetSourceScheme = "target://"

// restoreStallTimeout is how long a download may go without a single byte
// before it is abandoned. A variable so the tests do not wait two minutes.
var restoreStallTimeout = 2 * time.Minute

// restoreHTTPClient builds the client a URL source is fetched with. A variable
// so the tests can trust their own TLS server's certificate; production code
// verifies against the system roots, with no way to turn that off.
var restoreHTTPClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DisableCompression = true
	tr.ResponseHeaderTimeout = 60 * time.Second
	tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: tr}
}

type sourceKind int

const (
	sourceLocal sourceKind = iota
	sourceHTTPS
	sourceTarget
)

// restoreSource is the parsed first argument of `ffm restore`.
type restoreSource struct {
	kind sourceKind
	raw  string
	u    *url.URL
	// target, bench and file name a file on a backup target. file is empty for
	// "the newest archive of bench".
	target, bench, file string
}

var urlSchemeRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]+://`)
var plainFileNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var targetNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// parseRestoreSource classifies the archive argument. Anything that does not
// look like a URL is a local path, exactly as before.
func parseRestoreSource(raw string) (restoreSource, error) {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "http://"):
		return restoreSource{}, errors.New("refusing a plain http:// source: the archive holds every secret of the site. " +
			"Serve it over https:// (or put it on a backup target)")
	case strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(raw)
		if err != nil {
			var ue *url.Error
			if errors.As(err, &ue) {
				err = ue.Err
			}
			return restoreSource{}, fmt.Errorf("the source URL is malformed: %w", err)
		}
		if u.Host == "" {
			return restoreSource{}, errors.New("the source URL has no host")
		}
		if u.User != nil {
			return restoreSource{}, errors.New("the source URL carries a user name or password. ffm does not send " +
				"credentials in a URL: use a presigned URL, or a backup target (target://NAME/BENCH)")
		}
		return restoreSource{kind: sourceHTTPS, raw: raw, u: u}, nil
	case strings.HasPrefix(lower, TargetSourceScheme):
		rest := raw[len(TargetSourceScheme):]
		parts := strings.Split(rest, "/")
		usage := fmt.Errorf("expected %sNAME/BENCH[/FILE], with NAME from 'ffm backup target list'", TargetSourceScheme)
		if len(parts) < 2 || len(parts) > 3 {
			return restoreSource{}, usage
		}
		src := restoreSource{kind: sourceTarget, raw: raw, target: parts[0], bench: parts[1]}
		if len(parts) == 3 {
			src.file = parts[2]
		}
		if !targetNameRe.MatchString(src.target) {
			return restoreSource{}, fmt.Errorf("%q is not a backup target name: %w", src.target, usage)
		}
		if err := bench.ValidateName(src.bench); err != nil {
			return restoreSource{}, fmt.Errorf("%w: %w", err, usage)
		}
		if len(parts) == 3 && (src.file == "" || src.file == "." || src.file == ".." ||
			strings.ContainsAny(src.file, `\`) || !strings.HasSuffix(src.file, ".ffm.tar"+agecrypt.Ext)) {
			return restoreSource{}, fmt.Errorf("%q is not an archive file name (backup targets hold <bench>_<time>.ffm.tar.age): %w",
				src.file, usage)
		}
		return src, nil
	case urlSchemeRe.MatchString(raw):
		scheme := raw[:strings.Index(raw, "://")]
		return restoreSource{}, fmt.Errorf("unsupported source scheme %q. Restore reads an https:// URL, a local path, "+
			"or a file on a backup target: add the bucket with 'ffm backup target add' and use %sNAME/BENCH",
			scheme, TargetSourceScheme)
	}
	return restoreSource{kind: sourceLocal, raw: raw}, nil
}

// redactURL is how a URL is ever shown: scheme, host and the file name only. The
// query string, user info and fragment of a presigned URL are credentials, and
// the path may be.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" {
		base = ""
	}
	return u.Scheme + "://" + u.Host + "/…/" + base
}

// urlFileName is the local name a downloaded archive gets: the last path
// element when it is plain, else a fixed one.
func urlFileName(u *url.URL) string {
	base := path.Base(u.Path)
	if len(base) > 0 && len(base) <= 200 && plainFileNameRe.MatchString(base) {
		return base
	}
	return "download.ffm.tar"
}

// stalledReader cancels a request when the body goes quiet.
type stalledReader struct {
	r     io.Reader
	timer *time.Timer
	d     time.Duration
}

func (s *stalledReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	s.timer.Reset(s.d)
	return n, err
}

// httpGet fetches u into w. max (> 0) is the most it will accept, space (when
// not nil) is asked whether a body of the announced size fits. It returns the
// bytes written.
//
// It never lets the URL's query string reach an error or a log line.
func httpGet(ctx context.Context, u *url.URL, w io.Writer, max int64, space func(int64) error) (int64, error) {
	base := restoreHTTPClient()
	client := *base
	client.Timeout = 0 // the body of a big archive takes as long as it takes
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("stopped after 5 redirects")
		}
		if req.URL.Scheme != "https" {
			return fmt.Errorf("the server redirected to a non-https URL (%s): refusing", redactURL(req.URL))
		}
		if req.URL.User != nil {
			return errors.New("the server redirected to a URL with credentials in it: refusing")
		}
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(restoreStallTimeout, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("build the request for %s: %w", redactURL(u), unwrapURLError(err))
	}
	// Raw bytes: an archive is a tar, and a transparent gunzip would break the
	// length check below.
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("User-Agent", "ffm-restore")

	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return 0, fmt.Errorf("download of %s stopped: no data for %s or cancelled", redactURL(u), restoreStallTimeout)
		}
		return 0, fmt.Errorf("fetch %s: %w", redactURL(u), unwrapURLError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("fetch %s: the server answered %s", redactURL(u), resp.Status)
	}
	if max > 0 && resp.ContentLength > max {
		return 0, fmt.Errorf("%s is %s, over the %s download limit", redactURL(u),
			humanBytes(resp.ContentLength), humanBytes(max))
	}
	if space != nil && resp.ContentLength > 0 {
		if err := space(resp.ContentLength); err != nil {
			return 0, err
		}
	}

	var body io.Reader = &stalledReader{r: resp.Body, timer: stall, d: restoreStallTimeout}
	if max > 0 {
		// One byte past the cap tells "exactly max" from "more than max", which
		// a chunked response (no Content-Length) can otherwise hide.
		body = io.LimitReader(body, max+1)
	}
	n, err := io.Copy(w, body)
	if err != nil {
		if ctx.Err() != nil {
			return n, fmt.Errorf("download of %s stopped after %s: no data for %s or cancelled",
				redactURL(u), humanBytes(n), restoreStallTimeout)
		}
		return n, fmt.Errorf("download of %s broke after %s: %w", redactURL(u), humanBytes(n), unwrapURLError(err))
	}
	if max > 0 && n > max {
		return n, fmt.Errorf("%s is larger than the %s download limit", redactURL(u), humanBytes(max))
	}
	if resp.ContentLength >= 0 && n != resp.ContentLength {
		return n, fmt.Errorf("download of %s is truncated: got %d of %d bytes", redactURL(u), n, resp.ContentLength)
	}
	if n == 0 {
		return n, fmt.Errorf("%s is empty", redactURL(u))
	}
	return n, nil
}

// unwrapURLError drops the *url.Error wrapper, which carries the full request
// URL (query string included) in its text.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// resolveRestoreSource turns the archive argument into a local file. For a
// remote source it downloads into a private temporary directory under the
// backups directory and returns a cleanup that deletes it (unless
// KeepDownload).
func (s *Service) resolveRestoreSource(in RestoreInput, pw ProgressWriter) (string, func(), error) {
	noop := func() {}
	src, err := parseRestoreSource(in.Archive)
	if err != nil {
		return "", noop, err
	}
	if src.kind == sourceLocal {
		archivePath, err := filepath.Abs(in.Archive)
		if err != nil {
			return "", noop, err
		}
		if _, err := os.Stat(archivePath); err != nil {
			return "", noop, fmt.Errorf("archive %s: %w", in.Archive, err)
		}
		return archivePath, noop, nil
	}
	if !in.AllowRemote {
		return "", noop, errors.New("restoring from a URL or backup target is a command-line feature; download the archive first")
	}
	ctx := in.Context
	if ctx == nil {
		ctx = context.Background()
	}
	maxBytes := in.MaxDownloadBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxDownloadBytes
	}

	// A bench name given up front can be checked before a byte is fetched.
	if in.TargetName != "" {
		if err := bench.ValidateNewName(in.TargetName); err != nil {
			return "", noop, err
		}
		if problems := s.nameCollisions(in.TargetName); len(problems) > 0 {
			return "", noop, problemsError(problems)
		}
	}

	if err := os.MkdirAll(config.BackupsDir(), 0o700); err != nil {
		return "", noop, err
	}
	dir, err := os.MkdirTemp(config.BackupsDir(), ".download-")
	if err != nil {
		return "", noop, err
	}
	cleanup := func() {
		if in.KeepDownload {
			pw.Printf("Kept the downloaded archive in %s\n", dir)
			return
		}
		os.RemoveAll(dir)
	}
	fail := func(err error) (string, func(), error) {
		os.RemoveAll(dir)
		return "", noop, err
	}
	space := func(size int64) error {
		if in.SkipSpaceCheck {
			return nil
		}
		free, ok := freeBytes(dir)
		if ok && uint64(size)+(64<<20) > free {
			return fmt.Errorf("not enough space in %s to download %s (%s free; use --skip-space-check to try anyway)",
				config.BackupsDir(), humanBytes(size), humanBytes(int64(free)))
		}
		return nil
	}

	var local string
	switch src.kind {
	case sourceTarget:
		local, err = pullArchiveTo(ctx, src.target, src.bench, src.file, pullOptions{
			dir:      func() (string, error) { return dir, nil },
			maxBytes: maxBytes,
			space:    space,
		}, pw)
		if err != nil {
			return fail(err)
		}
	case sourceHTTPS:
		local, err = s.fetchURLArchive(ctx, src.u, dir, maxBytes, space, in, pw)
		if err != nil {
			return fail(err)
		}
	}
	if in.DryRun {
		fmt.Fprintln(pw.Stderr(), "note: --dry-run downloaded the archive to check it; the download is deleted afterwards.")
	}
	return local, cleanup, nil
}

// fetchURLArchive downloads an https archive into dir, with its header sidecar
// when it is age-encrypted.
func (s *Service) fetchURLArchive(ctx context.Context, u *url.URL, dir string, maxBytes int64,
	space func(int64) error, in RestoreInput, pw ProgressWriter) (string, error) {

	name := urlFileName(u)
	local := filepath.Join(dir, name)

	if agecrypt.IsEncrypted(name) {
		// The header is a few hundred bytes. Fetching it first means a wrong URL,
		// a missing sidecar or a name collision is found before the archive's
		// gigabytes are.
		hu, err := headerURLFor(u, in.HeaderURL)
		if err != nil {
			return "", err
		}
		pw.Step("Fetching the archive header")
		var buf bytes.Buffer
		if _, err := httpGet(ctx, hu, &buf, maxHeaderBytes, nil); err != nil {
			return "", fmt.Errorf("%w\nan age-encrypted archive needs its header sidecar (<archive>.header.json) "+
				"next to it; --header-url says where it is if it is elsewhere", err)
		}
		header, err := ParseHeader(buf.Bytes())
		if err != nil {
			return "", fmt.Errorf("the header at %s: %w", redactURL(hu), err)
		}
		target := in.TargetName
		if target == "" {
			target = header.BenchName
		}
		if err := bench.ValidateNewName(target); err != nil {
			return "", err
		}
		if in.TargetName == "" {
			if problems := s.nameCollisions(target); len(problems) > 0 {
				return "", problemsError(problems)
			}
		}
		if err := os.WriteFile(sidecarPath(local), buf.Bytes(), 0o600); err != nil {
			return "", err
		}
	}

	pw.Step("Downloading " + redactURL(u))
	f, err := os.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	n, err := httpGet(ctx, u, f, maxBytes, space)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	pw.Printf("Downloaded %s.\n", humanBytes(n))
	return local, nil
}

// sourceLabel is how the plan names the archive: the path for a local file, and
// for a download the source it came from (the local copy is deleted afterwards,
// and a URL is only ever shown redacted).
func sourceLabel(in RestoreInput, local string) string {
	src, err := parseRestoreSource(in.Archive)
	if err != nil {
		return local
	}
	switch src.kind {
	case sourceHTTPS:
		return redactURL(src.u)
	case sourceTarget:
		return src.raw
	}
	return local
}

// headerURLFor is where the header sidecar of an encrypted https archive is:
// the explicit override, else the archive URL with ".header.json" appended to
// its path (any query string is kept).
func headerURLFor(archiveURL *url.URL, override string) (*url.URL, error) {
	if override != "" {
		src, err := parseRestoreSource(override)
		if err != nil {
			return nil, fmt.Errorf("--header-url: %w", err)
		}
		if src.kind != sourceHTTPS {
			return nil, errors.New("--header-url must be an https:// URL")
		}
		return src.u, nil
	}
	h := *archiveURL
	h.Path += ".header.json"
	h.RawPath = ""
	return &h, nil
}
