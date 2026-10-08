// Package agecrypt encrypts backup archives with age (https://age-encryption.org)
// to a set of recipients, and decrypts them with an identity file.
//
// ffm keeps only recipients (public keys). The identity that decrypts is
// printed once by `ffm backup key init` and lives wherever the user keeps
// secrets; ffm reads it only when a restore asks for it.
package agecrypt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

// Ext is appended to an encrypted archive's name.
const Ext = ".age"

// IsEncrypted reports whether path names an encrypted archive.
func IsEncrypted(path string) bool { return strings.HasSuffix(path, Ext) }

// GenerateIdentity returns a new X25519 identity and its recipient.
func GenerateIdentity() (identity, recipient string, err error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return "", "", err
	}
	return id.String(), id.Recipient().String(), nil
}

// ParseRecipients reads age recipients, one per line; blank lines and
// lines starting with # are ignored.
func ParseRecipients(r io.Reader) ([]age.Recipient, error) {
	var out []age.Recipient
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rec, err := age.ParseX25519Recipient(line)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", n, err)
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

// ValidRecipient reports whether s is an age X25519 recipient (age1…).
func ValidRecipient(s string) error {
	_, err := age.ParseX25519Recipient(strings.TrimSpace(s))
	return err
}

// LoadIdentities reads an age identity file (AGE-SECRET-KEY-… lines).
func LoadIdentities(path string) ([]age.Identity, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ids, err := age.ParseIdentities(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return ids, nil
}

// EncryptFile writes src, encrypted to recipients, to dst. dst appears only
// once complete (written as dst.partial, then renamed) and is 0600.
func EncryptFile(src, dst string, recipients []age.Recipient) error {
	if len(recipients) == 0 {
		return errors.New("no recipients to encrypt to")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeAtomic(dst, func(out io.Writer) error {
		w, err := age.Encrypt(out, recipients...)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, in); err != nil {
			return err
		}
		return w.Close()
	})
}

// DecryptFile writes src, decrypted with identities, to dst (0600). age
// authenticates every chunk, so a tampered or truncated file fails here.
func DecryptFile(src, dst string, identities []age.Identity) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeAtomic(dst, func(out io.Writer) error {
		r, err := age.Decrypt(in, identities...)
		if err != nil {
			var noMatch *age.NoIdentityMatchError
			if errors.As(err, &noMatch) {
				return fmt.Errorf("the identity does not match any recipient of %s", filepath.Base(src))
			}
			return err
		}
		_, err = io.Copy(out, r)
		return err
	})
}

func writeAtomic(dst string, fill func(io.Writer) error) error {
	tmp := dst + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if err := fill(f); err != nil {
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
	return os.Rename(tmp, dst)
}
