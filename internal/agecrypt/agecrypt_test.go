package agecrypt

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRoundTripAndTamper(t *testing.T) {
	dir := t.TempDir()
	idStr, recStr, err := GenerateIdentity()
	if err != nil {
		t.Fatal(err)
	}
	recs, err := ParseRecipients(strings.NewReader("# mine\n" + recStr + "\n\n"))
	if err != nil || len(recs) != 1 {
		t.Fatalf("recipients: %v %v", recs, err)
	}
	idFile := filepath.Join(dir, "id.txt")
	os.WriteFile(idFile, []byte(idStr+"\n"), 0o600)
	ids, err := LoadIdentities(idFile)
	if err != nil {
		t.Fatal(err)
	}

	plain := bytes.Repeat([]byte("archive bytes "), 100000)
	src := filepath.Join(dir, "a.ffm.tar")
	os.WriteFile(src, plain, 0o600)
	enc := src + Ext
	if err := EncryptFile(src, enc, recs); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(enc); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	raw, _ := os.ReadFile(enc)
	if bytes.Contains(raw, []byte("archive bytes")) {
		t.Fatal("ciphertext contains the plaintext")
	}
	out := filepath.Join(dir, "out.tar")
	if err := DecryptFile(enc, out, ids); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, plain) {
		t.Fatal("round trip changed the bytes")
	}

	// A flipped byte must fail, not decrypt to something else.
	raw[len(raw)-100] ^= 0xff
	bad := filepath.Join(dir, "bad.age")
	os.WriteFile(bad, raw, 0o600)
	if err := DecryptFile(bad, filepath.Join(dir, "bad.tar"), ids); err == nil {
		t.Error("tampered archive decrypted")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.tar")); err == nil {
		t.Error("a failed decryption left its output behind")
	}

	// Another identity cannot decrypt it.
	other, _, _ := GenerateIdentity()
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte(other), 0o600)
	oids, _ := LoadIdentities(filepath.Join(dir, "other.txt"))
	if err := DecryptFile(enc, filepath.Join(dir, "x.tar"), oids); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Errorf("wrong identity: %v", err)
	}
	if ValidRecipient("age1notarecipient") == nil || ValidRecipient(recStr) != nil {
		t.Error("ValidRecipient")
	}
}
