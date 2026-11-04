package manager

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// pinTarget is one app a pinned restore will fetch at a recorded commit, with
// the source it is cloned from (the one the fetch goes to).
type pinTarget struct {
	Name, Source, Commit string
}

// pinTargets mirrors pinnable, adding each app's clone source.
func pinTargets(p restorePlan) []pinTarget {
	var out []pinTarget
	if p.FrappeArchive == nil && p.frappeInfo != nil && p.frappeInfo.Commit != "" && !p.frappeOverrideMovesBranch() {
		src := p.FrappeRepo
		if src == "" {
			src = "https://github.com/frappe/frappe"
		}
		out = append(out, pinTarget{Name: "frappe", Source: src, Commit: p.frappeInfo.Commit})
	}
	for _, a := range p.Apps {
		if a.Archived() || a.Info == nil || a.Info.Commit == "" || !appNameRe.MatchString(a.Name) || a.overrideMovesBranch() {
			continue
		}
		out = append(out, pinTarget{Name: a.Name, Source: bench.ParseAppSpec(a.Spec, "").Source, Commit: a.Info.Commit})
	}
	return out
}

var githubHTTPSRe = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9._-]+/[A-Za-z0-9._-]+?)(?:\.git)?/?$`)

// githubRepoOf returns "owner/repo" for a github.com source and whether the
// source is SSH, or "" for anything else (another host, a bare name).
func githubRepoOf(source string) (repo string, ssh bool) {
	if m := githubSSHRe.FindStringSubmatch(source); m != nil {
		repo, ssh = m[1], true
	} else if m := githubHTTPSRe.FindStringSubmatch(source); m != nil {
		repo = m[1]
	}
	// The value goes into an API path: no "." or ".." segment.
	for _, seg := range strings.Split(repo, "/") {
		if seg == "." || seg == ".." {
			return "", false
		}
	}
	return repo, ssh
}

// commitStatus asks GitHub whether a repository has a commit, returning the
// HTTP status: 200 it does, 422 the repository exists and the commit does not,
// 404 the repository is not visible with these credentials.
type commitStatus func(ctx context.Context, repo, sha, token string) (int, error)

var githubCommitStatus commitStatus = func(ctx context.Context, repo, sha, token string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.github.com/repos/"+repo+"/commits/"+sha, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

// checkPinnedCommits refuses, before anything is built, a pinned restore whose
// fetch cannot work: a commit that is gone from its repository (force-pushed,
// branch deleted), a private HTTPS repository with no token, a token GitHub
// refuses. Only github.com sources are checked; for anything else, and for any
// answer this cannot interpret, it warns and lets the fetch decide.
//
// An SSH source with no token is never refused on a 404: the API cannot see a
// private repository without a token, but the clone may well get in through a
// forwarded agent.
func checkPinnedCommits(plan restorePlan, token string, lookup commitStatus, warn func(string)) []Problem {
	var problems []Problem
	// One 401, one unreachable network or one rate limit says it all: every
	// further call would only repeat it (offline, each one waits out its
	// timeout).
	stop := false
	for _, t := range pinTargets(plan) {
		if stop {
			break
		}
		repo, ssh := githubRepoOf(t.Source)
		if repo == "" || !commitRe.MatchString(t.Commit) {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		status, err := lookup(ctx, repo, t.Commit, token)
		cancel()
		short := shortCommit(t.Commit)
		switch {
		case err != nil:
			stop = true
			warn(fmt.Sprintf("could not reach GitHub to check the pinned commits (%v) — trying anyway", err))
		case status == http.StatusOK:
		case status == http.StatusUnprocessableEntity:
			problems = append(problems, Problem{
				Message: fmt.Sprintf("%s: commit %s, which the archive recorded, is no longer on github.com/%s "+
					"(force-pushed, or its branch deleted)", t.Name, short, repo),
				Override: fmt.Sprintf("--latest-apps to restore at the branch heads, or --app %s=<git-url>@<another-branch> "+
					"to take that one app from a branch's head", t.Name),
			})
		case status == http.StatusUnauthorized:
			stop = true
			problems = append(problems, Problem{
				Message: "GitHub refused the token (401): it is expired, revoked or mistyped",
			})
		case status == http.StatusNotFound && token != "":
			problems = append(problems, Problem{
				Message: fmt.Sprintf("%s: the GitHub token cannot read github.com/%s (private repository the "+
					"token has no access to, or no such repository)", t.Name, repo),
			})
		case status == http.StatusNotFound && !ssh:
			problems = append(problems, Problem{
				Message: fmt.Sprintf("%s: github.com/%s is private or does not exist, and no GitHub token was given",
					t.Name, repo),
				Override: `--github-token, or FFM_GITHUB_TOKEN (FFM_GITHUB_TOKEN="$(gh auth token)" with the GitHub CLI)`,
			})
		case status == http.StatusNotFound:
			// SSH with no token: see above.
		case status == http.StatusForbidden || status == http.StatusTooManyRequests:
			stop = true
			warn(fmt.Sprintf("GitHub would not answer (%d, usually the rate limit for calls without a token) — "+
				"the pinned commits are not checked; trying anyway", status))
		default:
			warn(fmt.Sprintf("could not check that github.com/%s still has %s's commit %s (GitHub answered %d) — trying anyway",
				repo, t.Name, short, status))
		}
	}
	return problems
}

// pinFailureSuffix explains a failed pin fetch: an authentication hint when
// git's output reads like one, else the ways out of a commit that is gone.
func pinFailureSuffix(app, out string, haveToken bool) string {
	if s := gitAuthSuffix(out, haveToken); s != "" {
		return s
	}
	return fmt.Sprintf("\nhint: if the commit is gone from the remote (force-push, deleted branch), restore with "+
		"--latest-apps to use the branch head, or --app %s=<git-url>@<branch>", app)
}
