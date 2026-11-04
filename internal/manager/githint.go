package manager

import "strings"

// What git prints when it cannot authenticate, by transport. The HTTPS ones
// cover a missing token ("could not read Username": git wants to prompt and
// has no terminal) and a refused one; GitHub answers "Repository not found"
// for a private repository the caller cannot see.
var (
	gitHTTPSAuthMarkers = []string{
		"could not read Username",
		"could not read Password",
		"terminal prompts disabled",
		"Authentication failed",
		"Repository not found",
		"The requested URL returned error: 401",
		"The requested URL returned error: 403",
	}
	gitSSHAuthMarkers = []string{
		"Permission denied (publickey",
		"Host key verification failed",
	}
)

// gitAuthHint turns a failed clone or fetch into the next step to take, or
// returns "" when the output does not look like an authentication failure.
//
// Without it the user saw bench's traceback and "bench init: exit status 1",
// with nothing saying that a token was all it took.
func gitAuthHint(out string, haveToken bool) string {
	switch {
	case containsAny(out, gitSSHAuthMarkers):
		return "hint: git could not authenticate over SSH. Pass --ssh-agent to forward this " +
			"host's SSH agent (dev benches), or --github-token to clone github.com over HTTPS instead"
	case !containsAny(out, gitHTTPSAuthMarkers):
		return ""
	case haveToken:
		return "hint: git could not authenticate with the GitHub token given. Check that it is " +
			"not expired and can read this repository (a fine-grained token needs Contents: read on it)"
	default:
		return "hint: git could not authenticate — the repository is private or does not exist. " +
			"Pass --github-token, or set FFM_GITHUB_TOKEN (or GITHUB_TOKEN). With the GitHub CLI " +
			"logged in: FFM_GITHUB_TOKEN=\"$(gh auth token)\" ffm ..."
	}
}

// gitAuthSuffix is gitAuthHint on a line of its own, for the end of an error
// message, or "" when there is no hint.
func gitAuthSuffix(out string, haveToken bool) string {
	if hint := gitAuthHint(out, haveToken); hint != "" {
		return "\n" + hint
	}
	return ""
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
