package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/config"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

// RetentionFloor is how many of the newest scheduled archives are kept
// whatever the policy says and however old they are. It is what stops a
// bench whose backups have been failing for a month from being pruned down
// to nothing by age-based tiers.
const RetentionFloor = 3

// partialMaxAge is how old a leftover .partial file must be before pruning
// removes it. Under the bench lock no ffm backup can be writing one, but an
// ffm older than the lock could be; an hour is far beyond any real backup.
const partialMaxAge = time.Hour

// ArchiveInfo is one archive found in a bench's backup directory.
type ArchiveInfo struct {
	Path   string
	Size   int64
	Header Header
	// Err is set when the header could not be read. Such a file is listed
	// but never pruned: ffm does not delete what it cannot identify.
	Err error
}

// CreatedAt is when the archive was taken, per its header.
func (a ArchiveInfo) CreatedAt() time.Time { return a.Header.CreatedAt }

// ScanArchives lists the archives in a bench's backup directory, newest
// first. Only *.ffm.tar files are considered; .partial files are not archives.
func ScanArchives(benchName string) ([]ArchiveInfo, error) {
	dir := config.BenchBackupsDir(benchName)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []ArchiveInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ffm.tar") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info := ArchiveInfo{Path: path}
		if fi, err := e.Info(); err == nil {
			info.Size = fi.Size()
		}
		raw, err := archive.PeekHeader(path)
		if err == nil {
			info.Header, err = ParseHeader(raw)
		}
		info.Err = err
		out = append(out, info)
	}
	sort.SliceStable(out, func(i, j int) bool {
		// Unreadable archives have a zero time and sort last.
		return out[i].CreatedAt().After(out[j].CreatedAt())
	})
	return out, nil
}

// prunable reports whether an archive is one scheduled pruning may consider:
// readable, written by a scheduled run, and for this bench.
func prunable(a ArchiveInfo, benchName string) bool {
	return a.Err == nil && a.Header.IsScheduled() && a.Header.BenchName == benchName
}

// selectRetained splits scheduled archives (newest first) into those the
// policy keeps and those it drops. An archive kept by any rule survives.
func selectRetained(archives []ArchiveInfo, p state.BackupPolicy, loc *time.Location) (keep, drop []ArchiveInfo) {
	keepIdx := map[int]bool{}
	for i := 0; i < len(archives) && i < RetentionFloor; i++ {
		keepIdx[i] = true
	}
	tiers := []struct {
		limit  int
		bucket func(time.Time) string
	}{
		{p.KeepHourly, func(t time.Time) string { return t.Format("2006-01-02T15") }},
		{p.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") }},
		{p.KeepWeekly, func(t time.Time) string {
			y, w := t.ISOWeek()
			return fmt.Sprintf("%d-W%02d", y, w)
		}},
	}
	for _, tier := range tiers {
		if tier.limit <= 0 {
			continue
		}
		seen := map[string]bool{}
		for i, a := range archives {
			if len(seen) >= tier.limit {
				break
			}
			key := tier.bucket(a.CreatedAt().In(loc))
			if seen[key] {
				continue
			}
			// Newest first, so the first archive met in a bucket is its newest.
			seen[key] = true
			keepIdx[i] = true
		}
	}
	for i, a := range archives {
		if keepIdx[i] {
			keep = append(keep, a)
		} else {
			drop = append(drop, a)
		}
	}
	return keep, drop
}

// PruneResult reports what a prune removed (or, dry-run, would remove).
type PruneResult struct {
	Kept     []ArchiveInfo
	Removed  []ArchiveInfo
	Partials []string
	// Ignored counts archives outside pruning's reach: manual, unreadable or
	// belonging to another bench.
	Ignored int
}

// PruneBackups applies the bench's retention policy to its scheduled
// archives. Manual archives, unreadable files and other benches' archives are
// never touched.
func (s *Service) PruneBackups(benchName string, dryRun bool) (PruneResult, error) {
	b, err := s.GetBench(benchName)
	if err != nil {
		return PruneResult{}, err
	}
	if b.BackupSchedule == nil {
		return PruneResult{}, fmt.Errorf("bench %q has no backup schedule, so there is no retention "+
			"policy to apply — set one with 'ffm backup schedule %s'", benchName, benchName)
	}
	release, err := s.lockBench(benchName)
	if err != nil {
		return PruneResult{}, err
	}
	defer release()
	return pruneWithPolicy(benchName, *b.BackupSchedule, dryRun, s.clock(), time.Local)
}

func pruneWithPolicy(benchName string, p state.BackupPolicy, dryRun bool, now time.Time, loc *time.Location) (PruneResult, error) {
	all, err := ScanArchives(benchName)
	if err != nil {
		return PruneResult{}, err
	}
	var res PruneResult
	var candidates []ArchiveInfo
	for _, a := range all {
		if prunable(a, benchName) {
			candidates = append(candidates, a)
		} else {
			res.Ignored++
		}
	}
	res.Kept, res.Removed = selectRetained(candidates, p, loc)

	partials, _ := filepath.Glob(filepath.Join(config.BenchBackupsDir(benchName), "*.partial"))
	for _, path := range partials {
		if fi, err := os.Stat(path); err == nil && now.Sub(fi.ModTime()) > partialMaxAge {
			res.Partials = append(res.Partials, path)
		}
	}
	if dryRun {
		return res, nil
	}
	for _, a := range res.Removed {
		if err := os.Remove(a.Path); err != nil && !os.IsNotExist(err) {
			return res, fmt.Errorf("remove %s: %w", filepath.Base(a.Path), err)
		}
	}
	for _, path := range res.Partials {
		_ = os.Remove(path)
	}
	return res, nil
}
