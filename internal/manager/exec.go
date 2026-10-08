package manager

import (
	"fmt"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
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
	runner := bench.NewRunner(b.Name, b.Dir, s.Verbose)
	workdir := "/workspace/frappe-bench"
	if service != "frappe" {
		workdir = ""
	}
	if in.Command == "" {
		return "", fmt.Errorf("command is required")
	}
	return runner.ExecSilent(service, "bash", "-c", "cd "+workdir+" && "+in.Command)
}
