package manager

import (
	"fmt"
	"os"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
)

// Restart stops then starts a bench, optionally rebuilding the image first.
func (s *Service) Restart(in RestartInput, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	if err := s.Stop(in.Name, pw); err != nil {
		return err
	}
	if in.Rebuild {
		b, err := s.GetBench(in.Name)
		if err != nil {
			return err
		}
		pw.Printf("Updating Dockerfile for bench %q...\n", in.Name)
		if err := bench.WriteDockerfile(b.Dir, bench.ComposeData{Mode: b.Mode, DBType: b.DBEngine()}); err != nil {
			return fmt.Errorf("write Dockerfile: %w", err)
		}
		if b.IsProd() {
			pw.Printf("Updating wsgi.py for bench %q...\n", in.Name)
			if err := bench.WriteWsgiWrapper(b.Dir, b.SiteName); err != nil {
				return fmt.Errorf("write wsgi.py: %w", err)
			}
		}
		if err := bench.PatchAuthenticateJs(b.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not patch authenticate.js: %v\n", err)
		}
		if err := bench.PatchUtilsJs(b.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not patch utils.js: %v\n", err)
		}
		runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
		pw.Printf("Rebuilding image for bench %q...\n", in.Name)
		if err := runner.Build(); err != nil {
			return fmt.Errorf("docker compose build: %w", err)
		}
	}
	return s.Start(in.Name, pw)
}
