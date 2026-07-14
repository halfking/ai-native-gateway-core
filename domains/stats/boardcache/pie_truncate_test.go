package boardcache

import "testing"

func TestTruncatePieItemsAggregatesOther(t *testing.T) {
	items := make([]any, 0, 25)
	for i := 0; i < 25; i++ {
		items = append(items, map[string]any{
			"key":      string(rune('a' + i)),
			"requests": int64(25 - i),
			"tokens":   int64(100),
			"credits":  int64(0),
			"cost_usd": float64(0),
		})
	}
	out := truncatePieItems(items, 20)
	if len(out) != 21 {
		t.Fatalf("expected top 20 + other, got %d", len(out))
	}
	last, ok := out[20].(map[string]any)
	if !ok || last["key"] != boardPieOtherKey {
		t.Fatalf("expected __other__ bucket, got %#v", out[20])
	}
	if toInt64(last["requests"]) != 15 {
		t.Fatalf("other requests = %v, want 15", last["requests"])
	}
}
