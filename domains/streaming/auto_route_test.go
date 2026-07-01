package streaming

import (
	"encoding/json"
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

// TestAutoDecision_RoundTrip verifies the full Decision → wire → JSON →
// unmarshal pipeline. This is the contract between autoroute.Decision,
// the X-Gw-Auto-Decision header, and request_logs.auto_decision JSONB:
// every field must survive serialisation intact.
func TestAutoDecision_RoundTrip(t *testing.T) {
	dec := &autoroute.Decision{
		TaskType:           autoroute.TaskCode,
		Confidence:         0.87,
		Profile:            autoroute.ProfileSpeedFirst,
		Classifier:         "heuristic_v2",
		Reason:             "code intent, speed-first",
		ChosenModel:        "claude-sonnet",
		ChosenRawModel:     "claude-sonnet-4-2026",
		ChosenCredentialID: 777,
		EnabledFeatures:    []string{"channel_quality_routing", "cache_revalidation"},
		CacheReused:        false,
		FallbackUsed:       true,
		CandidatesTopN: []autoroute.ScoredCandidate{
			{Candidate: autoroute.Candidate{CanonicalName: "claude-sonnet", CredentialID: 777},
				Breakdown: autoroute.ScoringBreakdown{Composite: 85, MatchScore: 70, PriceScore: 60, ChannelQuality: 55}},
			{Candidate: autoroute.Candidate{CanonicalName: "gpt-4.1", CredentialID: 123},
				Breakdown: autoroute.ScoringBreakdown{Composite: 72, MatchScore: 65, PriceScore: 50, ChannelQuality: 40}},
		},
	}

	// Step 1: Decision → wire
	wire := decisionToWire(dec)

	// Step 2: wire → JSON (simulates writeAutoDecisionHeader + SetAutoDecision)
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Step 3: JSON → generic map (simulates what a client / DB consumer sees)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Assert every Decision field survived the round-trip.
	assertStr(t, decoded, "task_type", "code")
	assertFloat(t, decoded, "confidence", 0.87)
	assertStr(t, decoded, "profile", "speed_first")
	assertStr(t, decoded, "classifier", "heuristic_v2")
	assertStr(t, decoded, "reason", "code intent, speed-first")
	assertStr(t, decoded, "chosen_model", "claude-sonnet")
	assertStr(t, decoded, "chosen_raw_model", "claude-sonnet-4-2026")
	assertFloat(t, decoded, "chosen_credential_id", 777)

	// Event-level audit fields — these are the ones most likely to be
	// silently dropped by future struct changes.
	if v, ok := decoded["cache_reused"]; !ok || v != false {
		t.Fatalf("cache_reused missing or wrong: %v", decoded["cache_reused"])
	}
	if v, ok := decoded["fallback_used"]; !ok || v != true {
		t.Fatalf("fallback_used missing or wrong: %v", decoded["fallback_used"])
	}

	// enabled_features
	features, ok := decoded["enabled_features"].([]any)
	if !ok || len(features) != 2 {
		t.Fatalf("enabled_features missing or wrong length: %v", decoded["enabled_features"])
	}
	if features[0] != "channel_quality_routing" || features[1] != "cache_revalidation" {
		t.Fatalf("enabled_features content mismatch: %v", features)
	}

	// candidates_top3 — at least the winner must survive
	cands, ok := decoded["candidates_top3"].([]any)
	if !ok || len(cands) != 2 {
		t.Fatalf("candidates_top3 missing or wrong length: %v", decoded["candidates_top3"])
	}
	winner, ok := cands[0].(map[string]any)
	if !ok {
		t.Fatal("winner candidate not a map")
	}
	assertStr(t, winner, "model", "claude-sonnet")
	assertFloat(t, winner, "composite_score", 85)
}

func assertStr(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	if v, ok := m[key]; !ok || v != want {
		t.Fatalf("%s: want %q, got %v", key, want, m[key])
	}
}

func assertFloat(t *testing.T, m map[string]any, key string, want float64) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("%s: key missing", key)
	}
	switch n := v.(type) {
	case float64:
		if n != want {
			t.Fatalf("%s: want %v, got %v", key, want, n)
		}
	default:
		t.Fatalf("%s: expected numeric, got %T (%v)", key, v, v)
	}
}

// TestMaybeResolveAuto_NoDecider_StillFallsBack pins the legacy "decider
// not wired" fallback contract that the 2026-07-01 P1 fix preserves.
// When autoroute is disabled (the common case in environments that don't
// opt into the v2.0 routing pipeline), model=auto must still be rewritten
// to autoFallbackModel() so clients don't suddenly start seeing 502s.
// This test guards against an accidental regression where the new
// shouldFail semantics are applied to the nil-decider path too.
func TestMaybeResolveAuto_NoDecider_StillFallsBack(t *testing.T) {
	h := &ChatHandler{} // no decider

	reqBody := &chatRequestBody{
		Model: autoRequestMagic,
	}
	rawBody := []byte(`{"model":"auto","messages":[{"role":"user","content":"hi"}]}`)

	body, wire, shouldFail := h.maybeResolveAuto(reqBody, rawBody, nil, 0)

	if shouldFail {
		t.Fatalf("no-decider path must not signal failure, got shouldFail=true")
	}
	if body == nil {
		t.Fatal("expected rewritten body when decider is nil")
	}
	if wire != nil {
		t.Errorf("wire must be nil when decider is nil, got %+v", wire)
	}
	if reqBody.Model != autoFallbackModel() {
		t.Errorf("reqBody.Model must be rewritten to %q when decider is nil, got %q",
			autoFallbackModel(), reqBody.Model)
	}
}

// TestMaybeResolveAuto_NonAutoRequest_NoOp covers the hot path: when the
// client passes a real model name (not the "auto" magic string),
// maybeResolveAuto must short-circuit and return (nil, nil, false) so the
// caller can keep using the original body without any rewrite.
func TestMaybeResolveAuto_NonAutoRequest_NoOp(t *testing.T) {
	h := &ChatHandler{}

	reqBody := &chatRequestBody{
		Model: "gpt-4.1",
	}
	rawBody := []byte(`{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}`)

	body, wire, shouldFail := h.maybeResolveAuto(reqBody, rawBody, nil, 0)

	if shouldFail {
		t.Errorf("non-auto request must not signal failure")
	}
	if body != nil {
		t.Errorf("non-auto request must not return a rewritten body, got %d bytes", len(body))
	}
	if wire != nil {
		t.Errorf("non-auto request must not return a wire decision, got %+v", wire)
	}
	if reqBody.Model != "gpt-4.1" {
		t.Errorf("reqBody.Model must remain untouched, got %q", reqBody.Model)
	}
}
