package manager

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/bench"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// SiteState is what `ffm site` reports.
type SiteState struct {
	Site        string `json:"site"`
	Maintenance bool   `json:"maintenance"`
	// Scheduler is Frappe's own word: enabled, disabled, paused, or "" when
	// the bench is not running.
	Scheduler string `json:"scheduler,omitempty"`
}

func siteCmd(b state.Bench, args string) string {
	return "cd /workspace/frappe-bench && bench --site " + bench.ShellQuote(b.SiteName) + " " + args
}

// SiteStatus reads maintenance mode from site_config.json and the scheduler
// state from Frappe.
func (s *Service) SiteStatus(name string) (SiteState, error) {
	b, err := s.GetBench(name)
	if err != nil {
		return SiteState{}, err
	}
	st := SiteState{Site: b.SiteName}
	cfg, err := readJSONFile(filepath.Join(b.Dir, "workspace", "frappe-bench", "sites", b.SiteName, "site_config.json"))
	if err != nil {
		return st, err
	}
	st.Maintenance = truthy(cfg["maintenance_mode"])
	if s.LiveStatus(b) == StatusRunning {
		out, err := s.quietRunnerFor(b).ExecSilent("frappe", "bash", "-c", siteCmd(b, "scheduler status --format json"))
		if err == nil {
			for _, l := range strings.Split(out, "\n") {
				var v struct {
					Status string `json:"status"`
				}
				if json.Unmarshal([]byte(strings.TrimSpace(l)), &v) == nil && v.Status != "" {
					st.Scheduler = strings.ToLower(v.Status)
				}
			}
		}
	}
	return st, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "1" || strings.EqualFold(t, "true")
	}
	return false
}

// SiteMigrate runs `bench --site <site> migrate`, streaming its output.
func (s *Service) SiteMigrate(name string, out io.Writer) error {
	release, err := s.lockBench(name)
	if err != nil {
		return err
	}
	defer release()
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	if st := s.LiveStatus(b); st != StatusRunning {
		return fmt.Errorf("%w: start it first (ffm start %s)", ErrBenchStopped, b.Name)
	}
	return s.migrateLocked(b, out)
}

func (s *Service) migrateLocked(b state.Bench, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	if err := s.runnerFor(b).ExecTo("frappe", "/workspace/frappe-bench", out, out, "bash", "-c", siteCmd(b, "migrate")); err != nil {
		return fmt.Errorf("bench migrate: %w", err)
	}
	return nil
}

// SiteMaintenance turns Frappe's maintenance mode on or off: visitors get a
// maintenance page, Administrator can still log in.
func (s *Service) SiteMaintenance(name string, on bool) error {
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	return s.setMaintenance(b, on)
}

func (s *Service) setMaintenance(b state.Bench, on bool) error {
	arg := "off"
	if on {
		arg = "on"
	}
	if out, err := s.runnerFor(b).ExecSilent("frappe", "bash", "-c", siteCmd(b, "set-maintenance-mode "+arg)); err != nil {
		return fmt.Errorf("set-maintenance-mode %s: %w\n%s", arg, err, lastLines(out, 5))
	}
	return nil
}

// Scheduler actions `ffm site scheduler` accepts, mapped to Frappe's.
var schedulerActions = map[string]string{"on": "enable", "off": "disable", "pause": "pause", "resume": "resume"}

// SiteScheduler enables, disables, pauses or resumes Frappe's scheduler.
// Pause keeps the setting and stops jobs until resume; it is what an update
// uses, so a user's own "disabled" survives it.
func (s *Service) SiteScheduler(name, action string) error {
	b, err := s.GetBench(name)
	if err != nil {
		return err
	}
	return s.setScheduler(b, action)
}

func (s *Service) setScheduler(b state.Bench, action string) error {
	verb, ok := schedulerActions[action]
	if !ok {
		return fmt.Errorf("scheduler action %q: on, off, pause or resume", action)
	}
	if out, err := s.runnerFor(b).ExecSilent("frappe", "bash", "-c", siteCmd(b, "scheduler "+verb)); err != nil {
		return fmt.Errorf("scheduler %s: %w\n%s", verb, err, lastLines(out, 5))
	}
	return nil
}
