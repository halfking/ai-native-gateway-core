package main

import (
	"encoding/json"
	"testing"
)

// Note: Tests use ValidationCheckV2 which has Severity and Description fields
// This is the extended version used in validator.go

func TestCheckRequestIDParity_AllMatch(t *testing.T) {
	v1Turns := []V1Turn{
		{RequestID: "req_1"},
		{RequestID: "req_2"},
		{RequestID: "req_3"},
	}
	v2Turns := []V2Turn{
		{RequestID: "req_1"},
		{RequestID: "req_2"},
		{RequestID: "req_3"},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkRequestIDParity(v1Turns, v2Turns)

	if !check.Passed {
		t.Errorf("Expected check to pass, got failed: %s", check.Description)
	}
	if check.Severity != "error" {
		t.Errorf("Expected severity=error, got %s", check.Severity)
	}
	if check.V1Value != 3 || check.V2Value != 3 {
		t.Errorf("Expected V1=3, V2=3, got V1=%v, V2=%v", check.V1Value, check.V2Value)
	}
}

func TestCheckRequestIDParity_MissingInV2(t *testing.T) {
	v1Turns := []V1Turn{
		{RequestID: "req_1"},
		{RequestID: "req_2"},
		{RequestID: "req_3"},
	}
	v2Turns := []V2Turn{
		{RequestID: "req_1"},
		{RequestID: "req_2"},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkRequestIDParity(v1Turns, v2Turns)

	if check.Passed {
		t.Errorf("Expected check to fail, got passed")
	}
	if len(check.Details) == 0 {
		t.Errorf("Expected details about missing request, got none")
	}
}

func TestCheckTokenSum_WithinTolerance(t *testing.T) {
	v1Turns := []V1Turn{
		{Usage: json.RawMessage(`{"prompt_tokens": 100, "completion_tokens": 50}`)},
		{Usage: json.RawMessage(`{"prompt_tokens": 200, "completion_tokens": 100}`)},
	}
	v2Turns := []V2Turn{
		{PromptTokens: 100, CompletionTokens: 50},
		{PromptTokens: 200, CompletionTokens: 100},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkTokenSum(v1Turns, v2Turns)

	if !check.Passed {
		t.Errorf("Expected check to pass, got failed: %s", check.Description)
	}
	if check.V1Value != int64(450) || check.V2Value != int64(450) {
		t.Errorf("Expected V1=450, V2=450, got V1=%v, V2=%v", check.V1Value, check.V2Value)
	}
}

func TestCheckTokenSum_OutsideTolerance(t *testing.T) {
	v1Turns := []V1Turn{
		{Usage: json.RawMessage(`{"prompt_tokens": 1000, "completion_tokens": 500}`)},
	}
	v2Turns := []V2Turn{
		{PromptTokens: 1000, CompletionTokens: 600}, // 100 token difference
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkTokenSum(v1Turns, v2Turns)

	if check.Passed {
		t.Errorf("Expected check to fail due to token mismatch")
	}
	if check.Severity != "warning" {
		t.Errorf("Expected severity=warning, got %s", check.Severity)
	}
}

func TestCheckCostSum_ExactMatch(t *testing.T) {
	v1Turns := []V1Turn{
		{CostUSD: 0.05},
		{CostUSD: 0.10},
		{CostUSD: 0.15},
	}
	v2Turns := []V2Turn{
		{CostUSD: 0.05},
		{CostUSD: 0.10},
		{CostUSD: 0.15},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkCostSum(v1Turns, v2Turns)

	if !check.Passed {
		t.Errorf("Expected check to pass, got failed: %s", check.Description)
	}
	// Use approximate comparison for floating point
	v1Cost, ok := check.V1Value.(float64)
	if !ok || abs64(v1Cost-0.30) > 0.0001 {
		t.Errorf("Expected V1≈0.30, got %v", check.V1Value)
	}
}

func TestCheckMetadataConsistency_AllMatch(t *testing.T) {
	v1Turns := []V1Turn{
		{
			RequestID:       "req_1",
			ClientModel:     "gpt-4",
			ProviderID:      "openai",
			CredentialID:    "cred_1",
			CompressionMeta: json.RawMessage(`{"injection_verdict": "pass", "output_verdict": "pass"}`),
		},
	}
	v2Turns := []V2Turn{
		{
			RequestID:        "req_1",
			Model:            "gpt-4",
			Provider:         "openai",
			CredentialID:     "cred_1",
			InjectionVerdict: "pass",
			OutputVerdict:    "pass",
		},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkMetadataConsistency(v1Turns, v2Turns)

	if !check.Passed {
		t.Errorf("Expected check to pass, got failed: %s", check.Description)
	}
}

func TestCheckMetadataConsistency_ModelMismatch(t *testing.T) {
	v1Turns := []V1Turn{
		{
			RequestID:   "req_1",
			ClientModel: "gpt-4",
			ProviderID:  "openai",
		},
	}
	v2Turns := []V2Turn{
		{
			RequestID: "req_1",
			Model:     "gpt-3.5-turbo",
			Provider:  "openai",
		},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkMetadataConsistency(v1Turns, v2Turns)

	if check.Passed {
		t.Errorf("Expected check to fail due to model mismatch")
	}
	if len(check.Details) == 0 {
		t.Errorf("Expected details about mismatch, got none")
	}
}

func TestCheckSnapshotAccuracy_Perfect(t *testing.T) {
	v2Turns := []V2Turn{
		{TurnNo: 1, PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.05},
		{TurnNo: 2, PromptTokens: 200, CompletionTokens: 100, CostUSD: 0.10},
	}
	v2Session := &V2Session{
		TotalTurns:   2,
		TotalTokens:  450,
		TotalCostUSD: 0.15,
		LastTurnNo:   2,
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkSnapshotAccuracy(v2Turns, v2Session)

	if !check.Passed {
		t.Errorf("Expected check to pass, got failed: %s", check.Description)
	}
}

func TestCheckSnapshotAccuracy_TurnCountMismatch(t *testing.T) {
	v2Turns := []V2Turn{
		{TurnNo: 1, PromptTokens: 100, CompletionTokens: 50, CostUSD: 0.05},
		{TurnNo: 2, PromptTokens: 200, CompletionTokens: 100, CostUSD: 0.10},
	}
	v2Session := &V2Session{
		TotalTurns:   3, // Wrong count
		TotalTokens:  450,
		TotalCostUSD: 0.15,
		LastTurnNo:   2,
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkSnapshotAccuracy(v2Turns, v2Session)

	if check.Passed {
		t.Errorf("Expected check to fail due to turn count mismatch")
	}
}

func TestCheckBodiesIntegrity_ValidJSON(t *testing.T) {
	v2Turns := []V2Turn{
		{TurnNo: 1, SubmitMode: "full"},
	}
	v2Bodies := []V2Body{
		{
			RequestID:     "",
			SessionID:     "",
			TenantID:      "",
			TurnNo:        1,
			RequestDelta:  json.RawMessage(`[{"role": "user", "content": "hello"}]`),
			ResponseDelta: json.RawMessage(`[{"role": "assistant", "content": "hi"}]`),
		},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkBodiesIntegrity(v2Turns, v2Bodies)

	if !check.Passed {
		t.Errorf("Expected check to pass, got failed: %s", check.Description)
	}
}

func TestCheckBodiesIntegrity_InvalidJSON(t *testing.T) {
	v2Turns := []V2Turn{
		{TurnNo: 1, SubmitMode: "full"},
	}
	v2Bodies := []V2Body{
		{
			TurnNo:       1,
			RequestDelta: json.RawMessage(`{invalid json`),
		},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkBodiesIntegrity(v2Turns, v2Bodies)

	if check.Passed {
		t.Errorf("Expected check to fail due to invalid JSON")
	}
	if check.Severity != "error" {
		t.Errorf("Expected severity=error, got %s", check.Severity)
	}
}

func TestCheckBodiesIntegrity_CompressedMode(t *testing.T) {
	v2Turns := []V2Turn{
		{TurnNo: 1, SubmitMode: "inferred_compressed"},
	}
	v2Bodies := []V2Body{
		{
			TurnNo:        1,
			RequestDelta:  json.RawMessage(`[]`),
			ResponseDelta: json.RawMessage(`[]`),
		},
	}

	validator := NewSessionValidator("tenant_1", "gw_abc")
	check := validator.checkBodiesIntegrity(v2Turns, v2Bodies)

	if !check.Passed {
		t.Errorf("Expected check to pass (compressed mode is warning, not error)")
	}
	if check.Severity != "warning" {
		t.Errorf("Expected severity=warning for compressed mode, got %s", check.Severity)
	}
}
