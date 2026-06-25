package admin

import (
	"testing"
)

// helper to make a *int64 from a literal
func i64ptr(v int64) *int64 { return &v }

func TestDetectBackgroundTaskIDMismatch_Match(t *testing.T) {
	req := map[string]any{
		"provider_id":   float64(14),
		"credential_id": float64(6),
	}
	topPID := i64ptr(int64(14))
	topCID := i64ptr(int64(6))
	if m, ok := detectBackgroundTaskIDMismatch(40, "health_check", topPID, topCID, req); ok {
		t.Fatalf("expected no mismatch, got %+v", m)
	}
}

func TestDetectBackgroundTaskIDMismatch_ProviderMismatch(t *testing.T) {
	// Reproduces the user-reported scenario: top-level says provider=32,
	// request_json says provider=32 but credential_id differs.
	req := map[string]any{
		"provider_id":   float64(32),
		"credential_id": float64(2),
	}
	topPID := i64ptr(int64(32))
	topCID := i64ptr(int64(7))
	m, ok := detectBackgroundTaskIDMismatch(40, "health_check", topPID, topCID, req)
	if !ok {
		t.Fatalf("expected a mismatch to be detected")
	}
	if m["top_credential_id"].(*int64) == nil || *m["top_credential_id"].(*int64) != 7 {
		t.Errorf("top_credential_id not echoed correctly: %+v", m["top_credential_id"])
	}
	if m["request_credential_id"].(*int64) == nil || *m["request_credential_id"].(*int64) != 2 {
		t.Errorf("request_credential_id not echoed correctly: %+v", m["request_credential_id"])
	}
	if m["task_id"].(int64) != 40 || m["task_type"].(string) != "health_check" {
		t.Errorf("task metadata missing: %+v", m)
	}
}

func TestDetectBackgroundTaskIDMismatch_BothMismatch(t *testing.T) {
	req := map[string]any{
		"provider_id":   float64(14),
		"credential_id": float64(6),
	}
	topPID := i64ptr(int64(32))
	topCID := i64ptr(int64(7))
	if _, ok := detectBackgroundTaskIDMismatch(99, "health_check", topPID, topCID, req); !ok {
		t.Fatalf("expected mismatch when both IDs differ")
	}
}

func TestDetectBackgroundTaskIDMismatch_MissingKeys(t *testing.T) {
	// request_json doesn't have provider_id/credential_id at all (e.g. a
	// diagnose row) — we don't fabricate a mismatch, we just pass.
	req := map[string]any{"source": "auto_on_create"}
	topPID := i64ptr(int64(14))
	topCID := i64ptr(int64(6))
	if _, ok := detectBackgroundTaskIDMismatch(1, "health_check", topPID, topCID, req); ok {
		t.Fatalf("expected no mismatch when keys are missing")
	}
}

func TestDetectBackgroundTaskIDMismatch_NilTopButPresentReq(t *testing.T) {
	// A diagnose task has top-level credential_id = NULL. If request_json
	// somehow has a credential_id we should NOT flag that as a mismatch
	// (NULL is "unknown", not "definitely different").
	req := map[string]any{"provider_id": float64(14), "credential_id": float64(99)}
	if _, ok := detectBackgroundTaskIDMismatch(1, "diagnose", i64ptr(int64(14)), nil, req); ok {
		t.Fatalf("expected no mismatch when top-level credential_id is NULL")
	}
}

func TestDetectBackgroundTaskIDMismatch_Int64Typed(t *testing.T) {
	// Some JSON libraries decode numbers into int64 instead of float64.
	req := map[string]any{
		"provider_id":   int64(14),
		"credential_id": int64(6),
	}
	topPID := i64ptr(int64(14))
	topCID := i64ptr(int64(6))
	if _, ok := detectBackgroundTaskIDMismatch(40, "health_check", topPID, topCID, req); ok {
		t.Fatalf("expected no mismatch for int64-typed numbers")
	}
}

func TestDetectBackgroundTaskIDMismatch_IntTyped(t *testing.T) {
	req := map[string]any{
		"provider_id":   int(14),
		"credential_id": int(6),
	}
	topPID := i64ptr(int64(14))
	topCID := i64ptr(int64(7))
	if _, ok := detectBackgroundTaskIDMismatch(40, "health_check", topPID, topCID, req); !ok {
		t.Fatalf("expected mismatch when types are int and credential_id differs")
	}
}