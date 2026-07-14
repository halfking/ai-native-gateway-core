package admin

import "testing"

func TestResolveProviderPieLabels(t *testing.T) {
	h := &Handler{}
	items := []boardPieItem{
		{Key: "14", Requests: 10},
		{Key: "0", Requests: 2},
		{Key: "not-id", Requests: 1},
	}

	// nil db → passthrough except unknown id handling without lookup
	out := h.resolveProviderPieLabels(t.Context(), items)
	if len(out) != 3 {
		t.Fatalf("expected 3 items, got %d", len(out))
	}
	if out[1].Key != "Unknown" {
		t.Fatalf("expected provider 0 -> Unknown, got %q", out[1].Key)
	}
	if out[0].Key != "14" {
		t.Fatalf("expected unchanged id without db, got %q", out[0].Key)
	}
}
