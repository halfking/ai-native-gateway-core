package admin

import "testing"

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
