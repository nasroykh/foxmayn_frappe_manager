package manager

import "testing"

func prodManifest(domain string, aliases ...string) Manifest {
	m := devManifest()
	m.Header.Mode = "prod"
	m.Bench.Mode = "prod"
	m.Bench.Domain = domain
	m.Bench.DomainAliases = aliases
	m.Secrets.AdminPassword = "Str0ng-Pass_word.1"
	return m
}

func TestRestoreDomainOverride(t *testing.T) {
	cases := []struct {
		name    string
		m       Manifest
		in      RestoreInput
		problem string // "" means no domain problem expected
	}{
		{"archived domain invalid, no override", prodManifest("bad domain!.example.com"), RestoreInput{}, "archive's domain is"},
		{"archived domain invalid, --domain overrides", prodManifest("bad domain!.example.com"), RestoreInput{Domain: "erp.example.com"}, ""},
		{"--domain itself invalid", prodManifest("erp.example.com"), RestoreInput{Domain: "erp.example.com;id"}, "--domain"},
		{"bad alias, same name", prodManifest("erp.example.com", "bad alias!"), RestoreInput{}, "domain alias"},
		{"bad alias, restored under a new name", prodManifest("erp.example.com", "bad alias!"), RestoreInput{TargetName: "copy"}, ""},
	}
	for _, c := range cases {
		problems := checkManifestValues(c.m, c.in)
		for _, sub := range []string{"archive's domain is", "--domain", "domain alias"} {
			got := hasProblem(problems, sub)
			if want := sub == c.problem; got != want {
				t.Errorf("%s: problem %q present = %v, want %v (%v)", c.name, sub, got, want, problems)
			}
		}
	}
}

func TestRestoredSiteNameUsesOverride(t *testing.T) {
	m := prodManifest("old.example.com")
	if got := restoredSiteName(m, "staging", "ERP.Example.com"); got != "erp.example.com" {
		t.Errorf("with --domain: %q, want erp.example.com", got)
	}
	if got := restoredSiteName(m, "staging", ""); got != "old.example.com" {
		t.Errorf("without --domain: %q, want old.example.com", got)
	}
	if got := restoredSiteName(devManifest(), "copy", "ignored.example.com"); got != "copy.localhost" {
		t.Errorf("dev: %q, want copy.localhost", got)
	}
}
