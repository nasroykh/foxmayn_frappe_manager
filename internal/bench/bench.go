// Package bench contains the core logic for creating and managing Frappe benches.
package bench

import (
	"fmt"
	"regexp"
	"strings"
)

var validName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9-]{0,62}$`)

// ValidateName returns an error if name is not a valid bench identifier.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("bench name %q is invalid: must start with a letter, contain only letters/digits/hyphens, max 63 chars", name)
	}
	return nil
}

// reservedNames are subcommands of `ffm backup`. A bench with one of these
// names could not be addressed as `ffm backup <name>`, because Cobra resolves
// the subcommand first.
var reservedNames = map[string]bool{
	"list": true, "schedule": true, "prune": true, "run-due": true, "scheduler": true,
}

// ValidateNewName is ValidateName plus the names reserved for new benches.
// Existing benches keep working under a reserved name; only creating one is
// refused.
func ValidateNewName(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if reservedNames[strings.ToLower(name)] {
		return fmt.Errorf("bench name %q is reserved (it is an 'ffm backup' subcommand) — choose another", name)
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
