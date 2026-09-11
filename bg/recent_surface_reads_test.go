package bg

import (
	"os"
	"regexp"
	"testing"
)

// TestRecentWindowReadsUseCurrentMonthSurface extends the
// passive_probe_recent_surface_test.go doctrine (2026-09-10 minimax-prod-v2
// incident) to the remaining bg workers with recent-window request_logs
// reads. The bare request_logs parent only holds cold rows — its max(ts) ran
// a full day+ stale on 154 while request_logs_hot served live traffic — so
// every recent-window read against it is structurally blind.
//
// Covered files (2026-09-11 audit round):
//   - candidate_failure_monitor.go: 5-minute staleness probe + auto-cool
//     attempt window (staleness never fired / auto-cool never fired)
//   - shared_pick.go: 7-day most-used probe-model pick (stale pick)
//   - daily_probe_audit.go: 3-day lookback for daily submissions (under-scan)
//
// Deliberately NOT covered: integrity_fingerprint_drift.go still reads the
// bare parent because system_fingerprint is absent from the view (it is not
// in the request_logs_hot ∩ request_logs column intersection — the hot table
// is missing the column, same drift family as the 573 body-column gap).
// Switching it today would 42703 at runtime; fix the hot-table column first.
//
// 2026-09-12 audit round: same doctrine extended to candidate_failure_logs —
// the writer (domains/streaming/executors/candidate_failure_logger.go)
// inserts into candidate_failure_logs_hot, so the bare parent only holds
// promoted (cold) rows; with the partition promote failing (P5 duplicate key,
// since 09-10) its max(ts) ran 8h+ stale while the hot table was served
// minutes earlier. Covered files:
//   - candidate_failure_monitor.go: staleness max(ts) + 5-minute alert scan
//     + auto-cool cfl CTE (stale alert fired / alerts + auto-cool blind)
//   - daily_probe_audit.go: 3-day candidate branch (under-scan)
//   - model_probe.go: 5-minute passive-boost pick (always empty)
func TestRecentWindowReadsUseCurrentMonthSurface(t *testing.T) {
	re := regexp.MustCompile(`(?i)FROM\s+request_logs(\w*)`)
	for _, file := range []string{
		"candidate_failure_monitor.go",
		"shared_pick.go",
		"daily_probe_audit.go",
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		src := string(contents)
		matches := re.FindAllStringSubmatch(src, -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no request_logs reads found — query surface moved?", file)
		}
		for _, m := range matches {
			if m[1] != "_with_current_month" {
				t.Fatalf("%s reads the cold %q surface — recent-window reads must "+
					"use request_logs_with_current_month (2026-09-10 minimax-prod-v2 "+
					"incident doctrine)", file, m[0])
			}
		}
	}

	// 2026-09-12: same blindness, candidate_failure_logs family. The writer
	// feeds the hot table; the bare parent only holds promoted rows.
	reCfl := regexp.MustCompile(`(?i)FROM\s+candidate_failure_logs(\w*)`)
	for _, file := range []string{
		"candidate_failure_monitor.go",
		"daily_probe_audit.go",
		"model_probe.go",
	} {
		contents, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		matches := reCfl.FindAllStringSubmatch(string(contents), -1)
		if len(matches) == 0 {
			t.Fatalf("%s: no candidate_failure_logs reads found — query surface moved?", file)
		}
		for _, m := range matches {
			if m[1] != "_with_current_month" {
				t.Fatalf("%s reads the cold %q surface — recent-window reads must "+
					"use candidate_failure_logs_with_current_month (writer feeds the "+
					"hot table; bare parent only holds promoted rows)", file, m[0])
			}
		}
	}

	// Lock the conscious exclusion: the drift scanner must not silently move
	// to the view while system_fingerprint is missing from the hot table —
	// and equally must not grow a SECOND bare-parent reader unreviewed.
	drift, err := os.ReadFile("integrity_fingerprint_drift.go")
	if err != nil {
		t.Fatalf("read integrity_fingerprint_drift.go: %v", err)
	}
	if n := len(re.FindAllStringSubmatch(string(drift), -1)); n != 1 {
		t.Fatalf("integrity_fingerprint_drift.go bare-parent reader count changed (%d): "+
			"re-audit before touching — view lacks system_fingerprint until the "+
			"hot-table column drift is fixed", n)
	}

	// Lock the other conscious exclusion: opslog_trimmer deletes aged rows
	// from the bare parent on purpose — retention semantics (the UNION view
	// is not deletable, and hot rows must survive the trim window).
	trimmer, err := os.ReadFile("opslog_trimmer.go")
	if err != nil {
		t.Fatalf("read opslog_trimmer.go: %v", err)
	}
	if n := len(reCfl.FindAllStringSubmatch(string(trimmer), -1)); n != 2 {
		t.Fatalf("opslog_trimmer.go bare-parent candidate_failure_logs site count changed (%d): "+
			"retention deletes must stay on the deletable parent — re-audit before touching", n)
	}
}
