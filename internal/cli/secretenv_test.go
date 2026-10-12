package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestEnvFallbackFlagWins(t *testing.T) {
	t.Setenv("FFM_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "from-github-token")
	t.Setenv(envAdminPassword, "from-env")

	run := func(args ...string) (admin, token string) {
		cmd := &cobra.Command{Use: "x", RunE: func(c *cobra.Command, _ []string) error {
			envFallback(c, "admin-password", &admin, envAdminPassword)
			envFallback(c, "github-token", &token, envGitHubTokens...)
			return nil
		}}
		cmd.Flags().StringVar(&admin, "admin-password", "admin", "")
		cmd.Flags().StringVar(&token, "github-token", "", "")
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if a, tok := run(); a != "from-env" || tok != "from-github-token" {
		t.Errorf("env: %q %q", a, tok)
	}
	if a, tok := run("--admin-password", "flag-pw", "--github-token", "flag-tok"); a != "flag-pw" || tok != "flag-tok" {
		t.Errorf("flags must win: %q %q", a, tok)
	}
	t.Setenv("FFM_GITHUB_TOKEN", "from-ffm")
	if _, tok := run(); tok != "from-ffm" {
		t.Errorf("FFM_GITHUB_TOKEN must win over GITHUB_TOKEN, got %q", tok)
	}
}

func TestPreferEnvHelp(t *testing.T) {
	if got := preferEnv(envGitHubTokens...); !strings.Contains(got, "FFM_GITHUB_TOKEN (or GITHUB_TOKEN)") {
		t.Errorf("preferEnv = %q", got)
	}
}
