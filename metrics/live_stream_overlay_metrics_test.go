package metrics

import "testing"

func TestLiveStreamTileOverlayMetrics(t *testing.T) {
	for _, status := range []string{"success", "failure", "locked", "other"} {
		outcome := normalizeLiveStreamOverlayOutcome(status)
		before := readCounterVec(t, LiveStreamTileOverlayDBLookupVec(outcome))
		RecordLiveStreamTileOverlayDBLookup(status)
		if got := readCounterVec(t, LiveStreamTileOverlayDBLookupVec(outcome)); got != before+1 {
			t.Fatalf("%s count = %v, want %v", outcome, got, before+1)
		}
	}
}
