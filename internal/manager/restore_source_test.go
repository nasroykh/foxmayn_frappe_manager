package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx/fakeexec"
)

func TestParseRestoreSource(t *testing.T) {
	tests := []struct {
		in      string
		kind    sourceKind
		wantErr string
	}{
		{in: "backup.ffm.tar", kind: sourceLocal},
		{in: "/abs/path/b.ffm.tar", kind: sourceLocal},
		{in: `C:\Users\me\b.ffm.tar`, kind: sourceLocal},
		{in: "C:/Users/me/b.ffm.tar", kind: sourceLocal},
		{in: "mybench", kind: sourceLocal},
		{in: "https://files.example.com/a/b.ffm.tar?X-Amz-Signature=abc", kind: sourceHTTPS},
		{in: "HTTPS://files.example.com/b.ffm.tar", kind: sourceHTTPS},
		{in: "http://files.example.com/b.ffm.tar", wantErr: "plain http"},
		{in: "https://user:pw@files.example.com/b.ffm.tar", wantErr: "user name or password"},
		{in: "https:///b.ffm.tar", wantErr: "no host"},
		{in: "s3://bucket/b.ffm.tar", wantErr: "unsupported source scheme"},
		{in: "sftp://host/b.ffm.tar", wantErr: "unsupported source scheme"},
		{in: "target://nas/alpha", kind: sourceTarget},
		{in: "target://nas/alpha/alpha_20260101T000000Z.ffm.tar.age", kind: sourceTarget},
		{in: "target://nas", wantErr: "expected target://NAME/BENCH"},
		{in: "target://Nas/alpha", wantErr: "not a backup target name"},
		{in: "target://nas/9bad_name", wantErr: "invalid"},
		{in: "target://nas/alpha/../x.ffm.tar.age", wantErr: "expected target://NAME/BENCH"},
		{in: "target://nas/alpha/plain.ffm.tar", wantErr: "not an archive file name"},
		{in: "target://nas/alpha/..", wantErr: "not an archive file name"},
	}
	for _, tt := range tests {
		got, err := parseRestoreSource(tt.in)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%q: error %v, want %q", tt.in, err, tt.wantErr)
			}
			continue
		}
		if err != nil || got.kind != tt.kind {
			t.Errorf("%q: kind %v err %v, want %v", tt.in, got.kind, err, tt.kind)
		}
	}
	src, _ := parseRestoreSource("target://nas/alpha/alpha_x.ffm.tar.age")
	if src.target != "nas" || src.bench != "alpha" || src.file != "alpha_x.ffm.tar.age" {
		t.Errorf("target parts: %+v", src)
	}
	if src, _ := parseRestoreSource("target://nas/alpha"); src.file != "" {
		t.Errorf("a bench-only reference has a file: %+v", src)
	}
}

func TestRedactURLKeepsNoQueryNoUserInfoNoDirs(t *testing.T) {
	u, _ := url.Parse("https://bob:pw@host.example/secret-token/dir/b.ffm.tar?sig=ABC#frag")
	got := redactURL(u)
	for _, bad := range []string{"ABC", "bob", "pw", "secret-token", "frag", "?"} {
		if strings.Contains(got, bad) {
			t.Errorf("redactURL leaked %q: %s", bad, got)
		}
	}
	if !strings.Contains(got, "host.example") || !strings.HasSuffix(got, "b.ffm.tar") {
		t.Errorf("redactURL = %s", got)
	}
}

// tlsFixture serves handler over TLS and points the download client at it.
func tlsFixture(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	old := restoreHTTPClient
	restoreHTTPClient = func() *http.Client { return srv.Client() }
	t.Cleanup(func() { restoreHTTPClient = old })
	return srv
}

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestHTTPGet(t *testing.T) {
	body := bytes.Repeat([]byte("0123456789"), 100) // 1000 bytes

	t.Run("ok", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Accept-Encoding"); got != "identity" {
				t.Errorf("Accept-Encoding = %q", got)
			}
			w.Write(body)
		}))
		var buf bytes.Buffer
		n, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a.ffm.tar"), &buf, 1<<20, nil)
		if err != nil || n != 1000 || !bytes.Equal(buf.Bytes(), body) {
			t.Fatalf("n=%d err=%v", n, err)
		}
	})

	t.Run("404 does not leak the query string", func(t *testing.T) {
		srv := tlsFixture(t, http.NotFoundHandler())
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/tok/a.ffm.tar?X-Amz-Signature=SIGSECRET"), &bytes.Buffer{}, 1<<20, nil)
		if err == nil || !strings.Contains(err.Error(), "404") {
			t.Fatalf("err = %v", err)
		}
		for _, bad := range []string{"SIGSECRET", "X-Amz", "/tok/"} {
			if strings.Contains(err.Error(), bad) {
				t.Errorf("the error leaks %q: %v", bad, err)
			}
		}
	})

	t.Run("redirect to plain http is refused", func(t *testing.T) {
		var hit atomic.Int32
		plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hit.Add(1)
			w.Write(body)
		}))
		defer plain.Close()
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, plain.URL+"/x.ffm.tar?sig=LEAK", http.StatusFound)
		}))
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a.ffm.tar"), &bytes.Buffer{}, 1<<20, nil)
		if err == nil || !strings.Contains(err.Error(), "non-https") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "LEAK") || hit.Load() != 0 {
			t.Errorf("the plain server was contacted (%d) or leaked: %v", hit.Load(), err)
		}
	})

	t.Run("redirect loop stops", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
		}))
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &bytes.Buffer{}, 1<<20, nil)
		if err == nil || !strings.Contains(err.Error(), "redirects") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a huge Content-Length is refused before the body", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "999999999999")
			w.WriteHeader(200)
			w.Write(body)
		}))
		var buf bytes.Buffer
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &buf, 1<<20, nil)
		if err == nil || !strings.Contains(err.Error(), "download limit") {
			t.Fatalf("err = %v", err)
		}
		if buf.Len() != 0 {
			t.Errorf("%d bytes were written before the refusal", buf.Len())
		}
	})

	t.Run("an unannounced body past the cap is stopped", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush() // chunked: no Content-Length
			for i := 0; i < 50; i++ {
				w.Write(body)
			}
		}))
		var buf bytes.Buffer
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &buf, 5000, nil)
		if err == nil || !strings.Contains(err.Error(), "larger than") {
			t.Fatalf("err = %v", err)
		}
		if buf.Len() > 5001 {
			t.Errorf("wrote %d bytes past a 5000 cap", buf.Len())
		}
	})

	t.Run("a truncated body is an error", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(200)
			w.Write(body[:100])
		}))
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &bytes.Buffer{}, 1<<20, nil)
		if err == nil || !strings.Contains(err.Error(), "broke") && !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("an empty body is an error", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		if _, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &bytes.Buffer{}, 1<<20, nil); err == nil ||
			!strings.Contains(err.Error(), "empty") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("a stalled server is abandoned", func(t *testing.T) {
		old := restoreStallTimeout
		restoreStallTimeout = 150 * time.Millisecond
		defer func() { restoreStallTimeout = old }()
		release := make(chan struct{})
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(200)
			w.Write(body[:10])
			w.(http.Flusher).Flush()
			<-release
		}))
		defer close(release)
		start := time.Now()
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &bytes.Buffer{}, 1<<20, nil)
		if err == nil || !strings.Contains(err.Error(), "no data") {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Errorf("took %v", time.Since(start))
		}
	})

	t.Run("cancelling the context stops it", func(t *testing.T) {
		release := make(chan struct{})
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(200)
			w.Write(body[:10])
			w.(http.Flusher).Flush()
			<-release
		}))
		defer close(release)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(100*time.Millisecond, cancel)
		_, err := httpGet(ctx, mustURL(t, srv.URL+"/a"), &bytes.Buffer{}, 1<<20, nil)
		if err == nil {
			t.Fatal("a cancelled download succeeded")
		}
	})

	t.Run("the space check sees the announced size", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(body) }))
		_, err := httpGet(context.Background(), mustURL(t, srv.URL+"/a"), &bytes.Buffer{}, 1<<20,
			func(n int64) error { return fmt.Errorf("no room for %d", n) })
		if err == nil || !strings.Contains(err.Error(), "no room for 1000") {
			t.Fatalf("err = %v", err)
		}
	})
}

// restorableArchive writes an ffm archive that passes every dry-run gate.
func restorableArchive(t *testing.T, dir, bench string) []byte {
	t.Helper()
	m := devManifest()
	m.Header.BenchName, m.Bench.Name = bench, bench
	m.Header.SiteName, m.Site.SiteName = bench+".localhost", bench+".localhost"
	m.Bench.Apps = []string{"https://github.com/frappe/erpnext"}
	m.Secrets.AdminPassword = "Adm1n-Sentinel-77"
	m.Header.CreatedAt = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(dir, bench+".ffm.tar")
	w, err := archive.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	hraw, _ := json.Marshal(m.Header)
	w.AddBytes(archive.HeaderName, hraw, archive.EncodingNone)
	mem, err := w.AddBytes(archive.Prefix+"/db/database.sql.gz", gzipBytes("CREATE TABLE `__Auth` (x int);"), archive.EncodingGzip)
	if err != nil {
		t.Fatal(err)
	}
	m.Members = []archive.Member{mem}
	mraw, _ := json.Marshal(m)
	w.AddBytes(archive.ManifestName, mraw, archive.EncodingNone)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func downloadDirs(t *testing.T) []string {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(os.Getenv("FFM_BACKUPS_DIR"), ".download-*"))
	return left
}

func isolatedDirs(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("FFM_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("FFM_BENCHES_DIR", filepath.Join(root, "benches"))
	t.Setenv("FFM_BACKUPS_DIR", filepath.Join(root, "backups"))
	t.Setenv("FFM_AGE_IDENTITY_FILE", "")
}

func TestRestoreFromURLDryRunDownloadsChecksAndCleans(t *testing.T) {
	isolatedDirs(t)
	fake := fakeexec.Install(t)
	raw := restorableArchive(t, t.TempDir(), "srcbench")
	var gets atomic.Int32
	srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/srcbench.ffm.tar") {
			http.NotFound(w, r)
			return
		}
		w.Write(raw)
	}))
	s := New(false)
	in := RestoreInput{Archive: srv.URL + "/dir/srcbench.ffm.tar?sig=SECRETQ", TargetName: "fromurl",
		DryRun: true, SkipSpaceCheck: true, WithFiles: true, AllowRemote: true}

	// A caller that did not opt in (the dashboard, clone, verify) can never make
	// ffm fetch a URL.
	deny := in
	deny.AllowRemote = false
	if err := s.Restore(deny, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "command-line") {
		t.Errorf("AllowRemote=false: %v", err)
	}
	if gets.Load() != 0 {
		t.Fatal("a URL was fetched without AllowRemote")
	}

	pw := &BufferProgress{}
	if err := s.Restore(in, pw); err != nil {
		t.Fatalf("Restore: %v\nlines: %v", err, pw.Lines)
	}
	out := strings.Join(pw.Lines, "\n")
	if strings.Contains(out, "SECRETQ") {
		t.Errorf("the query string reached the output:\n%s", out)
	}
	for _, want := range []string{"Downloading https://127.0.0.1", "downloaded the archive to check it", "this restore would succeed"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if left := downloadDirs(t); len(left) != 0 {
		t.Errorf("download left behind: %v", left)
	}
	if fake.Called("compose -p") || fake.Called(" build") {
		t.Errorf("a dry run touched docker: %v", fake.Calls())
	}

	// --keep-download keeps it, private.
	keep := in
	keep.KeepDownload = true
	if err := s.Restore(keep, DiscardProgress{}); err != nil {
		t.Fatal(err)
	}
	left := downloadDirs(t)
	if len(left) != 1 {
		t.Fatalf("kept downloads: %v", left)
	}
	if st, _ := os.Stat(left[0]); st.Mode().Perm() != 0o700 {
		t.Errorf("download dir mode %v", st.Mode().Perm())
	}
	if st, err := os.Stat(filepath.Join(left[0], "srcbench.ffm.tar")); err != nil || st.Mode().Perm() != 0o600 {
		t.Errorf("kept file: %v %v", st, err)
	}
}

func TestRestoreFromURLHostileServers(t *testing.T) {
	isolatedDirs(t)
	fakeexec.Install(t)
	s := New(false)
	base := RestoreInput{TargetName: "hostile", DryRun: true, SkipSpaceCheck: true, WithFiles: true, AllowRemote: true}

	t.Run("not an archive", func(t *testing.T) {
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html>login required</html>"))
		}))
		in := base
		in.Archive = srv.URL + "/x.ffm.tar"
		if err := s.Restore(in, DiscardProgress{}); err == nil {
			t.Fatal("an HTML page was accepted as an archive")
		}
		if left := downloadDirs(t); len(left) != 0 {
			t.Errorf("left behind: %v", left)
		}
	})
	t.Run("over the limit", func(t *testing.T) {
		raw := restorableArchive(t, t.TempDir(), "big")
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(raw) }))
		in := base
		in.Archive, in.MaxDownloadBytes = srv.URL+"/x.ffm.tar", 1024
		if err := s.Restore(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "download limit") {
			t.Fatalf("err = %v", err)
		}
		if left := downloadDirs(t); len(left) != 0 {
			t.Errorf("left behind: %v", left)
		}
	})
	t.Run("404", func(t *testing.T) {
		srv := tlsFixture(t, http.NotFoundHandler())
		in := base
		in.Archive = srv.URL + "/x.ffm.tar?token=TOK"
		err := s.Restore(in, DiscardProgress{})
		if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "TOK") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("a taken name is found before the download", func(t *testing.T) {
		var gets atomic.Int32
		srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gets.Add(1) }))
		fakeexec.Install(t, fakeexec.Rule{Match: "volume ls", Stdout: "ffm-hostile_db\n"})
		in := base
		in.Archive = srv.URL + "/x.ffm.tar"
		err := s.Restore(in, DiscardProgress{})
		if err == nil || gets.Load() != 0 {
			t.Fatalf("err = %v, gets = %d", err, gets.Load())
		}
	})
}

func TestRestoreFromURLEncryptedNeedsItsHeader(t *testing.T) {
	isolatedDirs(t)
	fakeexec.Install(t)
	s := New(false)
	var archiveGets atomic.Int32
	hdr, _ := json.Marshal(NewHeader(fullBench(), "s.localhost", "16", "", []string{TierCore}, time.Now()))
	withHeader := true
	srv := tlsFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a_x.ffm.tar.age.header.json":
			if !withHeader {
				http.NotFound(w, r)
				return
			}
			w.Write(hdr)
		case "/a_x.ffm.tar.age":
			archiveGets.Add(1)
			w.Write([]byte("not really age data"))
		default:
			http.NotFound(w, r)
		}
	}))
	in := RestoreInput{Archive: srv.URL + "/a_x.ffm.tar.age", TargetName: "enc", DryRun: true,
		SkipSpaceCheck: true, AllowRemote: true}

	withHeader = false
	err := s.Restore(in, DiscardProgress{})
	if err == nil || !strings.Contains(err.Error(), "header sidecar") || archiveGets.Load() != 0 {
		t.Fatalf("missing header: err = %v, archive gets = %d (must be 0: the header is fetched first)", err, archiveGets.Load())
	}

	withHeader = true
	err = s.Restore(in, DiscardProgress{})
	// The header is fine, so the archive is fetched; then the missing identity
	// stops it — which proves the encrypted path was taken.
	if archiveGets.Load() != 1 || err == nil || !strings.Contains(err.Error(), "--identity") {
		t.Fatalf("with header: gets = %d err = %v", archiveGets.Load(), err)
	}
	if left := downloadDirs(t); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}

	// --header-url for an archive whose URL cannot take a suffix.
	in.HeaderURL = srv.URL + "/a_x.ffm.tar.age.header.json"
	withHeader = false
	if err := s.Restore(in, DiscardProgress{}); err == nil {
		t.Error("expected failure with the header 404")
	}
	in.HeaderURL = "http://insecure/x.header.json"
	if err := s.Restore(in, DiscardProgress{}); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Errorf("http header URL: %v", err)
	}
}

func TestHeaderURLKeepsTheQuery(t *testing.T) {
	h, err := headerURLFor(mustURL(t, "https://h.example/dir/a.ffm.tar.age?sig=1"), "")
	if err != nil || h.String() != "https://h.example/dir/a.ffm.tar.age.header.json?sig=1" {
		t.Errorf("%v %v", h, err)
	}
}

func TestUrlFileName(t *testing.T) {
	for in, want := range map[string]string{
		"https://h/a/b_1.ffm.tar":   "b_1.ffm.tar",
		"https://h/":                "download.ffm.tar",
		"https://h/a/..%2f..%2fetc": "etc",
		"https://h/a/.hidden":       "download.ffm.tar",
		"https://h/a/b c.tar":       "download.ffm.tar",
		"https://h/a/b.ffm.tar.age": "b.ffm.tar.age",
	} {
		if got := urlFileName(mustURL(t, in)); got != want {
			t.Errorf("%s -> %q, want %q", in, got, want)
		}
	}
}
