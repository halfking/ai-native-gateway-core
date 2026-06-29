package streaming

import (
	"testing"

	"github.com/kaixuan/llm-gateway-go/autoroute"
)

func TestDecisionToWire_IncludesEnabledFeatures(t *testing.T) {
	dec := &autoroute.Decision{
		TaskType:           autoroute.TaskCode,
		Confidence:         0.91,
		Profile:            autoroute.ProfileSmart,
		Classifier:         "heuristic_v2",
		Reason:             "matched code intent",
		ChosenModel:        "gpt-4.1",
		ChosenRawModel:     "gpt-4.1-2026-06",
		ChosenCredentialID: 123,
		EnabledFeatures:    []string{"simplified_scoring", "hot_top3_pool", "fallback_48h"},
		CacheReused:        true,
		FallbackUsed:       false,
	}

	wire := decisionToWire(dec)
	if wire == nil {
		t.Fatal("expected non-nil wire decision")
	}
	if len(wire.EnabledFeatures) != 3 {
		t.Fatalf("expected 3 enabled features, got %d", len(wire.EnabledFeatures))
	}
	if wire.EnabledFeatures[0] != "simplified_scoring" || wire.EnabledFeatures[1] != "hot_top3_pool" || wire.EnabledFeatures[2] != "fallback_48h" {
		t.Fatalf("unexpected enabled features: %v", wire.EnabledFeatures)
	}
	if wire.ChosenCredID != 123 {
		t.Fatalf("expected chosen credential id 123, got %d", wire.ChosenCredID)
	}
	if !wire.CacheReused {
		t.Fatal("expected CacheReused to propagate to wire")
	}
	if wire.FallbackUsed {
		t.Fatal("expected FallbackUsed to be false in wire")
	}
}
