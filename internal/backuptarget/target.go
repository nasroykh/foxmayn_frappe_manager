// Package backuptarget stores backup archives off the host: an S3-compatible
// bucket, an SFTP server, a mounted directory, or anything rclone reaches.
//
// Every target is addressed by keys relative to its prefix
// ("<bench>/<archive>"). ffm only ever uploads age-encrypted archives and their
// cleartext header sidecars, so a target needs no trust beyond keeping them.
package backuptarget

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"
)

// Types of target.
const (
	TypeS3     = "s3"
	TypeSFTP   = "sftp"
	TypeLocal  = "local"
	TypeRclone = "rclone"
)

// Target is one configured destination, as stored in backup-targets.json.
type Target struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Prefix is prepended to every key: a folder in the bucket, below the
	// SFTP path, the local path or the rclone remote.
	Prefix string `json:"prefix,omitempty"`

	// S3-compatible (AWS, R2, B2, Wasabi, MinIO…).
	Endpoint    string `json:"endpoint,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Region      string `json:"region,omitempty"`
	Insecure    bool   `json:"insecure,omitempty"` // plain HTTP, for a local MinIO
	AccessKeyID string `json:"access_key_id,omitempty"`
	// SecretAccessKey is kept in backup-targets.json (0600) unless
	// SecretAccessKeyFile names a file to read it from instead.
	SecretAccessKey     string `json:"secret_access_key,omitempty"`
	SecretAccessKeyFile string `json:"secret_access_key_file,omitempty"`

	// SFTP. HostKey pins the server's public key (authorized_keys format);
	// there is no unpinned mode.
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user,omitempty"`
	KeyFile string `json:"key_file,omitempty"`
	HostKey string `json:"host_key,omitempty"`

	// Local directory (a NAS mount, a USB disk).
	Path string `json:"path,omitempty"`

	// rclone remote, e.g. "gdrive:backups"; credentials stay in rclone's config.
	Remote string `json:"remote,omitempty"`
}

// Object is one stored file.
type Object struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// Destination is an opened target.
type Destination interface {
	// Put stores r (size bytes) under key. A reader seeing the key afterwards
	// sees the whole object.
	Put(ctx context.Context, key string, r io.Reader, size int64) error
	Get(ctx context.Context, key string, w io.Writer) error
	// List returns the objects under prefix (recursive).
	List(ctx context.Context, prefix string) ([]Object, error)
	Delete(ctx context.Context, key string) error
	Close() error
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// Validate checks that a target is complete.
func (t Target) Validate() error {
	if !nameRe.MatchString(t.Name) {
		return fmt.Errorf("target name %q: lowercase letters, digits and '-', up to 32", t.Name)
	}
	need := func(field, v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("target %q (%s) needs %s", t.Name, t.Type, field)
		}
		return nil
	}
	var errs []error
	switch t.Type {
	case TypeS3:
		errs = append(errs, need("--endpoint", t.Endpoint), need("--bucket", t.Bucket), need("--access-key-id", t.AccessKeyID))
		if t.SecretAccessKey == "" && t.SecretAccessKeyFile == "" {
			errs = append(errs, fmt.Errorf("target %q (s3) needs a secret access key (--secret-access-key-stdin or --secret-access-key-file)", t.Name))
		}
	case TypeSFTP:
		errs = append(errs, need("--host", t.Host), need("--user", t.User), need("--key-file", t.KeyFile), need("a pinned host key (--host-key or --accept-host-key)", t.HostKey))
	case TypeLocal:
		errs = append(errs, need("--path", t.Path))
		if t.Path != "" && !strings.HasPrefix(t.Path, "/") {
			errs = append(errs, fmt.Errorf("target %q: --path must be absolute", t.Name))
		}
	case TypeRclone:
		errs = append(errs, need("--remote", t.Remote))
	default:
		return fmt.Errorf("target type %q: must be s3, sftp, local or rclone", t.Type)
	}
	return errors.Join(errs...)
}

// Describe is a one-line, secret-free summary.
func (t Target) Describe() string {
	switch t.Type {
	case TypeS3:
		scheme := "https"
		if t.Insecure {
			scheme = "http"
		}
		return fmt.Sprintf("s3 %s://%s/%s", scheme, t.Endpoint, path.Join(t.Bucket, t.Prefix))
	case TypeSFTP:
		return fmt.Sprintf("sftp %s@%s:%d%s", t.User, t.Host, t.port(), path.Join("/", t.Path, t.Prefix))
	case TypeLocal:
		return "local " + path.Join(t.Path, t.Prefix)
	case TypeRclone:
		return "rclone " + strings.TrimSuffix(t.Remote, "/") + "/" + t.Prefix
	}
	return t.Type
}

func (t Target) port() int {
	if t.Port == 0 {
		return 22
	}
	return t.Port
}

// Open connects to a target.
func Open(ctx context.Context, t Target) (Destination, error) {
	if err := t.Validate(); err != nil {
		return nil, err
	}
	switch t.Type {
	case TypeS3:
		return openS3(ctx, t)
	case TypeSFTP:
		return openSFTP(ctx, t)
	case TypeLocal:
		return openLocal(t)
	case TypeRclone:
		return openRclone(t)
	}
	return nil, fmt.Errorf("unknown target type %q", t.Type)
}

// cleanKey refuses keys that could leave the target's prefix.
func cleanKey(key string) (string, error) {
	c := path.Clean("/" + key)[1:]
	if c == "" || c != strings.TrimPrefix(key, "/") || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid key %q", key)
	}
	return c, nil
}

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return strings.TrimSuffix(prefix, "/") + "/" + key
}
