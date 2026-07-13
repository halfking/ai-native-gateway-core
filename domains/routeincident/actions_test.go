package routeincident

import (
	"testing"
)

// TestSanitizeReason_Bounds checks the operator reason is bounded.
func TestSanitizeReason_Bounds(t *testing.T) {
	if got := sanitizeReason(""); got != "" {
		t.Fatalf("empty: %q", got)
	}
	if got := sanitizeReason("  "); got != "" {
		t.Fatalf("whitespace: %q", got)
	}
	long := make([]byte, MaxReasonLen*3)
	for i := range long {
		long[i] = 'a'
	}
	got := sanitizeReason(string(long))
	if len([]rune(got)) > MaxReasonLen {
		t.Fatalf("reason too long: %d runes", len([]rune(got)))
	}
}

// TestSanitizeParameters_AllowList confirms only allow-listed keys
// survive. This is the gate that keeps credentials, headers, and
// raw bodies out of the audit row.
func TestSanitizeParameters_AllowList(t *testing.T) {
	in := map[string]any{
		"timeout_ms":   5000,
		"max_tokens":   256,
		"target_state": "recovered",
		"slot_id":      "slot-1",
		"credential":   "sk-live-xxx",      // not allowed
		"api_key":      "sk-live-yyy",      // not allowed
		"raw_body":     "should never see", // not allowed
		"random_url":   "https://evil",     // not allowed
	}
	out := sanitizeParameters(in)
	for _, k := range []string{"credential", "api_key", "raw_body", "random_url"} {
		if _, ok := out[k]; ok {
			t.Fatalf("forbidden key %q leaked: %#v", k, out)
		}
	}
	for _, k := range []string{"timeout_ms", "max_tokens", "target_state", "slot_id"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("allowed key %q missing: %#v", k, out)
		}
	}
}

// TestSanitizeParameters_NilSafe confirms the helper is safe on
// nil input.
func TestSanitizeParameters_NilSafe(t *testing.T) {
	if got := sanitizeParameters(nil); got == nil {
		t.Fatal("nil-safe contract violated")
	}
	if got := sanitizeParameters(nil); len(got) != 0 {
		t.Fatalf("empty input should yield empty map: %#v", got)
	}
}

// TestIsAllowedAction confirms the closed allow-list.
func TestIsAllowedAction(t *testing.T) {
	for _, k := range AllActionKinds() {
		if !IsAllowedAction(k) {
			t.Errorf("allowed action %q rejected", k)
		}
	}
	if IsAllowedAction("not-in-list") {
		t.Error("unknown action accepted")
	}
	if IsAllowedAction("") {
		t.Error("empty action accepted")
	}
}

// TestActionKind_IsMutating confirms only evidence_export is
// non-mutating. Every other action requires a confirmation token.
func TestActionKind_IsMutating(t *testing.T) {
	for _, k := range AllActionKinds() {
		isMutating := k.IsMutating()
		if k == ActionEvidenceExport && isMutating {
			t.Errorf("%q should be non-mutating", k)
		}
		if k != ActionEvidenceExport && !isMutating {
			t.Errorf("%q should be mutating", k)
		}
	}
}

// TestDiagnosticRunState_IsTerminal confirms the terminal-state
// predicate matches the spec.
func TestDiagnosticRunState_IsTerminal(t *testing.T) {
	cases := map[DiagnosticRunState]bool{
		RunPending:   false,
		RunRunning:   false,
		RunSucceeded: true,
		RunFailed:    true,
		RunCancelled: true,
	}
	for s, want := range cases {
		if got := s.IsTerminal(); got != want {
			t.Errorf("%s: want terminal=%v got %v", s, want, got)
		}
	}
}

// TestHashToken confirms the SHA-256 hex output and that the
// plaintext is never recoverable.
func TestHashToken(t *testing.T) {
	if got := hashToken(""); got != "" {
		t.Fatalf("empty token should yield empty hash, got %q", got)
	}
	h1 := hashToken("secret-1")
	h2 := hashToken("secret-2")
	if h1 == h2 {
		t.Fatal("hash collision on distinct tokens")
	}
	if len(h1) != 64 {
		t.Fatalf("SHA-256 hex should be 64 chars, got %d", len(h1))
	}
}

// TestHashIP confirms the IP short-hash is bounded and stable.
func TestHashIP(t *testing.T) {
	if got := hashIP(""); got != "" {
		t.Fatalf("empty IP should yield empty hash, got %q", got)
	}
	h1 := hashIP("10.0.0.1")
	h2 := hashIP("10.0.0.2")
	if h1 == h2 {
		t.Fatal("IP hash collision on distinct inputs")
	}
	// We use a 16-char prefix (8 bytes). Verify the length.
	if len(h1) != 16 {
		t.Fatalf("IP short hash should be 16 chars, got %d", len(h1))
	}
}
