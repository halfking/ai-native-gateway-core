package streaming

import (
	"encoding/json"
	"testing"
)

// 2026-09-14 audit O2 observability: auto-route successes must project the
// decision wire into routing_decision_log.decision_trace so fallback_used /
// task_type are observable via SQL (production previously stored only `{}`).
func TestAutoDecisionTrace(t *testing.T) {
	wire := &autoRouteDecision{
		TaskType:       "planning",
		Confidence:     0.82,
		Classifier:     "heuristic_v2",
		ChosenModel:    "deepseek-v4-flash",
		ChosenRawModel: "deepseek-v4-flash",
		ChosenCredID:   11,
		FallbackUsed:   true,
	}
	wireJSON, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal wire: %v", err)
	}

	trace := autoDecisionTrace(wireJSON)
	if trace == nil {
		t.Fatalf("autoDecisionTrace returned nil for valid wire")
	}
	var got map[string]any
	if err := json.Unmarshal(trace, &got); err != nil {
		t.Fatalf("trace is not valid JSON: %v", err)
	}
	if got["task_type"] != "planning" {
		t.Fatalf("task_type = %v, want planning", got["task_type"])
	}
	if got["fallback_used"] != true {
		t.Fatalf("fallback_used = %v, want true", got["fallback_used"])
	}
	if got["source"] != "auto_route" {
		t.Fatalf("source = %v, want auto_route", got["source"])
	}
	if got["chosen_credential_id"] != float64(11) {
		t.Fatalf("chosen_credential_id = %v, want 11", got["chosen_credential_id"])
	}

	if autoDecisionTrace([]byte("not-json")) != nil {
		t.Fatalf("autoDecisionTrace should return nil for unparsable wire")
	}
}
