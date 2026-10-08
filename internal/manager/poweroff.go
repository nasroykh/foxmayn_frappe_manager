package manager

import (
	"fmt"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/proxy"
)

// Poweroff stops every running bench and then, unless keepProxy, the shared
// proxy. A bench that fails to stop is reported and the rest still stop.
func (s *Service) Poweroff(keepProxy bool, pw ProgressWriter) error {
	if pw == nil {
		pw = CLIProgress{}
	}
	benches, err := s.LoadBenches()
	if err != nil {
		return err
	}
	var failed []string
	stopped := 0
	for _, b := range benches {
		if s.LiveStatus(b) == "stopped" {
			continue
		}
		if err := s.Stop(b.Name, pw); err != nil {
			pw.Printf("  could not stop %q: %v\n", b.Name, err)
			failed = append(failed, b.Name)
			continue
		}
		stopped++
	}
	if !keepProxy && proxy.IsRunning() {
		if err := s.ProxyStop(pw); err != nil {
			pw.Printf("  could not stop the proxy: %v\n", err)
			failed = append(failed, "proxy")
		}
	}
	pw.Printf("Stopped %d bench(es).\n", stopped)
	if len(failed) > 0 {
		return fmt.Errorf("could not stop: %v", failed)
	}
	return nil
}
