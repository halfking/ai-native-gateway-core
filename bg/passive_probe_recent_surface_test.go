package bg

import (
	"os"
	"regexp"
	"testing"
)

// TestPassiveProbeRecentWindowReadsUseCurrentMonthSurface locks the
// 2026-09-10 minimax-prod-v2 incident fix: every recent-window read in the
// passive probe listener must go through request_logs_with_current_month.
//
// Production evidence (154): the bare request_logs parent only holds cold
// rows — its max(ts) was a full day stale — while request_logs_hot held 472
// successes for (cred 21, MiniMax-M3) in the review window. The listener's
// success reset, window_total_count refresh, and reviewResolution success
// check all read the bare parent, so "consecutive transient errors with no
// success" was structurally guaranteed and the review marked a healthy
// credential unreachable right after the operator force-enabled it.
func TestPassiveProbeRecentWindowReadsUseCurrentMonthSurface(t *testing.T) {
	contents, err := os.ReadFile("passive_probe_listener.go")
	if err != nil {
		t.Fatalf("read passive_probe_listener.go: %v", err)
	}
	src := string(contents)

	// Any "FROM request_logs<suffix>" read must target the current-month
	// surface. A bare "FROM request_logs" (suffix empty) is the cold parent
	// and is blind to request_logs_hot.
	re := regexp.MustCompile(`(?i)FROM\s+request_logs(\w*)`)
	matches := re.FindAllStringSubmatch(src, -1)
	if len(matches) == 0 {
		t.Fatalf("no request_logs reads found in passive_probe_listener.go — query surface moved?")
	}
	for _, m := range matches {
		if m[1] != "_with_current_month" {
			t.Fatalf("passive probe listener reads the cold %q surface — recent-window reads "+
				"must use request_logs_with_current_month (2026-09-10 minimax-prod-v2 incident)", m[0])
		}
	}
	if len(matches) < 4 {
		t.Fatalf("expected at least 4 current-month reads (poll errors, success reset, window refresh, review success check); got %d", len(matches))
	}

	// The review success check must also compare model names
	// case-insensitively: the stored raw_model_name comes from
	// COALESCE(outbound, client) and can differ in case from the logged
	// columns ('MiniMax-M3' vs 'minimax-m3').
	want := "LOWER(COALESCE(outbound_model, client_model)) = LOWER($2)"
	found := false
	for _, line := range regexp.MustCompile(`(?i)LOWER\(COALESCE\(outbound_model, client_model\)\)\s*=\s*LOWER\(\$2\)`).FindAllString(src, -1) {
		_ = line
		found = true
	}
	if !found {
		t.Fatalf("reviewResolution success check must match model names case-insensitively (%q)", want)
	}
}
