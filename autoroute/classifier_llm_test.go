package autoroute

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestLLMFallbackClassifier_RecordsDisabledOutcome guards the 2026-09-15
// audit fix: production without LLMGatewayAutoLLMEndpoint gets a bare
// DisabledCaller (no InstrumentedCaller wrapper), so the "disabled" outcome
// promised by the llm_gateway_llm_classifier_total HELP would never be
// emitted without the explicit record in Classify.
func TestLLMFallbackClassifier_RecordsDisabledOutcome(t *testing.T) {
	var outcomes []string
	oldSink := RecordLLMMetricCall
	RecordLLMMetricCall = func(outcome string, _ time.Duration) {
		outcomes = append(outcomes, outcome)
	}
	defer func() { RecordLLMMetricCall = oldSink }()

	c := NewLLMFallbackClassifierWithCaller(DisabledCaller{})
	_, err := c.Classify(context.Background(), ClassificationSignals{})
	if !errors.Is(err, ErrLLMDisabled) {
		t.Fatalf("err = %v, want ErrLLMDisabled", err)
	}
	if len(outcomes) != 1 || outcomes[0] != "disabled" {
		t.Fatalf("outcomes = %v, want exactly [disabled]", outcomes)
	}
}
