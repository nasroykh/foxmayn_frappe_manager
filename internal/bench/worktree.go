package bench

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Names of a worktree bench: ffm worktree add <branch> --from <bench> makes a
// bench called <from>-<slug> that answers on <slug>.<from>.localhost as well
// as on its own <bench>.localhost.

var nonSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

// WorktreeSlug reduces a branch name to lowercase letters, digits and single
// hyphens: "Feat/Login_Page" is "feat-login-page". It is empty when the name
// holds none of them.
func WorktreeSlug(branch string) string {
	return strings.Trim(nonSlugRe.ReplaceAllString(strings.ToLower(branch), "-"), "-")
}

// BranchHash is six hex digits of the branch name's SHA-1: what keeps two long
// branches that share a prefix apart once they are truncated.
func BranchHash(branch string) string {
	sum := sha1.Sum([]byte(branch))
	return hex.EncodeToString(sum[:])[:6]
}

// fitName returns s when it is at most max characters, otherwise s cut to leave
// room for "-<hash>" with no hyphen left dangling before it.
func fitName(s, branch string, max int) string {
	if len(s) <= max {
		return s
	}
	keep := strings.TrimRight(s[:max-7], "-")
	return keep + "-" + BranchHash(branch)
}

// WorktreeBenchName returns the bench name for a worktree of from on branch:
// <from>-<slug>, shortened with a hash of the branch when it would pass 63
// characters. The result is a valid new bench name.
func WorktreeBenchName(from, branch string) (string, error) {
	slug := WorktreeSlug(branch)
	if slug == "" {
		return "", fmt.Errorf("branch %q has no letters or digits to name a bench after: pass --name", branch)
	}
	name := fitName(from+"-"+slug, branch, 63)
	if err := ValidateNewName(name); err != nil {
		return "", err
	}
	return name, nil
}

// WorktreeAlias returns the host a worktree bench answers on besides its own:
// <slug>.<from>.localhost, each label at most 63 characters.
func WorktreeAlias(from, branch string) (string, error) {
	slug := WorktreeSlug(branch)
	if slug == "" {
		return "", fmt.Errorf("branch %q has no letters or digits to name a host after", branch)
	}
	return NormalizeDomain(fitName(slug, branch, 63) + "." + from + ".localhost")
}
