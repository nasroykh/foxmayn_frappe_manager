package backuptarget

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// exercise runs the contract every destination must keep.
func exercise(t *testing.T, d Destination) {
	t.Helper()
	ctx := context.Background()
	data := bytes.Repeat([]byte("encrypted archive "), 5000)
	if err := d.Put(ctx, "alpha/a.ffm.tar.age", bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "alpha/a.ffm.tar.age.header.json", strings.NewReader("{}"), 2); err != nil {
		t.Fatal(err)
	}
	if err := d.Put(ctx, "beta/b.ffm.tar.age", strings.NewReader("b"), 1); err != nil {
		t.Fatal(err)
	}
	objs, err := d.List(ctx, "alpha/")
	if err != nil || len(objs) != 2 {
		t.Fatalf("List(alpha/) = %+v, %v", objs, err)
	}
	for _, o := range objs {
		if o.Key == "alpha/a.ffm.tar.age" && o.Size != int64(len(data)) {
			t.Errorf("size %d, want %d", o.Size, len(data))
		}
	}
	var got bytes.Buffer
	if err := d.Get(ctx, "alpha/a.ffm.tar.age", &got); err != nil || !bytes.Equal(got.Bytes(), data) {
		t.Fatalf("Get: %v, %d bytes", err, got.Len())
	}
	if err := d.Delete(ctx, "alpha/a.ffm.tar.age"); err != nil {
		t.Fatal(err)
	}
	if objs, _ := d.List(ctx, "alpha/"); len(objs) != 1 {
		t.Errorf("after delete: %+v", objs)
	}
	if err := d.Put(ctx, "../escape", strings.NewReader("x"), 1); err == nil {
		t.Error("a key leaving the prefix was accepted")
	}
}

func TestLocal(t *testing.T) {
	root := t.TempDir()
	d, err := Open(context.Background(), Target{Name: "nas", Type: TypeLocal, Path: root, Prefix: "ffm"})
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, d)
	if _, err := os.Stat(filepath.Join(root, "ffm", "beta", "b.ffm.tar.age")); err != nil {
		t.Error("prefix not applied on disk")
	}
	if st, _ := os.Stat(filepath.Join(root, "ffm", "beta", "b.ffm.tar.age")); st.Mode().Perm() != 0o600 {
		t.Errorf("object mode %v", st.Mode().Perm())
	}
}

func TestValidate(t *testing.T) {
	for _, bad := range []Target{
		{Name: "Bad Name", Type: TypeLocal, Path: "/x"},
		{Name: "a", Type: "ftp"},
		{Name: "a", Type: TypeLocal, Path: "relative"},
		{Name: "a", Type: TypeS3, Endpoint: "e", Bucket: "b", AccessKeyID: "k"}, // no secret
		{Name: "a", Type: TypeSFTP, Host: "h", User: "u", KeyFile: "/k"},        // no pinned host key
	} {
		if bad.Validate() == nil {
			t.Errorf("%+v validated", bad)
		}
	}
}

// sftpServer serves SFTP on 127.0.0.1 for one authorized client key.
func sftpServer(t *testing.T, client ssh.PublicKey) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	_, hpriv, _ := ed25519.GenerateKey(rand.Reader)
	hsigner, _ := ssh.NewSignerFromKey(hpriv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), client.Marshal()) {
			return nil, nil
		}
		return nil, io.EOF
	}}
	cfg.AddHostKey(hsigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nch := range chans {
					ch, creqs, _ := nch.Accept()
					go func() {
						for r := range creqs {
							r.Reply(r.Type == "subsystem", nil)
							if r.Type == "subsystem" {
								srv, _ := sftp.NewServer(ch)
								srv.Serve()
								ch.Close()
							}
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().String(), hsigner.PublicKey()
}

func TestSFTPPinsTheHostKey(t *testing.T) {
	_, cpriv, _ := ed25519.GenerateKey(rand.Reader)
	csigner, _ := ssh.NewSignerFromKey(cpriv)
	block, _ := ssh.MarshalPrivateKey(cpriv, "")
	keyFile := filepath.Join(t.TempDir(), "id")
	os.WriteFile(keyFile, pem.EncodeToMemory(block), 0o600)

	addr, hostKey := sftpServer(t, csigner.PublicKey())
	host, port, _ := net.SplitHostPort(addr)
	p := 0
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	line, fp, err := FetchHostKey(host, p)
	if err != nil || line != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))) || !strings.HasPrefix(fp, "SHA256:") {
		t.Fatalf("FetchHostKey = %q %q %v", line, fp, err)
	}
	tg := Target{Name: "box", Type: TypeSFTP, Host: host, Port: p, User: "u", KeyFile: keyFile, HostKey: line, Path: t.TempDir()}
	d, err := Open(context.Background(), tg)
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, d)
	d.Close()

	// Another server key must be refused.
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	os, _ := ssh.NewSignerFromKey(other)
	tg.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(os.PublicKey())))
	if _, err := Open(context.Background(), tg); err == nil {
		t.Error("connected to a server whose host key does not match the pinned one")
	}
}
