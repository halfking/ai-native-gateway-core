package bg

import (
	"testing"
	"time"
)

func TestFailureLogIsStale(t *testing.T) {
	now := time.Date(2026, time.July, 14, 1, 0, 0, 0, time.UTC)
	fresh := now.Add(-5 * time.Minute)
	old := now.Add(-time.Hour)

	tests := []struct {
		name        string
		lastFailure *time.Time
		lastRequest *time.Time
		want        bool
	}{
		{name: "quiet gateway does not alert", lastFailure: &old, lastRequest: nil, want: false},
		{name: "recent failure log is healthy", lastFailure: &fresh, lastRequest: &fresh, want: false},
		{name: "old failure log is stale", lastFailure: &old, lastRequest: &fresh, want: true},
		{name: "missing failure log is stale during traffic", lastFailure: nil, lastRequest: &fresh, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := failureLogIsStale(tt.lastFailure, tt.lastRequest, now, 30*time.Minute); got != tt.want {
				t.Fatalf("failureLogIsStale() = %v, want %v", got, tt.want)
			}
		})
	}
}
