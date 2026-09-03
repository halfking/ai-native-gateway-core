package admin

import "testing"

func TestApplyTurnsV2ShadowPerTurn(t *testing.T) {
	payload := map[string]any{"turns": []any{
		map[string]any{"request_id": "r1"},
		map[string]any{"request_id": "r2"},
	}}
	applyTurnsV2Shadow(payload, []byte(`{"turns":[{"request_id":"r1","turn_no":7,"digest":{"ok":true}}]}`), turnsV2Shadow{Status: "match", StatusCode: 200})
	turns := payload["turns"].([]any)
	first := turns[0].(map[string]any)["v2_shadow"].(SessionTurnV2Shadow)
	if first.TurnNo != 7 || !first.DigestAvailable {
		t.Fatalf("unexpected per-turn shadow: %+v", first)
	}
	if turns[1].(map[string]any)["v2_shadow"] != nil {
		t.Fatalf("missing V2 turn should have nil shadow: %+v", turns[1])
	}
}

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
