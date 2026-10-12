package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func secretData(mode, db string) ComposeData {
	d := ComposeData{
		Name: "s", Mode: mode, DBType: db, DBRootPassword: "s3cretRootPw!",
		WebPort: 8000, WebPortEnd: 8005, SocketIOPort: 9000, SocketIOPortEnd: 9005,
		Domain: "erp.example.com", SiteName: "erp.example.com", NoSSL: true,
		MariaDBBufferPool: "1G", GunicornWorkers: 2, WorkerLongCount: 1, WorkerShortCount: 1,
		RedisCacheMaxmem: "512mb", RedisQueueMaxmem: "512mb",
	}
	return d
}

// The root password lives in secrets/db_root_password, never in the compose
// file, for every mode and engine.
func TestComposeHasNoRootPassword(t *testing.T) {
	for _, mode := range []string{"dev", "prod"} {
		for _, db := range []string{"mariadb", "postgres"} {
			out, err := RenderCompose(secretData(mode, db))
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			if strings.Contains(s, "s3cretRootPw!") {
				t.Errorf("%s/%s: the password is in the compose file", mode, db)
			}
			if strings.Contains(s, "MYSQL_ROOT_PASSWORD:") || strings.Contains(s, "POSTGRES_PASSWORD:") {
				t.Errorf("%s/%s: an inline password variable is still rendered", mode, db)
			}
			want := "MARIADB_ROOT_PASSWORD_FILE: /run/secrets/db_root_password"
			if db == "postgres" {
				want = "POSTGRES_PASSWORD_FILE: /run/secrets/db_root_password"
			}
			for _, need := range []string{want, "- db_root_password", "file: ./secrets/db_root_password"} {
				if !strings.Contains(s, need) {
					t.Errorf("%s/%s: missing %q", mode, db, need)
				}
			}
		}
	}
	// The prod healthcheck reads the secret too.
	out, _ := RenderCompose(secretData("prod", "mariadb"))
	if !strings.Contains(string(out), `$(cat \"$${MARIADB_ROOT_PASSWORD_FILE}\")`) {
		t.Error("prod mariadb healthcheck does not read the secret file")
	}
}

func TestWriteComposeWritesSecretInPlace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	d := secretData("prod", "mariadb")
	if err := WriteCompose(dir, d); err != nil {
		t.Fatal(err)
	}
	path := DBRootSecretPath(dir)
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("secret mode = %o, want 600", st.Mode().Perm())
	}
	if ds, _ := os.Stat(filepath.Dir(path)); ds.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir mode = %o, want 700", ds.Mode().Perm())
	}
	if got, _ := os.ReadFile(path); string(got) != "s3cretRootPw!" {
		t.Errorf("secret = %q", got)
	}

	// A rewrite with a LONGER then shorter password keeps the inode (the file
	// is a bind mount of the running container) and leaves no tail behind.
	for _, pw := range []string{"a-much-longer-new-password-123", "short"} {
		d.DBRootPassword = pw
		if err := WriteCompose(dir, d); err != nil {
			t.Fatal(err)
		}
		st2, _ := os.Stat(path)
		if !os.SameFile(st, st2) {
			t.Fatal("the secret file was replaced, not rewritten in place")
		}
		if got, _ := os.ReadFile(path); string(got) != pw {
			t.Errorf("secret = %q, want %q", got, pw)
		}
		if st2.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o after rewrite", st2.Mode().Perm())
		}
	}
	// An empty password (render without credentials) leaves the secret alone.
	d.DBRootPassword = ""
	if err := WriteCompose(dir, d); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "short" {
		t.Errorf("an empty password overwrote the secret: %q", got)
	}
}
