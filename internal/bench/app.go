package bench

import "strings"

// AppSpec represents a single app to install during bench creation.
//
// Supported formats for raw --apps values:
//
//	erpnext                                → short name, branch defaults to frappeBranch
//	erpnext@version-16                     → short name with explicit branch override
//	git@github.com:org/app.git             → SSH URL, bench get-app picks default branch
//	git@github.com:org/app.git@main        → SSH URL with explicit branch
//	https://github.com/org/app             → HTTPS URL, bench get-app picks default branch
//	https://github.com/org/app@main        → HTTPS URL with explicit branch
type AppSpec struct {
	// Source is the value passed to bench get-app (name or URL, without @branch suffix).
	Source string
	// Branch is the git branch to check out.
	// Empty string means no --branch flag is passed to bench get-app.
	Branch string
	// IsURL reports whether Source is a git URL rather than a short app name.
	IsURL bool
}

// ParseAppSpec parses a raw --apps value into an AppSpec.
// frappeBranch is used as the default branch for short named apps.
func ParseAppSpec(raw, frappeBranch string) AppSpec {
	isURL := strings.Contains(raw, "://") || strings.HasPrefix(raw, "git@")

	source := raw
	branch := ""

	switch {
	case strings.HasPrefix(raw, "git@"):
		// SSH URL: git@HOST:PATH  or  git@HOST:PATH@BRANCH
		// Split by "@" with a limit of 3: ["git", "HOST:PATH", "BRANCH"]
		parts := strings.SplitN(raw, "@", 3)
		if len(parts) == 3 {
			source = "git@" + parts[1]
			branch = parts[2]
		}
		// else: source = raw, branch = "" (no explicit branch)

	case strings.Contains(raw, "://"):
		// HTTPS URL: https://HOST/PATH  or  https://HOST/PATH@BRANCH
		// Find the start of the path component (first "/" after "://").
		schemeEnd := strings.Index(raw, "://") + 3
		slashInPath := strings.Index(raw[schemeEnd:], "/")
		if slashInPath >= 0 {
			pathStart := schemeEnd + slashInPath
			pathPart := raw[pathStart:]
			if atIdx := strings.LastIndex(pathPart, "@"); atIdx >= 0 {
				source = raw[:pathStart+atIdx]
				branch = pathPart[atIdx+1:]
			}
		}
		// else: no path, treat whole thing as source

	default:
		// Short app name: "erpnext"  or  "erpnext@version-16"
		if s, b, found := strings.Cut(raw, "@"); found {
			source = s
			branch = b
		} else {
			// No explicit branch — use the Frappe branch as convention.
			branch = frappeBranch
		}
	}

	return AppSpec{Source: source, Branch: branch, IsURL: isURL}
}

// GetAppCmd returns the bench get-app shell command for this spec.
func (a AppSpec) GetAppCmd() string {
	if a.Branch != "" {
		return "bench get-app --branch " + a.Branch + " " + a.Source
	}
	return "bench get-app " + a.Source
}

// DisplayName returns a short human-readable label for log output.
// For URL apps it extracts the repo name from the path; for named apps it returns Source.
func (a AppSpec) DisplayName() string {
	if !a.IsURL {
		return a.Source
	}
	// Strip trailing ".git" and take the last path segment.
	s := strings.TrimSuffix(a.Source, ".git")
	if idx := strings.LastIndexAny(s, "/:"); idx >= 0 {
		s = s[idx+1:]
	}
	return s
}
