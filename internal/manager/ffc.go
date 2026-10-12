package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/execx"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// ffcInstallCmd installs ffc on the host.
const ffcInstallCmd = "curl -fsSL https://raw.githubusercontent.com/nasroykh/foxmayn_frappe_cli/main/install.sh | sh"

var ffcVersionRE = regexp.MustCompile(`^ffc version (\S+) \(`)

// parseFfcVersion reads `ffc --version` ("ffc version 1.19.0 (abc, date)")
// and returns "1.19.0", or "" when the output is not that.
func parseFfcVersion(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if m := ffcVersionRE.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			return m[1]
		}
	}
	return ""
}

// FfcVersion reports the ffc version installed in a running dev bench's
// frappe container. It returns "" for anything else (prod bench, stopped
// bench, docker or ffc not answering): it is a status detail, never an error.
func (s *Service) FfcVersion(name string) string {
	b, err := s.GetBench(name)
	if err != nil || !b.IsDev() || s.LiveStatus(b) != StatusRunning {
		return ""
	}
	out, err := s.quietRunnerFor(b).ExecSilent("frappe", "ffc", "--version")
	if err != nil {
		return ""
	}
	return parseFfcVersion(out)
}

// errNoFfcKeys: the bench's ffc config has no entry (or no keys) for the bench.
var errNoFfcKeys = errors.New("no API keys for this bench in its ffc config")

// ffcHostConfig is the part of ffc's config.yaml FfcHost reads.
type ffcHostConfig struct {
	Sites map[string]struct {
		APIKey    string `yaml:"api_key"`
		APISecret string `yaml:"api_secret"`
	} `yaml:"sites"`
}

// benchFfcKeys reads the bench's API keys from the ffc config the container
// uses (home/ffc/config.yaml, a bind mount).
func benchFfcKeys(b state.Bench) (bench.APIKeys, error) {
	raw, err := os.ReadFile(filepath.Join(b.Dir, "home", "ffc", "config.yaml"))
	if err != nil {
		return bench.APIKeys{}, err
	}
	var cfg ffcHostConfig
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return bench.APIKeys{}, fmt.Errorf("parse ffc config: %w", err)
	}
	site, ok := cfg.Sites[b.Name]
	if !ok || site.APIKey == "" || site.APISecret == "" {
		return bench.APIKeys{}, errNoFfcKeys
	}
	return bench.APIKeys{Key: site.APIKey, Secret: site.APISecret}, nil
}

// hostFfc runs the host's ffc with secret on stdin. The output has the
// secret removed.
func hostFfc(secret string, args ...string) (string, error) {
	cmd := execx.Command("ffc", args...)
	if secret != "" {
		cmd.Stdin = strings.NewReader(secret + "\n")
	}
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "***")
	}
	return text, err
}

// FfcHost adds the bench as a site in the host's ffc, so AI clients on the
// host (Cursor, Claude Desktop, VS Code) can use the host's ffc. It reuses
// the keys the bench's own ffc already holds: minting new ones would revoke
// those. force replaces a host site of the same name.
func (s *Service) FfcHost(name string, force bool, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	if !b.IsDev() {
		return fmt.Errorf("ffc is set up on dev benches only; %q is a %s bench", b.Name, b.Mode)
	}
	if _, err := execx.LookPath("ffc"); err != nil {
		return fmt.Errorf("ffc is not installed on this machine; install it with:\n  %s", ffcInstallCmd)
	}

	keys, err := benchFfcKeys(b)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, errNoFfcKeys) {
			return err
		}
		pw.Printf("The bench has no ffc keys yet; setting up ffc in bench %q first...\n", name)
		if _, err := setupBenchAccess(s.runnerFor(b), b); err != nil {
			return err
		}
		if keys, err = benchFfcKeys(b); err != nil {
			return fmt.Errorf("ffc setup finished but the keys are still unreadable: %w", err)
		}
	}

	pw.Printf("Adding bench %q to the host's ffc...\n", name)
	url := "http://localhost:" + strconv.Itoa(b.WebPort)
	args := []string{"--no-input", "--name", b.Name, "--url", url, "--api-key", keys.Key, "--api-secret-stdin"}
	if force {
		args = append(args, "--force")
	}
	out, err := hostFfc(keys.Secret, append([]string{"site", "add"}, args...)...)
	if err != nil && (strings.Contains(out, "ffc init") || strings.Contains(out, "no config found")) {
		// No host config yet: `site add` needs one, `init` creates it.
		out, err = hostFfc(keys.Secret, append([]string{"init"}, args...)...)
	}
	if err != nil {
		if strings.Contains(out, "already exists") {
			return fmt.Errorf("the host's ffc already has a site named %q; pass --force to replace it", b.Name)
		}
		return fmt.Errorf("ffc site add on the host: %w\n%s\nIf the bench is stopped, run 'ffm start %s' first.", err, out, b.Name)
	}

	pingOut, pingErr := hostFfc("", "ping", "--site", b.Name)
	pw.Printf("\nSite %q added to the host's ffc (%s).\n", b.Name, url)
	if pingErr != nil {
		pw.Printf("  warning: 'ffc ping --site %s' failed; start the bench and retry it\n", b.Name)
		pw.Printf("           (%s)\n", pingOut)
	} else {
		pw.Printf("  ping: OK — %s\n", pingOut)
	}
	ro := ""
	if b.Agent && b.AgentReadOnly {
		ro = " --read-only"
	}
	pw.Printf("\nNext steps:\n")
	pw.Printf("  ffc mcp --site %s%s\n", b.Name, ro)
	pw.Printf("  ffc mcp install --client <claude-code|claude-desktop|cursor|vscode|codex> --site %s%s\n", b.Name, ro)
	pw.Printf("\n'ffm ffc %s' (without --host) mints new keys and breaks this entry: re-run 'ffm ffc %s --host --force' after it.\n", b.Name, b.Name)
	return nil
}

// SetupFFC generates API keys and writes ffc config inside the bench container.
func (s *Service) SetupFFC(name string, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	// ffc is installed only in the dev image. On prod this would mint a new
	// Administrator API secret (breaking whatever used the old one) and then
	// report success for a tool that is not there.
	if !b.IsDev() {
		return fmt.Errorf("ffc is set up on dev benches only; %q is a %s bench", b.Name, b.Mode)
	}
	runner := s.runnerFor(b)

	pw.Printf("Setting up ffc for bench %q...\n", name)
	who := "Administrator"
	if b.Agent {
		who = agentEmail(b)
	}
	pw.Printf("  [1] Minting API keys for %s and configuring ffc, .mcp.json and AGENTS.md\n", who)
	keys, err := setupBenchAccess(runner, b)
	if err != nil {
		return err
	}

	pw.Println("  [2] Verifying ffc connectivity (ffc ping)")
	out, pingErr := runner.ExecSilent("frappe", "bash", "-c", "ffc ping")

	pw.Printf("\nffc configured on bench %q.\n", name)
	pw.Printf("  api_key:    %s\n", keys.Key)
	pw.Printf("  api_secret: %s\n", SecretFor(pw, keys.Secret, "ffm ffc "+name+" --show-secrets, or home/ffc/config.yaml in the bench"))
	pw.Printf("  site:       %s\n", b.SiteName)
	if pingErr != nil {
		pw.Printf("  ping:       FAILED — start the bench and run 'ffc ping' to verify\n")
		pw.Printf("              (%s)\n", out)
	} else {
		pw.Printf("  ping:       OK — %s\n", out)
	}
	return nil
}
