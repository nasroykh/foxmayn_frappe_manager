package manager

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nasroykh/foxmayn_frappe_manager/internal/archive"
	"github.com/nasroykh/foxmayn_frappe_manager/internal/state"
)

var testLoc = time.FixedZone("UTC+1", 3600)

// simulate returns the archives that survive when a scheduled run succeeds
// every `every` from start for `span`, pruning after each run exactly as
// run-due does. skip reports runs that did not happen (machine off).
func simulate(p state.BackupPolicy, start time.Time, span, every time.Duration,
	skip func(time.Time) bool) []ArchiveInfo {
	var live []ArchiveInfo
	for t := start; !t.After(start.Add(span)); t = t.Add(every) {
		if skip != nil && skip(t) {
			continue
		}
		a := ArchiveInfo{Header: Header{CreatedAt: t, Trigger: TriggerScheduled}}
		live = append([]ArchiveInfo{a}, live...)
		live, _ = selectRetained(live, p, testLoc)
	}
	return live
}

func TestPresetsBoundArchiveCountAndCoverage(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 30, 0, 0, testLoc)
	for _, every := range []int{1, 6, 24, 168} {
		p := PresetPolicy(every)
		live := simulate(p, start, 60*24*time.Hour, time.Duration(every)*time.Hour, nil)

		max := p.KeepHourly + p.KeepDaily + p.KeepWeekly
		if max < RetentionFloor {
			max = RetentionFloor
		}
		if len(live) > max {
			t.Errorf("every %dh: %d archives kept, want at most %d", every, len(live), max)
		}
		oldest := live[len(live)-1].CreatedAt()
		newest := live[0].CreatedAt()
		if cover := newest.Sub(oldest); cover < 21*24*time.Hour {
			t.Errorf("every %dh: coverage %v, want at least 3 weeks", every, cover)
		}
	}
}

func TestHourlyKeepsLastDayAtHourlyGranularity(t *testing.T) {
	start := time.Date(2026, 3, 2, 0, 5, 0, 0, testLoc)
	live := simulate(PresetPolicy(1), start, 10*24*time.Hour, time.Hour, nil)
	// The newest 24 archives are consecutive hours.
	for i := 1; i < 24; i++ {
		if gap := live[i-1].CreatedAt().Sub(live[i].CreatedAt()); gap != time.Hour {
			t.Fatalf("archive %d: gap %v to the previous one, want 1h", i, gap)
		}
	}
}

func TestFloorSurvivesLongOutage(t *testing.T) {
	// Daily backups, then nothing for 90 days: every archive is far older than
	// any tier, but the newest RetentionFloor must remain.
	p := state.BackupPolicy{EveryHours: 24, KeepDaily: 1}
	start := time.Date(2026, 1, 1, 3, 0, 0, 0, testLoc)
	var live []ArchiveInfo
	for i := 0; i < 10; i++ {
		live = append([]ArchiveInfo{{Header: Header{
			CreatedAt: start.Add(time.Duration(i) * 24 * time.Hour), Trigger: TriggerScheduled}}}, live...)
	}
	keep, _ := selectRetained(live, p, testLoc)
	if len(keep) != RetentionFloor {
		t.Fatalf("kept %d, want the floor of %d", len(keep), RetentionFloor)
	}
	for i := range keep {
		if keep[i].CreatedAt() != live[i].CreatedAt() {
			t.Fatalf("floor kept %v, want the newest archives", keep[i].CreatedAt())
		}
	}
}

func TestGapsDoNotShrinkCoverage(t *testing.T) {
	// Laptop off every weekend: hourly runs only on weekdays.
	weekend := func(t time.Time) bool {
		d := t.In(testLoc).Weekday()
		return d == time.Saturday || d == time.Sunday
	}
	start := time.Date(2026, 5, 4, 0, 0, 0, 0, testLoc)
	live := simulate(PresetPolicy(1), start, 45*24*time.Hour, time.Hour, weekend)
	if cover := live[0].CreatedAt().Sub(live[len(live)-1].CreatedAt()); cover < 21*24*time.Hour {
		t.Fatalf("coverage with weekend gaps = %v, want at least 3 weeks", cover)
	}
}

func TestSelectAcrossDSTTransition(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Skip("tzdata unavailable:", err)
	}
	// 2026-10-25 03:00 CEST falls back to 02:00 CET: two distinct UTC hours
	// share the local label 02:xx. Neither may be lost to a bucket collision
	// beyond what the policy asks for, and nothing may panic.
	start := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var live []ArchiveInfo
	for i := 0; i < 30; i++ {
		live = append([]ArchiveInfo{{Header: Header{
			CreatedAt: start.Add(time.Duration(i) * time.Hour), Trigger: TriggerScheduled}}}, live...)
	}
	keep, _ := selectRetained(live, state.BackupPolicy{KeepHourly: 24}, paris)
	if len(keep) != 24 {
		t.Fatalf("kept %d across the DST change, want 24", len(keep))
	}
}

// writeArchive writes a minimal archive: just a header member, which is all
// scanning reads.
func writeArchive(t *testing.T, dir, name string, h Header) string {
	t.Helper()
	path := filepath.Join(dir, name)
	w, err := archive.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(h)
	if _, err := w.AddBytes(archive.HeaderName, raw, archive.EncodingNone); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPruneNeverTouchesManualForeignOrUnreadable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("FFM_BACKUPS_DIR", root)
	dir := filepath.Join(root, "alpha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	hdr := func(i int, trigger, bench string) Header {
		h := NewHeader(fullBench(), "s", "16", "", []string{TierCore}, base.Add(time.Duration(i)*time.Hour))
		h.BenchName = bench
		h.Trigger = trigger
		return h
	}
	var scheduled []string
	for i := 0; i < 10; i++ {
		scheduled = append(scheduled, writeArchive(t, dir,
			archiveFileName("alpha", TriggerScheduled, base.Add(time.Duration(i)*time.Hour)),
			hdr(i, TriggerScheduled, "alpha")))
	}
	manual := writeArchive(t, dir, "alpha_old.ffm.tar", hdr(-1000, TriggerManual, "alpha"))
	legacy := writeArchive(t, dir, "alpha_legacy.ffm.tar", hdr(-2000, "", "alpha"))
	foreign := writeArchive(t, dir, "beta_x.auto.ffm.tar", hdr(-3000, TriggerScheduled, "beta"))
	garbage := filepath.Join(dir, "broken.auto.ffm.tar")
	os.WriteFile(garbage, []byte("not a tar"), 0o600)
	stale := filepath.Join(dir, "alpha_x.ffm.tar.partial")
	fresh := filepath.Join(dir, "alpha_y.ffm.tar.partial")
	os.WriteFile(stale, nil, 0o600)
	os.WriteFile(fresh, nil, 0o600)
	now := base.Add(20 * time.Hour)
	os.Chtimes(stale, now.Add(-2*time.Hour), now.Add(-2*time.Hour))
	os.Chtimes(fresh, now, now)

	p := state.BackupPolicy{KeepHourly: 4}
	dry, err := pruneWithPolicy("alpha", p, true, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Removed) != 6 || dry.Ignored != 4 {
		t.Fatalf("dry run: removed %d ignored %d, want 6 and 4", len(dry.Removed), dry.Ignored)
	}
	for _, path := range scheduled {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry run deleted %s", path)
		}
	}

	res, err := pruneWithPolicy("alpha", p, false, now, time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Kept) != 4 {
		t.Fatalf("kept %d, want 4", len(res.Kept))
	}
	for i, path := range scheduled {
		_, err := os.Stat(path)
		if gone := os.IsNotExist(err); gone != (i < 6) {
			t.Errorf("scheduled archive %d: removed=%v, want %v", i, gone, i < 6)
		}
	}
	for _, path := range []string{manual, legacy, foreign, garbage, fresh} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s must survive pruning: %v", filepath.Base(path), err)
		}
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale .partial not removed")
	}
}

// simulateWithFiles runs a schedule the way run-due does, deciding whether
// each run carries attachments from the archives that survived pruning.
// It returns the survivors and how many runs included attachments.
func simulateWithFiles(p state.BackupPolicy, start time.Time, span time.Duration) ([]ArchiveInfo, int) {
	var live []ArchiveInfo
	filesRuns := 0
	every := time.Duration(p.EveryHours) * time.Hour
	for t := start; !t.After(start.Add(span)); t = t.Add(every) {
		st := ScheduleStatus{Policy: p}
		for _, a := range live {
			if a.Header.HasTier(TierFiles) {
				st.LastFiles = a.CreatedAt()
				break
			}
		}
		tiers := []string{TierCore}
		if st.includeFiles(t) {
			tiers = append(tiers, TierFiles)
			filesRuns++
		}
		a := ArchiveInfo{Header: Header{CreatedAt: t, Trigger: TriggerScheduled, Tiers: tiers}}
		live = append([]ArchiveInfo{a}, live...)
		live, _ = selectRetained(live, p, testLoc)
	}
	return live, filesRuns
}

func TestRetentionKeepsAttachmentsThroughHistory(t *testing.T) {
	start := time.Date(2026, 2, 2, 0, 17, 0, 0, testLoc)
	live, _ := simulateWithFiles(PresetPolicy(1), start, 40*24*time.Hour)
	newest := live[0].CreatedAt()
	for _, a := range live {
		if newest.Sub(a.CreatedAt()) > 26*time.Hour && !a.Header.HasTier(TierFiles) {
			t.Errorf("archive from %v (%.0fh old) kept without attachments — the daily/weekly "+
				"tiers must prefer the archive that has them", a.CreatedAt(), newest.Sub(a.CreatedAt()).Hours())
		}
	}
	if cover := newest.Sub(live[len(live)-1].CreatedAt()); cover < 21*24*time.Hour {
		t.Errorf("coverage %v, want at least 3 weeks", cover)
	}
}

func TestWeeklyFilesCadenceIsWeekly(t *testing.T) {
	p := PresetPolicy(1)
	p.Files = FilesWeekly
	start := time.Date(2026, 2, 2, 0, 17, 0, 0, testLoc)
	live, filesRuns := simulateWithFiles(p, start, 28*24*time.Hour)
	// 28 days of hourly runs with weekly attachments: about 5 runs carry
	// them (first run + one per week). If the weekly archive were pruned,
	// the cadence would restart daily and this would approach 28.
	if filesRuns < 4 || filesRuns > 6 {
		t.Fatalf("%d runs carried attachments in 4 weeks, want ~5 (weekly)", filesRuns)
	}
	withFiles := 0
	for _, a := range live {
		if a.Header.HasTier(TierFiles) {
			withFiles++
		}
	}
	if withFiles < 4 {
		t.Fatalf("only %d archives with attachments survived, want one per retained week", withFiles)
	}
}
