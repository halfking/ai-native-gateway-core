package admin

import (
	"net/http/httptest"
	"testing"
)

func TestOpsOverviewCache(t *testing.T) {
	h := &Handler{}
	cache := h.ensureOpsOverviewCache()
	if cache == nil {
		t.Fatal("expected cache")
	}
	if _, ok := cache.get(); ok {
		t.Fatal("expected empty cache")
	}
	cache.set(map[string]any{"center_stats": map[string]int{"online_instances": 1}})
	got, ok := cache.get()
	if !ok {
		t.Fatal("expected cached payload")
	}
	stats, _ := got["center_stats"].(map[string]int)
	if stats["online_instances"] != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestIncludeBoardOperationalDefault(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/admin/dashboard/board", nil)
	if !includeBoardOperational(req) {
		t.Fatal("expected operational included by default")
	}
	req = httptest.NewRequest("GET", "/api/admin/dashboard/board?include_operational=0", nil)
	if includeBoardOperational(req) {
		t.Fatal("expected operational excluded when include_operational=0")
	}
}
