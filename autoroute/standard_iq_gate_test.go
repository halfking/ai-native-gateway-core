package autoroute

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/modeliqdata"
)

// TestStandardIQMatch verifies the RT-1 hard-gate predicate: a candidate
// whose standard IQ (Artificial Analysis Intelligence Index, 0-100 — not a
// human IQ scale) is known and below the threshold must be excluded, while
// unknown models stay routable (fail-open, so a stale reference table can
// never empty the candidate pool).
func TestStandardIQMatch(t *testing.T) {
	const (
		highIQModel = "claude-opus-4-8"  // 61.4 in the embedded reference
		lowIQModel  = "gpt-3.5-turbo"    // 3.0 in the embedded reference
		unknownModel = "totally-unknown-model-x"
	)
	highIQ, highFound, _ := modeliqdata.LookupStandardIQ(highIQModel)
	lowIQ, lowFound, _ := modeliqdata.LookupStandardIQ(lowIQModel)
	if !highFound || !lowFound || highIQ <= lowIQ {
		t.Fatalf("reference table sanity failed: %s=%.1f(found=%v) %s=%.1f(found=%v)",
			highIQModel, highIQ, highFound, lowIQModel, lowIQ, lowFound)
	}

	tests := []struct {
		name       string
		model      string
		minIQ      float64
		wantPass   bool
		wantIQ     float64
		wantFound  bool
	}{
		{
			name:      "known model above threshold passes",
			model:     highIQModel,
			minIQ:     50,
			wantPass:  true,
			wantIQ:    highIQ,
			wantFound: true,
		},
		{
			name:      "known model below threshold is excluded",
			model:     lowIQModel,
			minIQ:     50,
			wantPass:  false,
			wantIQ:    lowIQ,
			wantFound: true,
		},
		{
			name:      "threshold equal to value passes (>= semantics)",
			model:     highIQModel,
			minIQ:     highIQ,
			wantPass:  true,
			wantIQ:    highIQ,
			wantFound: true,
		},
		{
			name:      "unknown model fails open",
			model:     unknownModel,
			minIQ:     50,
			wantPass:  true,
			wantIQ:    0,
			wantFound: false,
		},
		{
			name:      "zero threshold disables the gate",
			model:     lowIQModel,
			minIQ:     0,
			wantPass:  true,
			wantIQ:    lowIQ,
			wantFound: true,
		},
		{
			name:      "empty model name fails open",
			model:     "",
			minIQ:     50,
			wantPass:  true,
			wantIQ:    0,
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			passes, iq, found := StandardIQMatch(tt.model, tt.minIQ)
			if passes != tt.wantPass {
				t.Errorf("StandardIQMatch(%q, %.1f) passes = %v, want %v", tt.model, tt.minIQ, passes, tt.wantPass)
			}
			if found != tt.wantFound {
				t.Errorf("StandardIQMatch(%q, %.1f) found = %v, want %v", tt.model, tt.minIQ, found, tt.wantFound)
			}
			if found && iq != tt.wantIQ {
				t.Errorf("StandardIQMatch(%q, %.1f) iq = %.1f, want %.1f", tt.model, tt.minIQ, iq, tt.wantIQ)
			}
		})
	}
}
