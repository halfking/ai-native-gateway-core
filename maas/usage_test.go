package maas

import "testing"

func TestClampUsageDays(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 1},
		{-5, 1},
		{7, 7},
		{90, 90},
		{120, 90},
	}
	for _, tc := range tests {
		if got := ClampUsageDays(tc.in); got != tc.want {
			t.Fatalf("ClampUsageDays(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestClampUsageLimit(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 10},
		{-1, 10},
		{10, 10},
		{25, 25},
		{100, 50},
	}
	for _, tc := range tests {
		if got := ClampUsageLimit(tc.in); got != tc.want {
			t.Fatalf("ClampUsageLimit(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestQueryUsageSummary_requiresTenant(t *testing.T) {
	svc := NewService(nil)
	_, err := svc.QueryUsageSummary(t.Context(), "", 7, 10)
	if err == nil {
		t.Fatal("expected error for empty tenant")
	}
}
