package bench

import (
	"os/exec"
	"testing"
)

// TestShellQuoteSurvivesBash runs each value through a real shell: what the
// command receives must be exactly what ffm recorded.
func TestShellQuoteSurvivesBash(t *testing.T) {
	sh, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	for _, v := range []string{
		"plain", "with space", "dollar$HOME", "$(touch /tmp/ffm-pwned)", "`id`",
		"it's", `back\slash`, `dq"uote`, "semi;colon&amp|pipe>redir", "",
	} {
		out, err := exec.Command(sh, "-c", "printf %s "+ShellQuote(v)).Output()
		if err != nil {
			t.Fatalf("%q: %v", v, err)
		}
		if string(out) != v {
			t.Errorf("ShellQuote(%q) reached the command as %q", v, out)
		}
	}
}

func TestValidatePasswords(t *testing.T) {
	for _, ok := range []string{"ffm123456", "Str0ng!pw#2026", "a&b|c;d(e)<f>", "it's"} {
		if err := ValidateDBPassword(ok); err != nil {
			t.Errorf("ValidateDBPassword(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "has space", "dollar$x", `quo"te`, `back\slash`, "tab\there"} {
		if ValidateDBPassword(bad) == nil {
			t.Errorf("ValidateDBPassword(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"admin", "with space", "$(anything)", `"quoted"`} {
		if err := ValidateAdminPassword(ok); err != nil {
			t.Errorf("ValidateAdminPassword(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "two\nlines", "nul\x00"} {
		if ValidateAdminPassword(bad) == nil {
			t.Errorf("ValidateAdminPassword(%q) accepted", bad)
		}
	}
}
