package admin

import "testing"

func TestBuildTurnsV2Shadow(t *testing.T) {
	t.Run("match", func(t *testing.T) {
		got := buildTurnsV2Shadow([]byte(`{"turns":[{"request_id":"r1"}]}`), []byte(`{"turns":[{"request_id":"r1"}]}`), 200)
		if got.Status != "match" || got.TreeCount != 1 || got.V2Count != 1 {
			t.Fatalf("unexpected shadow: %+v", got)
		}
	})
	t.Run("mismatch", func(t *testing.T) {
		got := buildTurnsV2Shadow([]byte(`{"turns":[{"request_id":"r1"}]}`), []byte(`{"turns":[{"request_id":"r2"}]}`), 200)
		if got.Status != "mismatch" || len(got.MissingInV2) != 1 || len(got.ExtraInV2) != 1 {
			t.Fatalf("unexpected shadow: %+v", got)
		}
	})
	t.Run("unavailable", func(t *testing.T) {
		got := buildTurnsV2Shadow([]byte(`{"turns":[]}`), []byte(`{"error":"down"}`), 503)
		if got.Status != "unavailable" || got.StatusCode != 503 {
			t.Fatalf("unexpected shadow: %+v", got)
		}
	})
}
