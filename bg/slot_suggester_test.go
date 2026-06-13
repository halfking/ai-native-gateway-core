package bg

import "testing"

func TestSlotSuggester_CalculateSuggestedLimit(t *testing.T) {
	s := &SlotSuggester{}
	tests := []struct {
		name    string
		peak    int
		p95     int
		current int
		wantMin int
		wantMax int
	}{
		// peak/current = 30% is below 80%, but the suggester doesn't
		// enforce the 80% threshold itself — that's the caller's job.
		// The function always returns >= current+1.
		{"always-increases-low-peak", 30, 25, 100, 101, 200},
		{"increase-modest", 90, 80, 100, 105, 200},
		{"increase-large", 200, 180, 100, 200, 500},
		{"respect-global-cap", 1000, 1000, 100, 500, 500},
		{"min-increment-1", 80, 80, 100, 101, 200},
		{"very-low-current", 12, 11, 10, 11, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := s.calculateSuggestedLimit(tt.peak, tt.p95, tt.current)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("calculateSuggestedLimit(%d, %d, %d) = %d, want [%d, %d]",
					tt.peak, tt.p95, tt.current, got, tt.wantMin, tt.wantMax)
			}
			if got <= tt.current {
				t.Errorf("must always increase: got %d, current %d", got, tt.current)
			}
		})
	}
}
