package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// agentEmail is the dedicated user an agent-ready bench gives ffc and the MCP
// server, so changes are attributed to it and its keys can be revoked without
// touching Administrator.
func agentEmail(b state.Bench) string { return "agent@" + b.SiteName }

// agentUserScript creates or re-enables the agent user with System Manager,
// mints its API keys and prints them as JSON on the last line. argv: site,
// email. It runs as Administrator, which frappe.connect sets.
const agentUserScript = `import sys, json, frappe
from frappe.core.doctype.user.user import generate_keys
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
email = sys.argv[2]
if not frappe.db.exists("User", email):
    frappe.get_doc({"doctype": "User", "email": email, "first_name": "Coding agent",
        "user_type": "System User", "send_welcome_email": 0}).insert(ignore_permissions=True)
user = frappe.get_doc("User", email)
user.enabled = 1
user.save(ignore_permissions=True)
user.add_roles("System Manager")
keys = generate_keys(email)
frappe.db.commit()
print("FFM_KEYS=" + json.dumps({"api_key": keys.get("api_key") or frappe.db.get_value("User", email, "api_key"), "api_secret": keys["api_secret"]}))
`

// disableAgentScript disables the agent user, which also stops its API keys.
const disableAgentScript = `import sys, frappe
frappe.init(site=sys.argv[1], sites_path=".")
frappe.connect()
if frappe.db.exists("User", sys.argv[2]):
    frappe.db.set_value("User", sys.argv[2], "enabled", 0)
    frappe.db.commit()
`

func runSiteScript(runner *bench.Runner, site, script string, args ...string) (string, error) {
	cmd := "cd /workspace/frappe-bench/sites && /workspace/frappe-bench/env/bin/python -c " + bench.ShellQuote(script) +
		" " + bench.ShellQuote(site)
	for _, a := range args {
		cmd += " " + bench.ShellQuote(a)
	}
	return runner.ExecSilent("frappe", "bash", "-c", cmd)
}

// agentKeys returns API keys for the agent user, creating it as needed.
func agentKeys(runner *bench.Runner, b state.Bench) (bench.APIKeys, error) {
	out, err := runSiteScript(runner, b.SiteName, agentUserScript, agentEmail(b))
	if err != nil {
		return bench.APIKeys{}, fmt.Errorf("create the agent user: %w\n%s", err, lastLines(out, 5))
	}
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "FFM_KEYS="); ok {
			var k struct {
				APIKey    string `json:"api_key"`
				APISecret string `json:"api_secret"`
			}
			if json.Unmarshal([]byte(v), &k) == nil && k.APIKey != "" && k.APISecret != "" {
				return bench.APIKeys{Key: k.APIKey, Secret: k.APISecret}, nil
			}
		}
	}
	// Not echoed: the output may hold the new secret.
	return bench.APIKeys{}, fmt.Errorf("create the agent user: no keys in the output (%d bytes)", len(out))
}

// configureFfc points the container's ffc at the bench's site with keys.
//
// `ffc site add` replaces only this bench's entry, so sites the user added
// stay; it needs a config file, so the first time is `ffc init`. Either way
// the secret reaches ffc on stdin rather than on a command line. A config
// that did not exist yet also gets the formats ffm always set.
func configureFfc(runner *bench.Runner, benchName string, keys bench.APIKeys) error {
	q := bench.ShellQuote
	site := " --no-input --name " + q(benchName) + " --url http://localhost:8000 --api-key " + q(keys.Key) + " --api-secret-stdin"
	cmd := "fresh=0; if [ -f ~/.config/ffc/config.yaml ]; then ffc site add --force" + site +
		"; else fresh=1; ffc init" + site + "; fi && ffc config set --default-site " + q(benchName) +
		` && if [ "$fresh" = 1 ]; then ffc config set --number-format french --date-format yyyy-mm-dd; fi`
	if err := runner.ExecStdin("frappe", strings.NewReader(keys.Secret+"\n"), "bash", "-c", cmd); err != nil {
		return fmt.Errorf("configure ffc: %w", err)
	}
	return nil
}

// setupBenchAccess gives ffc and the MCP server their credentials: the agent
// user on an agent-ready bench, Administrator otherwise. It rewrites
// .mcp.json (read-only or not) and the generated AGENTS.md.
func setupBenchAccess(runner *bench.Runner, b state.Bench) (bench.APIKeys, error) {
	var keys bench.APIKeys
	var err error
	if b.Agent {
		keys, err = agentKeys(runner, b)
	} else {
		keys, err = runner.GenerateAdminAPIKeys(b.SiteName)
	}
	if err != nil {
		return keys, err
	}
	if err := configureFfc(runner, b.Name, keys); err != nil {
		return keys, err
	}
	frappeBench := filepath.Join(b.Dir, "workspace", "frappe-bench")
	if err := writeClaudeMcpConfigHost(frappeBench, b.Name, b.Agent && b.AgentReadOnly); err != nil {
		return keys, err
	}
	if err := writeAgentsMD(b); err != nil {
		return keys, err
	}
	return keys, nil
}

// SetAgentInput turns the agent-ready profile on or off for a dev bench.
type SetAgentInput struct {
	Name     string
	On       bool
	ReadOnly bool
}

// SetAgent applies or removes the agent-ready profile on an existing bench.
//
// On refuses a bench whose ports reach the network or that forwards the host
// SSH agent (reconcile fixes both), replaces a default admin password with a
// random one, and moves ffc and MCP to the agent user. Off disables that user
// and moves them back to Administrator.
func (s *Service) SetAgent(in SetAgentInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	release, err := s.lockBench(in.Name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.GetBench(in.Name)
	if err != nil {
		return err
	}
	if !b.IsDev() {
		return fmt.Errorf("the agent profile is for dev benches; %q is a %s bench", b.Name, b.Mode)
	}
	if in.On {
		if b.PublishHost() == "" || b.SSHAgent {
			return fmt.Errorf("bench %q publishes its ports on every interface or forwards your SSH agent;"+
				" run 'ffm reconcile %s --loopback --no-ssh-agent' first", b.Name, b.Name)
		}
	}
	runner := s.runnerFor(b)
	newPassword := ""
	if in.On && b.AdminPassword == defaultAdminPassword {
		if newPassword, err = randomPassword(); err != nil {
			return err
		}
		pw.Step("Replacing the default Administrator password")
		cmd := "cd /workspace/frappe-bench && IFS= read -r p && bench --site " + bench.ShellQuote(b.SiteName) + ` set-admin-password "$p"`
		if err := runner.ExecStdin("frappe", strings.NewReader(newPassword+"\n"), "bash", "-c", cmd); err != nil {
			return fmt.Errorf("set the admin password: %w", err)
		}
		b.AdminPassword = newPassword
	}
	if !in.On && b.Agent {
		pw.Step("Disabling the agent user " + agentEmail(b))
		if out, err := runSiteScript(runner, b.SiteName, disableAgentScript, agentEmail(b)); err != nil {
			return fmt.Errorf("disable the agent user: %w\n%s", err, lastLines(out, 5))
		}
	}
	b.Agent, b.AgentReadOnly = in.On, in.On && in.ReadOnly
	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.Agent, rec.AgentReadOnly, rec.AdminPassword = b.Agent, b.AgentReadOnly, b.AdminPassword
	}); err != nil {
		return fmt.Errorf("update state: %w", err)
	}
	pw.Step("Configuring ffc and the MCP server")
	if _, err := setupBenchAccess(runner, b); err != nil {
		return err
	}
	if in.On {
		mode := "full"
		if b.AgentReadOnly {
			mode = "read-only"
		}
		pw.Printf("Bench %q is agent-ready: ffc and MCP (%s) act as %s.\n", b.Name, mode, agentEmail(b))
		if newPassword != "" {
			pw.Printf("  The Administrator password is now %s (also in 'ffm status %s --show-secrets').\n", newPassword, b.Name)
		}
	} else {
		pw.Printf("Agent profile off for %q: ffc and MCP act as Administrator again.\n", b.Name)
	}
	return nil
}

// agentsMDMarker starts every AGENTS.md ffm writes. ffm rewrites only files
// that begin with it, so a file the user wrote or edited is left alone once
// the marker line is removed.
const agentsMDMarker = "<!-- Generated by ffm; it rewrites this file while this line is here. -->"

// writeAgentsMD writes AGENTS.md (and a CLAUDE.md that imports it) at the
// root of a dev bench, where the coding agent starts.
func writeAgentsMD(b state.Bench) error {
	if !b.IsDev() {
		return nil
	}
	dir := filepath.Join(b.Dir, "workspace", "frappe-bench")
	path := filepath.Join(dir, "AGENTS.md")
	if raw, err := os.ReadFile(path); err == nil && !strings.HasPrefix(string(raw), agentsMDMarker) {
		return nil
	}
	if err := os.WriteFile(path, []byte(agentsMD(b)), 0o644); err != nil {
		return err
	}
	claude := filepath.Join(dir, "CLAUDE.md")
	if _, err := os.Stat(claude); os.IsNotExist(err) {
		return os.WriteFile(claude, []byte("@AGENTS.md\n"), 0o644)
	}
	return nil
}

func agentsMD(b state.Bench) string {
	identity := "Administrator (API keys minted by ffm)"
	if b.Agent {
		identity = "`" + agentEmail(b) + "`, a System Manager user ffm created for agents"
		if b.AgentReadOnly {
			identity += "; the MCP server exposes read tools only"
		}
	}
	var w strings.Builder
	fmt.Fprintf(&w, "%s\n# Frappe bench `%s`\n\n", agentsMDMarker, b.Name)
	fmt.Fprintf(&w, "You are inside the dev container of a Frappe bench managed by ffm (Foxmayn Frappe Manager).\n\n")
	fmt.Fprintf(&w, "## Where things are\n\n")
	fmt.Fprintf(&w, "- Bench: `/workspace/frappe-bench` (apps in `apps/`, the site in `sites/%s/`).\n", b.SiteName)
	fmt.Fprintf(&w, "- Frappe %s, Python %s, Node %s.\n", b.FrappeBranch, orDefault(b.Python, "image default"), orDefault(b.Node, "image default"))
	fmt.Fprintf(&w, "- Site `%s`: http://localhost:8000 from inside the container (the host uses http://localhost:%d).\n", b.SiteName, b.WebPort)
	fmt.Fprintf(&w, "- Mail: everything the site sends goes to Mailpit, http://mailpit:8025 (API: `/api/v1/messages`).\n")
	fmt.Fprintf(&w, "- Database: `bench --site %s mariadb` (or `postgres`) as the site's own user.\n\n", b.SiteName)
	fmt.Fprintf(&w, "## Talking to the site\n\n")
	fmt.Fprintf(&w, "- `ffc` is configured for this site (`ffc --site %s …`, e.g. `ffc list-docs DocType`), and the `frappe` MCP server in `.mcp.json` runs `ffc mcp`.\n", b.Name)
	fmt.Fprintf(&w, "- Both act as %s. Credentials live in `~/.config/ffc/config.yaml`; do not print or copy them.\n", identity)
	fmt.Fprintf(&w, "- Frappe and ffc skills are in `.claude/skills/` and `.agents/skills/`.\n\n")
	fmt.Fprintf(&w, "## Working\n\n")
	fmt.Fprintf(&w, "- Run an app's tests: `bench --site %s run-tests --app <app> [--module …|--doctype …]` (developer mode exports DocTypes that tests save; check `git status` in the app afterwards).\n", b.SiteName)
	fmt.Fprintf(&w, "- After changing DocTypes or hooks: `bench --site %s migrate`; after JS/CSS: `bench build --app <app>`.\n", b.SiteName)
	fmt.Fprintf(&w, "- The dev server (`bench start`) is already running; its log is `~/bench-start.log`.\n\n")
	fmt.Fprintf(&w, "## For the human, on the host\n\n")
	fmt.Fprintf(&w, "- `ffm snapshot create %s` before risky work, `ffm snapshot restore %s --yes` to roll the database back.\n", b.Name, b.Name)
	fmt.Fprintf(&w, "- `ffm test <app> %s`, `ffm login %s`, `ffm mail %s`, `ffm logs %s`.\n", b.Name, b.Name, b.Name, b.Name)
	return w.String()
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
