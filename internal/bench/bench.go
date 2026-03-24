// Package bench contains the core logic for creating and managing Frappe benches.
package bench

import (
	"fmt"
	"regexp"
)

var validName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,62}$`)

// ValidateName returns an error if name is not a valid bench identifier.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("bench name %q is invalid: must start with a letter, contain only letters/digits/hyphens, max 63 chars", name)
	}
	return nil
}

// ProjectName returns the docker compose project name for a bench.
func ProjectName(name string) string {
	return "ffm-" + name
}

// ContainerName returns the full container name for a service within a bench.
func ContainerName(benchName, service string) string {
	return fmt.Sprintf("ffm-%s-%s-1", benchName, service)
}
