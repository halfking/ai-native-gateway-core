package admin

import (
	"os"
	"strings"
	"testing"
)

// TestTruncateBalanceError locks the rune-boundary behavior of the
// balance_error column cap (migration 721 comment: ≤500 chars). CJK vendor
// error pages must stay valid UTF-8 after truncation or PostgreSQL rejects
// the UPDATE.
func TestTruncateBalanceError(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		maxRS bool
	}{
		{"short ascii", "HTTP 502 from gateway", false},
		{"exactly 500 ascii", string(make([]byte, 500)), false}, // NULs, length check only
		{"cjk under cap", "余额不足：账户已欠费", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateBalanceError(tc.in)
			if len([]rune(got)) > 500 {
				t.Fatalf("truncateBalanceError returned %d runes, want ≤500", len([]rune(got)))
			}
		})
	}

	// CJK string longer than the cap: result must be exactly 500 runes and
	// valid as-produced runes (no broken multi-byte tail).
	longCJK := make([]rune, 700)
	for i := range longCJK {
		longCJK[i] = '余'
	}
	got := truncateBalanceError(string(longCJK))
	if got := len([]rune(got)); got != 500 {
		t.Fatalf("CJK truncation: got %d runes, want 500", got)
	}

	// 501 ASCII chars → exactly 500.
	longASCII := ""
	for i := 0; i < 501; i++ {
		longASCII += "x"
	}
	if got := truncateBalanceError(longASCII); len(got) != 500 {
		t.Fatalf("ASCII truncation: got %d bytes, want 500", len(got))
	}
}

// TestUpdateCredentialStampsManualOnlyOnValueChange (R42, 2026-09-18 audit)
// pins the conditional manual stamping in source: the drawer form always
// carries balance_usd, so an unconditional balance_source='manual' SET made
// any unrelated edit (tags/notes/label) silently suspend the automatic
// balance probes for 24h. The stamp (source/checked_at/error) must be
// guarded by a balance_usd IS DISTINCT FROM comparison against the old row
// (the local `valueChanged` fragment in updateCredential).
func TestUpdateCredentialStampsManualOnlyOnValueChange(t *testing.T) {
	src, err := os.ReadFile("provider_credential.go")
	if err != nil {
		t.Fatalf("read provider_credential.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, `valueChanged := "balance_usd IS DISTINCT FROM " + balArg`) {
		t.Fatalf("updateCredential must compare the OLD balance_usd against the incoming value before stamping manual provenance")
	}
	for _, marker := range []string{
		`"balance_source = CASE WHEN "+valueChanged+" THEN 'manual' ELSE balance_source END"`,
		`"balance_last_checked_at = CASE WHEN "+valueChanged+" THEN NOW() ELSE balance_last_checked_at END"`,
		`"balance_error = CASE WHEN "+valueChanged+" THEN NULL ELSE balance_error END"`,
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("updateCredential missing conditional stamp fragment %s — manual provenance must only be written when balance_usd actually changes", marker)
		}
	}
}

// TestRefreshBalanceFailureDoesNotBumpCheckedAt (R43, 2026-09-18 audit) pins
// the ⟳ failure-path semantics in source: the manual protection window is
// `balance_source='manual' AND balance_last_checked_at > NOW()-24h`, so a
// failure UPDATE that bumps checked_at (the pre-R43 behavior) extended the
// window on every click and suspended automatic probing on manual rows for
// as long as the vendor endpoint stayed broken. Same call the bg probe paths
// made in R42 (balanceProbeFailStamp: error only, checked_at untouched).
func TestRefreshBalanceFailureDoesNotBumpCheckedAt(t *testing.T) {
	source, err := os.ReadFile("provider_credential_balance.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	fnStart := strings.Index(text, "func (h *Handler) handleRefreshCredentialBalance(")
	if fnStart < 0 {
		// fall back to a looser anchor if the handler signature drifts
		fnStart = strings.Index(text, "balance probe failed (network/HTTP/parse)")
		if fnStart < 0 {
			t.Fatal("refresh-balance failure path not found in source")
		}
	}
	body := text[fnStart:]
	failIdx := strings.Index(body, "SET balance_error = $1")
	if failIdx < 0 {
		t.Fatal("refresh-balance failure UPDATE no longer writes balance_error — failure evidence lost")
	}
	tail := body[failIdx:min(len(body), failIdx+200)]
	if strings.Contains(tail, "balance_last_checked_at") {
		t.Fatal("refresh-balance failure UPDATE bumps balance_last_checked_at — " +
			"this extends the manual protection window on every failed ⟳ click (R43 regression)")
	}
}
