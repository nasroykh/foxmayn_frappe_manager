package manager

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// Attachment cadences for BackupPolicy.Files.
const (
	FilesEveryRun = "every-run"
	FilesDaily    = "daily"
	FilesWeekly   = "weekly"
	FilesNever    = "never"
)

// PresetPolicy is the policy `ffm backup schedule --every <n>h` gets when no
// retention flags are given: at least three weeks between the newest and
// oldest archive at every interval, with attachments daily for sub-daily
// schedules so hourly runs stay small.
//
// Sub-weekly presets keep 5 weekly archives, not 4: the weekly tier counts
// the CURRENT week, whose newest archive is today's, so 4 would reach back
// only 2-3 weeks depending on the weekday. A weekly schedule's 4 archives are
// 4 distinct weeks apart already.
func PresetPolicy(everyHours int) state.BackupPolicy {
	p := state.BackupPolicy{Enabled: true, EveryHours: everyHours, KeepWeekly: 5}
	if everyHours >= 168 {
		p.KeepWeekly = 4
	}
	switch {
	case everyHours < 24:
		p.KeepHourly = 24 / everyHours
		if p.KeepHourly < 1 {
			p.KeepHourly = 1
		}
		p.KeepDaily = 7
		p.Files = FilesDaily
	case everyHours < 168:
		p.KeepDaily = 7
		p.Files = FilesEveryRun
	default:
		p.Files = FilesEveryRun
	}
	return p
}

// ParseEvery reads an interval: 1h, 6h, 24h, 168h, daily or weekly, or a
// bare number of hours.
func ParseEvery(s string) (int, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "hourly":
		return 1, nil
	case "daily":
		return 24, nil
	case "weekly":
		return 168, nil
	}
	v = strings.TrimSuffix(v, "h")
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("invalid interval %q: use a whole number of hours such as 1h, 6h, 24h, "+
			"168h, or hourly/daily/weekly", s)
	}
	return n, nil
}

// ValidatePolicy checks a policy before it is saved.
func ValidatePolicy(p state.BackupPolicy) error {
	if p.EveryHours < 1 {
		return fmt.Errorf("the interval must be at least 1h")
	}
	if p.KeepHourly < 0 || p.KeepDaily < 0 || p.KeepWeekly < 0 {
		return fmt.Errorf("retention counts cannot be negative")
	}
	if p.KeepHourly+p.KeepDaily+p.KeepWeekly == 0 {
		return fmt.Errorf("the policy keeps nothing beyond the floor of %d — set --keep or a "+
			"--keep-hourly/--keep-daily/--keep-weekly count", RetentionFloor)
	}
	switch p.Files {
	case FilesEveryRun, FilesDaily, FilesWeekly, FilesNever:
	default:
		return fmt.Errorf("invalid --files %q: use every-run, daily, weekly or never", p.Files)
	}
	return nil
}

// ApplyKeepShorthand sets `--keep N` on the tier matching the interval.
func ApplyKeepShorthand(p *state.BackupPolicy, n int) {
	p.KeepHourly, p.KeepDaily, p.KeepWeekly = 0, 0, 0
	switch {
	case p.EveryHours < 24:
		p.KeepHourly = n
	case p.EveryHours < 168:
		p.KeepDaily = n
	default:
		p.KeepWeekly = n
	}
}
