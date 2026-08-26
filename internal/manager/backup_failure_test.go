package manager

import "testing"

// realFailure is the shape `bench backup --verbose` produces when the frappe
// container cannot reach the database: Frappe's blanket message first, then a
// traceback whose frames dump their locals, including the site's database
// password, and whose final line is the only part that names the cause.
const realFailure = `Backup failed for Site foxmayn.localhost. Database or site_config.json may be corrupted
Traceback (most recent call last):
  File "apps/frappe/frappe/database/database.py", line 147, in connect
    self.get_connection()
      self = frappe.database.mariadb.mysqlclient.MariaDBDatabase(cur_db_name='_c97bce7691465e80', host='mariadb', password='NfcgGq9TYaVsJ04J', port=3306)
  File "env/lib/python3.14/site-packages/MySQLdb/__init__.py", line 121, in Connect
    return Connection(*args, **kwargs)
      kwargs = {'user': '_c97bce7691465e80', 'host': 'mariadb', "password": "NfcgGq9TYaVsJ04J"}
MySQLdb.OperationalError: (2002, "Can't connect to server on 'mariadb' (115)")`

func TestBenchBackupFailureKeepsTheCause(t *testing.T) {
	got := benchBackupFailure(realFailure, false)
	want := "Backup failed for Site foxmayn.localhost. Database or site_config.json may be corrupted\n" +
		"MySQLdb.OperationalError: (2002, \"Can't connect to server on 'mariadb' (115)\")\n" +
		"(run ffm --verbose backup for Frappe's full traceback)"
	if got != want {
		t.Fatalf("benchBackupFailure:\n got: %q\nwant: %q", got, want)
	}
}

func TestBenchBackupFailureDropsTheFrameDump(t *testing.T) {
	got := benchBackupFailure(realFailure, false)
	for _, unwanted := range []string{"self.get_connection()", "MySQLdb/__init__.py", "kwargs ="} {
		if contains(got, unwanted) {
			t.Errorf("non-verbose output still carries traceback frame %q:\n%s", unwanted, got)
		}
	}
}

// The password appears in a keyword argument and in a dict entry, quoted two
// different ways. Both are in the frame dump that only --verbose prints, which
// is exactly the mode where it would reach a terminal.
func TestBenchBackupFailureRedactsPasswords(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		got := benchBackupFailure(realFailure, verbose)
		if contains(got, "NfcgGq9TYaVsJ04J") {
			t.Errorf("verbose=%v leaked the database password:\n%s", verbose, got)
		}
	}
	// The same rule has to cover the other credentials a frame dump carries.
	const others = `Traceback (most recent call last):
      conf = {'db_password': 'ffm123456', "encryption_key": "AbC123", 'api_token': 'zzz'}
ValueError: boom`
	redacted := benchBackupFailure(others, true)
	for _, secret := range []string{"ffm123456", "AbC123", "zzz"} {
		if contains(redacted, secret) {
			t.Errorf("leaked %q:\n%s", secret, redacted)
		}
	}

	got := benchBackupFailure(realFailure, true)
	if !contains(got, "password='[redacted]'") {
		t.Errorf("verbose output did not mark the redaction:\n%s", got)
	}
	if !contains(got, "MySQLdb/__init__.py") {
		t.Errorf("verbose output dropped the traceback it exists to show:\n%s", got)
	}
}

// A failure with no traceback at all — Frappe exiting before it raises, or bash
// failing on the mkdir — must still be reported verbatim rather than swallowed.
func TestBenchBackupFailureWithoutATraceback(t *testing.T) {
	const out = "mkdir: cannot create directory '/tmp/ffm-backup-1': No space left on device"
	if got := benchBackupFailure(out, false); got != out {
		t.Fatalf("benchBackupFailure dropped a non-traceback failure:\n got: %q\nwant: %q", got, out)
	}
}

func TestLastExceptionLineIgnoresSourceLines(t *testing.T) {
	// "raise SiteNotSpecifiedError" is a source line, not the exception line;
	// picking it would report the wrong cause.
	const tb = `
  File "apps/frappe/frappe/commands/site.py", line 928, in backup
    raise SiteNotSpecifiedError
frappe.exceptions.SiteNotSpecifiedError`
	if got := lastExceptionLine(tb); got != "frappe.exceptions.SiteNotSpecifiedError" {
		t.Fatalf("lastExceptionLine = %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
