package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/agecrypt"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
)

// ErrNoRecipients means no backup key has been set up yet.
var ErrNoRecipients = errors.New("no backup encryption key: run 'ffm backup key init' first")

// BackupRecipients returns the recipients backups are encrypted to.
func BackupRecipients() ([]age.Recipient, error) {
	f, err := os.Open(config.BackupRecipientsFile())
	if os.IsNotExist(err) {
		return nil, ErrNoRecipients
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	recs, err := agecrypt.ParseRecipients(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", config.BackupRecipientsFile(), err)
	}
	if len(recs) == 0 {
		return nil, ErrNoRecipients
	}
	return recs, nil
}

// RecipientLines returns the recipients file's recipients as written.
func RecipientLines() ([]string, error) {
	raw, err := os.ReadFile(config.BackupRecipientsFile())
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out, nil
}

// InitBackupKey creates an age identity, adds its recipient to the recipients
// file and returns the identity. It refuses when recipients exist already,
// unless add is set: a second key must be a deliberate choice, since every
// later backup is then readable with either.
//
// When out is set the identity is written there (0600, never overwritten)
// instead of being returned for printing.
func InitBackupKey(out string, add bool) (identity, recipient string, err error) {
	existing, err := RecipientLines()
	if err != nil {
		return "", "", err
	}
	if len(existing) > 0 && !add {
		return "", "", fmt.Errorf("backups are already encrypted to %d key(s) (%s); pass --add to add another",
			len(existing), config.BackupRecipientsFile())
	}
	identity, recipient, err = agecrypt.GenerateIdentity()
	if err != nil {
		return "", "", err
	}
	if out != "" {
		f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return "", "", fmt.Errorf("write identity: %w", err)
		}
		_, werr := fmt.Fprintf(f, "# ffm backup identity, public key %s\n%s\n", recipient, identity)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return "", "", werr
		}
	}
	if err := AddBackupRecipient(recipient); err != nil {
		return "", "", err
	}
	return identity, recipient, nil
}

// AddBackupRecipient appends a recipient (age1…), for example an offline key.
func AddBackupRecipient(recipient string) error {
	recipient = strings.TrimSpace(recipient)
	if err := agecrypt.ValidRecipient(recipient); err != nil {
		return fmt.Errorf("not an age recipient: %w", err)
	}
	existing, err := RecipientLines()
	if err != nil {
		return err
	}
	for _, e := range existing {
		if e == recipient {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(config.BackupRecipientsFile()), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(config.BackupRecipientsFile(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintln(f, recipient); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// sidecarPath is the cleartext header written next to an encrypted archive,
// so listing and pruning work without the identity.
func sidecarPath(encrypted string) string { return encrypted + ".header.json" }

// encryptArchive replaces a finished archive with its encrypted form and a
// header sidecar, and returns the new path.
func encryptArchive(path string, headerJSON []byte) (string, error) {
	recs, err := BackupRecipients()
	if err != nil {
		return "", err
	}
	enc := path + agecrypt.Ext
	if err := agecrypt.EncryptFile(path, enc, recs); err != nil {
		return "", fmt.Errorf("encrypt the archive: %w", err)
	}
	if err := os.WriteFile(sidecarPath(enc), headerJSON, 0o600); err != nil {
		os.Remove(enc)
		return "", fmt.Errorf("write the header sidecar: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove the plaintext archive: %w", err)
	}
	return enc, nil
}

// identityFile is where a restore reads the age identity: the flag, else
// $FFM_AGE_IDENTITY_FILE.
func identityFile(flag string) string {
	if flag != "" {
		return flag
	}
	return os.Getenv("FFM_AGE_IDENTITY_FILE")
}

// decryptForRestore decrypts an encrypted archive into a private temporary
// file next to the backups and returns its path and a cleanup function.
func decryptForRestore(path, identityFlag string) (string, func(), error) {
	idPath := identityFile(identityFlag)
	if idPath == "" {
		return "", nil, fmt.Errorf("%s is encrypted: pass --identity <file> (or set FFM_AGE_IDENTITY_FILE)", filepath.Base(path))
	}
	ids, err := agecrypt.LoadIdentities(idPath)
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(config.BackupsDir(), 0o700); err != nil {
		return "", nil, err
	}
	tmp, err := os.MkdirTemp(config.BackupsDir(), ".decrypt-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(tmp) }
	out := filepath.Join(tmp, strings.TrimSuffix(filepath.Base(path), agecrypt.Ext))
	if err := agecrypt.DecryptFile(path, out, ids); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("decrypt %s: %w", filepath.Base(path), err)
	}
	return out, cleanup, nil
}
