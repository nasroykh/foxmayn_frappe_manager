package bench

import (
	"strings"
	"testing"
)

func TestWorktreeSlug(t *testing.T) {
	for in, want := range map[string]string{
		"feat/X_y":             "feat-x-y",
		"Feature//Login--Page": "feature-login-page",
		"-lead/trail-":         "lead-trail",
		"123-fix":              "123-fix",
		"///":                  "",
	} {
		if got := WorktreeSlug(in); got != want {
			t.Errorf("WorktreeSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWorktreeBenchName(t *testing.T) {
	got, err := WorktreeBenchName("myapp", "feat/X_y")
	if err != nil || got != "myapp-feat-x-y" {
		t.Fatalf("got %q, %v", got, err)
	}
	// A leading digit in the branch is fine: the bench name starts with the source's letter.
	if got, err = WorktreeBenchName("myapp", "123-fix"); err != nil || got != "myapp-123-fix" {
		t.Errorf("leading digit: %q, %v", got, err)
	}

	long := "feature/" + strings.Repeat("a", 80)
	other := "feature/" + strings.Repeat("a", 79) + "b"
	n1, err1 := WorktreeBenchName("myapp", long)
	n2, err2 := WorktreeBenchName("myapp", other)
	if err1 != nil || err2 != nil {
		t.Fatalf("long names: %v %v", err1, err2)
	}
	if len(n1) > 63 || len(n2) > 63 || n1 == n2 {
		t.Errorf("long names must fit in 63 and stay distinct: %q (%d) %q (%d)", n1, len(n1), n2, len(n2))
	}
	for _, n := range []string{n1, n2} {
		if err := ValidateNewName(n); err != nil {
			t.Errorf("%q: %v", n, err)
		}
	}
	// The same branch always gives the same name.
	if again, _ := WorktreeBenchName("myapp", long); again != n1 {
		t.Errorf("not deterministic: %q vs %q", again, n1)
	}
	// A source name of 63 characters leaves no room for the slug.
	src := strings.Repeat("s", 63)
	if n, err := WorktreeBenchName(src, "feat"); err != nil || len(n) > 63 || ValidateNewName(n) != nil {
		t.Errorf("63-char source: %q %v", n, err)
	}
	if _, err := WorktreeBenchName("myapp", "///"); err == nil {
		t.Error("a branch with no letters or digits was accepted")
	}
}

func TestWorktreeAliasLabelLength(t *testing.T) {
	got, err := WorktreeAlias("myapp", "feat/X_y")
	if err != nil || got != "feat-x-y.myapp.localhost" {
		t.Fatalf("got %q, %v", got, err)
	}
	long := "feature/" + strings.Repeat("a", 100)
	got, err = WorktreeAlias("myapp", long)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range strings.Split(got, ".") {
		if len(label) > 63 {
			t.Errorf("label %q is %d characters", label, len(label))
		}
	}
	if !strings.HasSuffix(got, ".myapp.localhost") {
		t.Errorf("alias = %q", got)
	}
}
