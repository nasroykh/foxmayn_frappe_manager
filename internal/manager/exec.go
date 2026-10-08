package manager

import (
	"fmt"
)

// Exec runs a one-shot command in a bench container (ffm shell --exec).
func (s *Service) Exec(in ExecInput) (string, error) {
	b, err := s.GetBench(in.BenchName)
	if err != nil {
		return "", err
	}
	service := in.Service
	if service == "" {
		service = "frappe"
	}
	runner := s.runnerFor(b)
	workdir := "/workspace/frappe-bench"
	if service != "frappe" {
		workdir = ""
	}
	if in.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	return runner.ExecSilent(service, "bash", "-c", "cd "+workdir+" && "+in.Command)
}
