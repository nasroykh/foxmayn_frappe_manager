package manager

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// ReconcileInput re-renders a bench's docker-compose.yml from its record.
type ReconcileInput struct {
	Name string
	// DryRun prints what would change and touches nothing.
	DryRun bool
	// Bind, when non-empty, changes how the ports are published
	// (state.BindLoopback or state.BindLAN) before rendering.
	Bind string
	// SSHAgent, when non-nil, turns host SSH agent forwarding on or off.
	SSHAgent *bool
}

// Reconcile regenerates docker-compose.yml from the bench record and the
// templates of this ffm version, then rolls it onto the running containers
// with `docker compose up -d`.
//
// It is the non-destructive way for template fixes to reach existing benches:
// unlike recreate it keeps the volumes, the workspace and the state record, so
// the only effect is that containers whose definition changed are replaced.
func (s *Service) Reconcile(in ReconcileInput, pw ProgressWriter) error {
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

	changed := false
	if in.Bind != "" && in.Bind != b.Bind {
		if in.Bind != state.BindLoopback && in.Bind != state.BindLAN {
			return fmt.Errorf("invalid bind %q", in.Bind)
		}
		b.Bind = in.Bind
		changed = true
	}
	if in.SSHAgent != nil && *in.SSHAgent != b.SSHAgent {
		b.SSHAgent = *in.SSHAgent
		changed = true
	}
	if err := checkReconcile(b, in); err != nil {
		return err
	}

	data, err := s.composeDataFor(b)
	if err != nil {
		return err
	}
	want, err := bench.RenderCompose(data)
	if err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	have, err := os.ReadFile(filepath.Join(b.Dir, "docker-compose.yml"))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read docker-compose.yml: %w", err)
	}

	dockerfileStale := s.dockerfileStale(b, data)
	if bytes.Equal(have, want) && !changed {
		if b.TemplateVersion != bench.TemplateVersion && !in.DryRun {
			// The file already matches; just record that it does.
			if err := s.UpdateBench(b.Name, func(rec *state.Bench) { rec.TemplateVersion = bench.TemplateVersion }); err != nil {
				return fmt.Errorf("update state: %w", err)
			}
		}
		pw.Printf("Bench %q is up to date.\n", b.Name)
		printDockerfileHint(pw, b.Name, dockerfileStale)
		return nil
	}

	if in.DryRun {
		pw.Printf("Bench %q: docker-compose.yml would change:\n", b.Name)
		for _, l := range lineDiff(string(have), string(want)) {
			pw.Println("  " + l)
		}
		pw.Println("  Lines marked - that you added by hand will be lost: move them into " + bench.OverrideFile + ",")
		pw.Println("  which ffm never writes and merges on top of docker-compose.yml.")
		printDockerfileHint(pw, b.Name, dockerfileStale)
		if b.PublishHost() == "" {
			pw.Println("  Ports: all interfaces (pass --loopback to bind them to 127.0.0.1)")
		} else {
			pw.Println("  Ports: 127.0.0.1 only")
		}
		return nil
	}

	if err := s.UpdateBench(b.Name, func(rec *state.Bench) {
		rec.Bind = b.Bind
		rec.SSHAgent = b.SSHAgent
		rec.TemplateVersion = bench.TemplateVersion
	}); err != nil {
		return fmt.Errorf("update state: %w", err)
	}

	pw.Printf("Reconciling bench %q...\n", b.Name)
	if err := s.applyDomainChange(b, pw); err != nil {
		return err
	}
	b.TemplateVersion = bench.TemplateVersion
	if err := ensureDevMail(b); err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: could not point outgoing mail at Mailpit: %v\n", err)
	}
	healed := s.healDevAccess(b, pw)
	pw.Printf("Done. Bench %q matches this ffm version's templates.\n", b.Name)
	printDockerfileHint(pw, b.Name, dockerfileStale)
	if b.IsDev() && !healed {
		// up -d replaces the frappe container, and on a dev bench some state
		// lives in its filesystem rather than in ./workspace.
		pw.Println("  Note: the frappe container was replaced. If ffc or Claude Code inside it lost their")
		pw.Printf("  configuration, run 'ffm ffc %s' and log in to Claude Code again.\n", b.Name)
	} else if b.IsDev() {
		pw.Println("  Claude Code keeps its login in " + filepath.Join(b.Dir, "home", "claude") + " from now on; log in once more.")
	}
	return nil
}

// devServerWait bounds how long ffm waits for a restarted dev server.
var devServerWait = 90 * time.Second

// healDevAccess sets ffc up again on a dev bench whose ./home/ffc holds no
// config: the bind mount is new, or the file was removed. It waits for the
// dev server first, because ffc checks the credentials against the site. It
// reports whether ffc is configured afterwards.
func (s *Service) healDevAccess(b state.Bench, pw ProgressWriter) bool {
	if !hasMailpit(b) || s.LiveStatus(b) != "running" {
		return false
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "home", "ffc", "config.yaml")); err == nil {
		return true
	} else if !os.IsNotExist(err) {
		return false
	}
	if err := bench.WaitForHTTP(fmt.Sprintf("http://localhost:%d", b.WebPort), devServerWait); err != nil {
		return false
	}
	pw.Step("Configuring ffc (its config now lives in " + filepath.Join(b.Dir, "home", "ffc") + ")")
	if _, err := setupBenchAccess(s.runnerFor(b), b); err != nil {
		fmt.Fprintf(pw.Stderr(), "warning: could not configure ffc: %v (run 'ffm ffc %s')\n", err, b.Name)
		return false
	}
	return true
}

// checkReconcile refuses combinations that would leave the bench broken or
// exposed.
func checkReconcile(b state.Bench, in ReconcileInput) error {
	if in.Bind == state.BindLAN && b.AdminPassword == defaultAdminPassword {
		return fmt.Errorf("--lan publishes the bench to other machines; bench %q still has the default admin password", b.Name)
	}
	if b.IsDev() && len(b.DomainAliases) > 0 && b.PublishHost() != "" {
		return fmt.Errorf("bench %q has domain aliases, which on a dev bench need its ports on the LAN; drop --loopback or remove the aliases first", b.Name)
	}
	if in.SSHAgent != nil && *in.SSHAgent && os.Getenv("SSH_AUTH_SOCK") == "" {
		return fmt.Errorf("--ssh-agent needs a running SSH agent (SSH_AUTH_SOCK is not set)")
	}
	return nil
}

// lineDiff lists the lines only in a ("- ") or only in b ("+ "), in order.
// It is a readable summary for --dry-run, not a minimal diff.
func lineDiff(a, b string) []string {
	al := strings.Split(a, "\n")
	bl := strings.Split(b, "\n")
	inA := make(map[string]int, len(al))
	for _, l := range al {
		inA[l]++
	}
	inB := make(map[string]int, len(bl))
	for _, l := range bl {
		inB[l]++
	}
	var out []string
	for _, l := range al {
		if inB[l] > 0 {
			inB[l]--
			continue
		}
		out = append(out, "- "+l)
	}
	for _, l := range bl {
		if inA[l] > 0 {
			inA[l]--
			continue
		}
		out = append(out, "+ "+l)
	}
	return out
}

// dockerfileStale reports whether the bench's Dockerfile differs from what
// this ffm version renders. Reconcile only applies docker-compose.yml: an
// image change needs a rebuild, which takes minutes and is left to the user.
func (s *Service) dockerfileStale(b state.Bench, data bench.ComposeData) bool {
	want, err := bench.RenderDockerfile(data)
	if err != nil {
		return false
	}
	have, err := os.ReadFile(filepath.Join(b.Dir, "Dockerfile"))
	return err == nil && !bytes.Equal(have, want)
}

func printDockerfileHint(pw ProgressWriter, name string, stale bool) {
	if stale {
		pw.Printf("  The image recipe (Dockerfile) changed too; rebuild with 'ffm restart %s --rebuild'.\n", name)
	}
}
