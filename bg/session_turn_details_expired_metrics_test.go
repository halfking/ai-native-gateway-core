package bg

import (
	"math"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSessionTurnDetailsExpiredHotMetricIncludesUniqueConflicts(t *testing.T) {
	stats := SessionTurnDetailsExpiredHotStats{
		Total: 5, RequestDuplicate: 1, SessionTurnConflict: 1,
		IDDateConflict: 1, ReadyUnmoved: 2,
	}
	recordSessionTurnDetailsExpiredHotRows(stats)
	for status, want := range map[string]float64{
		"total": 5, "request_duplicate": 1,
		"session_turn_conflict": 1, "id_date_conflict": 1,
		"ready_unmoved": 2,
	} {
		if got := testutil.ToFloat64(sessionTurnDetailsExpiredHotRows.WithLabelValues(status)); got != want {
			t.Errorf("status=%s gauge=%v want %v", status, got, want)
		}
	}
	if got := testutil.ToFloat64(sessionTurnDetailsExpiredDuplicateHotRows); got != 1 {
		t.Errorf("legacy duplicate gauge=%v want 1", got)
	}
	if got := testutil.ToFloat64(sessionTurnDetailsExpiredHotRowsVerified); got != 1 {
		t.Errorf("verification gauge=%v want 1", got)
	}
	recordSessionTurnDetailsExpiredHotRowsFailure()
	if got := testutil.ToFloat64(sessionTurnDetailsExpiredHotRowsVerified); got != 0 {
		t.Errorf("failed verification gauge=%v want 0", got)
	}
	if got := testutil.ToFloat64(sessionTurnDetailsExpiredHotRows.WithLabelValues("total")); !math.IsNaN(got) {
		t.Errorf("failed verification total=%v want unknown", got)
	}
	if got := testutil.ToFloat64(sessionTurnDetailsExpiredDuplicateHotRows); !math.IsNaN(got) {
		t.Errorf("failed legacy duplicate gauge=%v want unknown", got)
	}
	recordSessionTurnDetailsExpiredHotRows(SessionTurnDetailsExpiredHotStats{})
	if got := testutil.ToFloat64(sessionTurnDetailsExpiredHotRows.WithLabelValues("total")); got != 0 {
		t.Errorf("verified empty total=%v want 0", got)
	}
}
