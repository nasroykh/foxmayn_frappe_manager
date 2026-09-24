package bench

import (
	"fmt"
	"regexp"
	"strings"
)

// ShellQuote single-quotes s for a POSIX shell, so it reaches the command as
// one literal argument whatever it contains. Every value interpolated into a
// `bash -c` string that did not come from ffm itself must go through it: an
// unquoted `$` is expanded (silently setting a different password than the one
// ffm records), and a space or quote splits or breaks the command.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dbPasswordBad matches what a database root password cannot contain. The
// password is rendered into docker-compose.yml inside a double-quoted YAML
// string (where `"` and `\` break the document) and is subject to Compose
// variable interpolation (where `$` is consumed), besides being passed on
// command lines. Whitespace and control characters are refused outright.
var dbPasswordBad = regexp.MustCompile(`[$"\\\s\x00-\x1f\x7f]`)

// ValidateDBPassword checks a database root password before it is used.
func ValidateDBPassword(pw string) error {
	if pw == "" {
		return fmt.Errorf("the database password cannot be empty")
	}
	if dbPasswordBad.MatchString(pw) {
		return fmt.Errorf("the database password cannot contain $, \", \\, spaces or control " +
			"characters — it is written into docker-compose.yml")
	}
	return nil
}

// ValidateAdminPassword checks an Administrator password. Anything printable
// is fine — it is always shell-quoted — but it must be a single line.
func ValidateAdminPassword(pw string) error {
	if pw == "" {
		return fmt.Errorf("the Administrator password cannot be empty")
	}
	if strings.ContainsAny(pw, "\x00\n\r") {
		return fmt.Errorf("the Administrator password cannot contain line breaks or NUL")
	}
	return nil
}
