package cli

import (
	"os"

	"github.com/spf13/cobra"
)

// Secrets given as flag values sit in ffm's own argument list, readable by
// every user on the machine (ps) and recorded in shell history and CI logs.
// Each such flag therefore has an environment variable; the flag still works
// and wins when both are set.
const (
	envAdminPassword = "FFM_ADMIN_PASSWORD"
	envDBPassword    = "FFM_DB_PASSWORD"
	envEncryptionKey = "FFM_ENCRYPTION_KEY"
)

// envGitHubTokens are the variables --github-token falls back to, in order.
var envGitHubTokens = []string{"FFM_GITHUB_TOKEN", "GITHUB_TOKEN"}

// envFallback fills *dst from the first non-empty variable of envs, unless the
// flag was given on the command line.
func envFallback(cmd *cobra.Command, flag string, dst *string, envs ...string) {
	if cmd.Flags().Changed(flag) {
		return
	}
	for _, e := range envs {
		if v := os.Getenv(e); v != "" {
			*dst = v
			return
		}
	}
}

// preferEnv is appended to the help of a secret-carrying flag.
func preferEnv(envs ...string) string {
	out := " Prefer the environment variable "
	for i, e := range envs {
		if i > 0 {
			out += " (or "
		}
		out += e
		if i > 0 {
			out += ")"
		}
	}
	return out + ": a flag value is visible in the process list."
}
