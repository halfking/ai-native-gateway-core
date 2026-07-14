package boardcache

import "testing"

func TestScopesForEntry(t *testing.T) {
	scopes := ScopesForEntry("acme")
	if len(scopes) != 2 {
		t.Fatalf("expected 2 scopes, got %d", len(scopes))
	}
	if scopes[0] != ScopeGlobal {
		t.Fatalf("first scope should be global, got %s", scopes[0])
	}
	if scopes[1] != ScopeTenant("acme") {
		t.Fatalf("second scope should be tenant:acme, got %s", scopes[1])
	}
}

func TestTenantFilter(t *testing.T) {
	if TenantFilter(ScopeGlobal) != "" {
		t.Fatal("global should map to empty tenant filter")
	}
	if TenantFilter(ScopeTenant("x")) != "x" {
		t.Fatal("tenant scope filter mismatch")
	}
}

func TestApplySummaryDelta(t *testing.T) {
	board := map[string]any{
		"summary": map[string]any{
			"total_requests": int64(10),
			"success_rate":   0.8,
			"avg_latency_ms": float64(100),
		},
	}
	applySummaryDelta(board, summaryCounters{
		Requests:     2,
		Success:      2,
		LatencyMsSum: 200,
	})
	s := board["summary"].(map[string]any)
	if s["total_requests"].(int64) != 12 {
		t.Fatalf("requests=%v", s["total_requests"])
	}
}

func TestParseDimField(t *testing.T) {
	f := dimField("model", "gpt-4o", "requests")
	dt, dk, m, ok := parseDimField(f)
	if !ok || dt != "model" || dk != "gpt-4o" || m != "requests" {
		t.Fatalf("parse failed: %s %s %s", dt, dk, m)
	}
}
