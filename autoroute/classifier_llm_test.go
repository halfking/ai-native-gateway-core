package autoroute

import (
	"context"
	"errors"
	"strings"
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

func TestBuildClassificationPromptIncludesCurrentTaskContractAndSignals(t *testing.T) {
	prompt := buildClassificationPrompt(ClassificationSignals{
		SystemPrompt:    "You are a release-planning assistant.",
		LastUserPrompt:  "Create a rollout roadmap from the tool findings.",
		ToolCount:       4,
		HasToolResults:  true,
		HasImages:       true,
		HasCodeBlock:    true,
		EstimatedTokens: 65_000,
	})

	for _, want := range []string{
		"planning", "code_audit", "intent_classification", "function_call",
		"System prompt:", "release-planning assistant", "tool_count=4",
		"has_tool_results=true", "has_images=true", "has_code_block=true",
		"estimated_tokens=65000", "Create a rollout roadmap",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildClassificationPromptBoundsText(t *testing.T) {
	prompt := buildClassificationPrompt(ClassificationSignals{
		SystemPrompt:   strings.Repeat("s", llmFallbackSystemPromptLimit+1),
		LastUserPrompt: strings.Repeat("u", llmFallbackUserPromptLimit+1),
	})

	if !strings.Contains(prompt, "...[truncated]") {
		t.Fatalf("prompt did not mark truncated content")
	}
	if strings.Count(prompt, "...[truncated]") != 2 {
		t.Fatalf("truncated marker count = %d, want 2", strings.Count(prompt, "...[truncated]"))
	}
}

func TestNormaliseLLMTaskTypePlanning(t *testing.T) {
	for _, raw := range []string{"planning", " Planning. ", "task: planning"} {
		got, ok := normaliseLLMTaskType(raw)
		if !ok || got != TaskPlanning {
			t.Errorf("normaliseLLMTaskType(%q) = (%q, %t), want (%q, true)", raw, got, ok, TaskPlanning)
		}
	}
}
