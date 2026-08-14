package autoroute

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDecideV2_StandardIQGate_FilterReasonsInMetadata pins the RT-1 audit
// contract end-to-end: when the gate excludes a candidate, the decision
// metadata (Decision.FilterReasons, surfaced in request_logs.auto_decision)
// records why; when the gate is off, FilterReasons stays nil.
func TestDecideV2_StandardIQGate_FilterReasonsInMetadata(t *testing.T) {
	build := func() (*Decider, *Index) {
		idx := &Index{entries: gateTestCandidates(), lastRefresh: time.Now()}
		d := NewDecider(&v2TestClassifier{task: TaskChat}, nil, idx, NewMemoryProfileStore())
		return d, idx
	}

	t.Run("gate off leaves FilterReasons nil", func(t *testing.T) {
		old := GetFeatureFlags()
		SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
		defer SetGlobalFeatureFlagsForTest(old)

		d, _ := build()
		dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 0, "", "", "")
		if err != nil {
			t.Fatalf("DecideV2 err: %v", err)
		}
		if dec.FilterReasons != nil {
			t.Errorf("FilterReasons must be nil when gate is off, got %v", dec.FilterReasons)
		}
	})

	t.Run("gate on records exclusion reason", func(t *testing.T) {
		old := GetFeatureFlags()
		SetGlobalFeatureFlagsForTest(&FeatureFlags{
			UseChannelQualityRouting: true,
			UseStandardIQGate:        true,
			MinStandardIQ:            50,
		})
		defer SetGlobalFeatureFlagsForTest(old)

		d, _ := build()
		dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 0, "", "", "")
		if err != nil {
			t.Fatalf("DecideV2 err: %v", err)
		}
		if dec.ChosenModel != "claude-opus-4-8" {
			t.Errorf("ChosenModel = %q, want claude-opus-4-8 (low-IQ model must not win)", dec.ChosenModel)
		}
		if len(dec.FilterReasons) != 1 {
			t.Fatalf("FilterReasons: want 1 entry, got %v", dec.FilterReasons)
		}
		if !strings.Contains(dec.FilterReasons[0], "standard_iq_below_min") ||
			!strings.Contains(dec.FilterReasons[0], "gpt-3.5-turbo") {
			t.Errorf("FilterReasons[0] should name the gate and the excluded model, got %q", dec.FilterReasons[0])
		}
	})
}
