package bench

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner wraps docker compose commands scoped to a bench project.
type Runner struct {
	Project    string // e.g. "ffm-mybench"
	ComposeDir string // directory containing docker-compose.yml
	Verbose    bool
}

func NewRunner(benchName, composeDir string, verbose bool) *Runner {
	return &Runner{
		Project:    ProjectName(benchName),
		ComposeDir: composeDir,
		Verbose:    verbose,
	}
}

// compose builds a docker compose command with project and file scoping.
func (r *Runner) compose(args ...string) *exec.Cmd {
	full := append([]string{"compose", "-p", r.Project, "-f", r.ComposeDir + "/docker-compose.yml"}, args...)
	cmd := exec.Command("docker", full...)
	if r.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd
}

// composeWithIO builds a command that always routes stdin/stdout/stderr to the
// terminal — used for interactive or streaming commands (exec, logs).
func (r *Runner) composeWithIO(args ...string) *exec.Cmd {
	full := append([]string{"compose", "-p", r.Project, "-f", r.ComposeDir + "/docker-compose.yml"}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// withOutput attaches stdout/stderr to the terminal when verbose is on,
// or discards output when verbose is off.
func (r *Runner) withOutput(cmd *exec.Cmd) *exec.Cmd {
	if r.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd
}

// Build builds the Docker image for the compose project, always streaming
// output so the user can see progress (the build takes several minutes).
func (r *Runner) Build() error {
	args := []string{"compose", "-p", r.Project, "-f", r.ComposeDir + "/docker-compose.yml", "build"}
	cmd := exec.Command("docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// Up starts all services detached.
func (r *Runner) Up() error {
	return r.withOutput(r.compose("up", "-d")).Run()
}

// Down stops and removes containers and volumes.
// --remove-orphans ensures containers from a previous compose config revision
// are also removed, preventing stale containers from blocking a re-create.
func (r *Runner) Down(removeVolumes bool) error {
	args := []string{"down", "--remove-orphans"}
	if removeVolumes {
		args = append(args, "-v")
	}
	return r.withOutput(r.compose(args...)).Run()
}

// Start starts existing stopped containers.
func (r *Runner) Start() error {
	return r.withOutput(r.compose("start")).Run()
}

// Stop stops running containers without removing them.
func (r *Runner) Stop() error {
	return r.withOutput(r.compose("stop")).Run()
}

// Exec runs a command inside a service container interactively.
func (r *Runner) Exec(service string, shellArgs ...string) error {
	args := append([]string{"exec", service}, shellArgs...)
	return r.composeWithIO(args...).Run()
}

// ExecInDir runs an interactive command inside a container at the given workdir.
func (r *Runner) ExecInDir(service, workdir string, shellArgs ...string) error {
	args := append([]string{"exec", "-w", workdir, service}, shellArgs...)
	return r.composeWithIO(args...).Run()
}

// ExecOutputInDir runs a non-interactive command inside a service container at
// the given workdir, streaming stdout/stderr directly to the terminal.
// Unlike ExecSilent it does not capture output; unlike Exec it allocates no TTY.
func (r *Runner) ExecOutputInDir(service, workdir string, shellArgs ...string) error {
	args := append([]string{"exec", "-T", "-w", workdir, service}, shellArgs...)
	full := append([]string{"compose", "-p", r.Project, "-f", r.ComposeDir + "/docker-compose.yml"}, args...)
	cmd := exec.Command("docker", full...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ExecSilent runs a command inside a service container, capturing output.
func (r *Runner) ExecSilent(service string, shellArgs ...string) (string, error) {
	args := append([]string{"exec", "-T", service}, shellArgs...)
	full := append([]string{"compose", "-p", r.Project, "-f", r.ComposeDir + "/docker-compose.yml"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// ExecDetached runs a command inside a service container without waiting.
func (r *Runner) ExecDetached(service string, shellArgs ...string) error {
	args := append([]string{"exec", "-d", service}, shellArgs...)
	cmd := r.compose(args...)
	return cmd.Run()
}

// Logs streams logs to stdout. If service is empty, all services are shown.
func (r *Runner) Logs(follow bool, service string) error {
	args := []string{"logs"}
	if follow {
		args = append(args, "-f")
	}
	if service != "" {
		args = append(args, service)
	}
	return r.composeWithIO(args...).Run()
}

// PS returns the raw output of docker compose ps.
func (r *Runner) PS(format string) (string, error) {
	args := []string{"ps"}
	if format != "" {
		args = append(args, "--format", format)
	}
	full := append([]string{"compose", "-p", r.Project, "-f", r.ComposeDir + "/docker-compose.yml"}, args...)
	out, err := exec.Command("docker", full...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// WaitForMariaDB polls until MariaDB accepts connections or the timeout elapses.
func (r *Runner) WaitForMariaDB(password string, timeout time.Duration, progressWriter io.Writer) error {
	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		out, err := r.ExecSilent("mariadb",
			"mariadb", "-u", "root", "-p"+password, "-e", "SELECT 1")
		if err == nil && strings.Contains(out, "1") {
			return nil
		}
		attempt++
		if progressWriter != nil {
			fmt.Fprintf(progressWriter, "\r  waiting for MariaDB... (%ds elapsed)", int(time.Since(deadline.Add(-timeout)).Seconds()))
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("MariaDB did not become ready within %s", timeout)
}

// WaitForHTTP polls until the given URL returns a non-error response.
func WaitForHTTP(url string, timeout time.Duration) error {
	// We use a TCP probe instead of an HTTP client to avoid importing net/http.
	// Frappe's dev server binds the port before it fully responds; a small extra
	// sleep in the caller accounts for that.
	host := strings.TrimPrefix(url, "http://")
	host = strings.Split(host, "/")[0]

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", host, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("server at %s did not become reachable within %s", url, timeout)
}
