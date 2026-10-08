package backuptarget

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
)

// --- local directory ---

type localDest struct{ root string }

func openLocal(t Target) (Destination, error) {
	root := filepath.Join(t.Path, filepath.FromSlash(t.Prefix))
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &localDest{root: root}, nil
}

func (d *localDest) file(key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(d.root, filepath.FromSlash(k)), nil
}

func (d *localDest) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := p + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, p)
}

func (d *localDest) Get(_ context.Context, key string, w io.Writer) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

func (d *localDest) List(_ context.Context, prefix string) ([]Object, error) {
	var out []Object
	err := filepath.WalkDir(d.root, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if e.IsDir() || strings.HasSuffix(p, ".partial") {
			return nil
		}
		rel, _ := filepath.Rel(d.root, p)
		key := filepath.ToSlash(rel)
		if !strings.HasPrefix(key, prefix) {
			return nil
		}
		fi, err := e.Info()
		if err != nil {
			return err
		}
		out = append(out, Object{Key: key, Size: fi.Size(), ModTime: fi.ModTime()})
		return nil
	})
	return out, err
}

func (d *localDest) Delete(_ context.Context, key string) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *localDest) Close() error { return nil }

// --- S3-compatible ---

type s3Dest struct {
	c      *minio.Client
	bucket string
	prefix string
}

func openS3(ctx context.Context, t Target) (Destination, error) {
	secret := t.SecretAccessKey
	if t.SecretAccessKeyFile != "" {
		raw, err := os.ReadFile(t.SecretAccessKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read the secret access key: %w", err)
		}
		secret = strings.TrimSpace(string(raw))
	}
	endpoint := strings.TrimPrefix(strings.TrimPrefix(t.Endpoint, "https://"), "http://")
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(t.AccessKeyID, secret, ""),
		Secure: !t.Insecure,
		Region: t.Region,
	})
	if err != nil {
		return nil, err
	}
	ok, err := c.BucketExists(ctx, t.Bucket)
	if err != nil {
		return nil, fmt.Errorf("reach bucket %q: %w", t.Bucket, err)
	}
	if !ok {
		return nil, fmt.Errorf("bucket %q does not exist", t.Bucket)
	}
	return &s3Dest{c: c, bucket: t.Bucket, prefix: strings.Trim(t.Prefix, "/")}, nil
}

func (d *s3Dest) Put(ctx context.Context, key string, r io.Reader, size int64) error {
	k, err := cleanKey(key)
	if err != nil {
		return err
	}
	_, err = d.c.PutObject(ctx, d.bucket, joinKey(d.prefix, k), r, size,
		minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (d *s3Dest) Get(ctx context.Context, key string, w io.Writer) error {
	k, err := cleanKey(key)
	if err != nil {
		return err
	}
	obj, err := d.c.GetObject(ctx, d.bucket, joinKey(d.prefix, k), minio.GetObjectOptions{})
	if err != nil {
		return err
	}
	defer obj.Close()
	_, err = io.Copy(w, obj)
	return err
}

func (d *s3Dest) List(ctx context.Context, prefix string) ([]Object, error) {
	var out []Object
	full := joinKey(d.prefix, prefix)
	for o := range d.c.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{Prefix: full, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		key := o.Key
		if d.prefix != "" {
			key = strings.TrimPrefix(key, d.prefix+"/")
		}
		out = append(out, Object{Key: key, Size: o.Size, ModTime: o.LastModified})
	}
	return out, nil
}

func (d *s3Dest) Delete(ctx context.Context, key string) error {
	k, err := cleanKey(key)
	if err != nil {
		return err
	}
	return d.c.RemoveObject(ctx, d.bucket, joinKey(d.prefix, k), minio.RemoveObjectOptions{})
}

func (d *s3Dest) Close() error { return nil }

// --- SFTP ---

type sftpDest struct {
	conn *ssh.Client
	c    *sftp.Client
	root string
}

// ParseHostKey reads a pinned host key in authorized_keys format.
func ParseHostKey(line string) (ssh.PublicKey, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
	return k, err
}

// FetchHostKey connects to host:port and returns its host key in
// authorized_keys format and its SHA256 fingerprint, without authenticating:
// it is what `--accept-host-key` shows the user before pinning it.
func FetchHostKey(host string, port int) (line, fingerprint string, err error) {
	var got ssh.PublicKey
	cfg := &ssh.ClientConfig{
		User: "ffm-host-key-probe",
		Auth: []ssh.AuthMethod{},
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			got = key
			return errors.New("probe only")
		},
		Timeout: 15 * time.Second,
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)), cfg)
	if conn != nil {
		conn.Close()
	}
	if got == nil {
		return "", "", fmt.Errorf("no host key from %s:%d: %w", host, port, err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(got))), ssh.FingerprintSHA256(got), nil
}

func openSFTP(_ context.Context, t Target) (Destination, error) {
	hostKey, err := ParseHostKey(t.HostKey)
	if err != nil {
		return nil, fmt.Errorf("pinned host key: %w", err)
	}
	raw, err := os.ReadFile(t.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("read the SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("SSH key %s: %w (passphrase-protected keys are not supported; use a dedicated key)", t.KeyFile, err)
	}
	conn, err := ssh.Dial("tcp", net.JoinHostPort(t.Host, strconv.Itoa(t.port())), &ssh.ClientConfig{
		User:            t.User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", t.Host, err)
	}
	c, err := sftp.NewClient(conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	root := path.Join(t.Path, t.Prefix)
	if root == "" {
		root = "."
	}
	return &sftpDest{conn: conn, c: c, root: root}, nil
}

func (d *sftpDest) file(key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return path.Join(d.root, k), nil
}

func (d *sftpDest) Put(_ context.Context, key string, r io.Reader, _ int64) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	if err := d.c.MkdirAll(path.Dir(p)); err != nil {
		return err
	}
	tmp := p + ".partial"
	f, err := d.c.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC)
	if err != nil {
		return err
	}
	_ = f.Chmod(0o600)
	if _, err := f.ReadFrom(r); err != nil {
		f.Close()
		d.c.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		d.c.Remove(tmp)
		return err
	}
	if err := d.c.PosixRename(tmp, p); err != nil {
		// Servers without the posix-rename extension: remove, then rename.
		_ = d.c.Remove(p)
		if err := d.c.Rename(tmp, p); err != nil {
			d.c.Remove(tmp)
			return err
		}
	}
	return nil
}

func (d *sftpDest) Get(_ context.Context, key string, w io.Writer) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	f, err := d.c.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteTo(w)
	return err
}

func (d *sftpDest) List(_ context.Context, prefix string) ([]Object, error) {
	var out []Object
	walker := d.c.Walk(d.root)
	for walker.Step() {
		if err := walker.Err(); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		st := walker.Stat()
		if st.IsDir() || strings.HasSuffix(walker.Path(), ".partial") {
			continue
		}
		key := strings.TrimPrefix(strings.TrimPrefix(walker.Path(), d.root), "/")
		if strings.HasPrefix(key, prefix) {
			out = append(out, Object{Key: key, Size: st.Size(), ModTime: st.ModTime()})
		}
	}
	return out, nil
}

func (d *sftpDest) Delete(_ context.Context, key string) error {
	p, err := d.file(key)
	if err != nil {
		return err
	}
	if err := d.c.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (d *sftpDest) Close() error {
	d.c.Close()
	return d.conn.Close()
}

// --- rclone ---

type rcloneDest struct{ base string }

func openRclone(t Target) (Destination, error) {
	if _, err := execx.Command("rclone", "version").Output(); err != nil {
		return nil, fmt.Errorf("rclone is not installed or not on PATH: %w", err)
	}
	base := strings.TrimSuffix(t.Remote, "/")
	if t.Prefix != "" {
		base += "/" + strings.Trim(t.Prefix, "/")
	}
	return &rcloneDest{base: base}, nil
}

func (d *rcloneDest) remote(key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	return d.base + "/" + k, nil
}

func rcloneRun(stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := execx.Command("rclone", args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("rclone %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (d *rcloneDest) Put(_ context.Context, key string, r io.Reader, size int64) error {
	p, err := d.remote(key)
	if err != nil {
		return err
	}
	return rcloneRun(r, io.Discard, "rcat", "--size", strconv.FormatInt(size, 10), p)
}

func (d *rcloneDest) Get(_ context.Context, key string, w io.Writer) error {
	p, err := d.remote(key)
	if err != nil {
		return err
	}
	return rcloneRun(nil, w, "cat", p)
}

func (d *rcloneDest) List(_ context.Context, prefix string) ([]Object, error) {
	var buf bytes.Buffer
	if err := rcloneRun(nil, &buf, "lsjson", "--recursive", "--files-only", d.base); err != nil {
		if strings.Contains(err.Error(), "directory not found") {
			return nil, nil
		}
		return nil, err
	}
	var items []struct {
		Path    string
		Size    int64
		ModTime time.Time
	}
	if err := json.Unmarshal(buf.Bytes(), &items); err != nil {
		return nil, err
	}
	var out []Object
	for _, it := range items {
		if strings.HasPrefix(it.Path, prefix) {
			out = append(out, Object{Key: it.Path, Size: it.Size, ModTime: it.ModTime})
		}
	}
	return out, nil
}

func (d *rcloneDest) Delete(_ context.Context, key string) error {
	p, err := d.remote(key)
	if err != nil {
		return err
	}
	return rcloneRun(nil, io.Discard, "deletefile", p)
}

func (d *rcloneDest) Close() error { return nil }
