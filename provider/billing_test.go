package provider

import "testing"

func TestBillingRound(t *testing.T) {
	tests := []struct {
		mode string
		want int
	}{
		{"free", 1},
		{"token_plan", 1},
		{"code_plan", 1},
		{"agent_plan", 1},
		{"monthly", 1},
		{"token", 2},
		{"per_token", 2},
		{"", 2},
	}
	for _, tc := range tests {
		if got := BillingRound(tc.mode); got != tc.want {
			t.Fatalf("BillingRound(%q) = %d, want %d", tc.mode, got, tc.want)
		}
	}
}
