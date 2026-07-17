package main

import (
	"testing"
)

func TestV1TurnStructure(t *testing.T) {
	// Test that V1Turn struct can be instantiated
	turn := V1Turn{
		RequestID:    "req_123",
		SessionID:    "gw_abc",
		TenantID:     "tenant_1",
		ClientModel:  "gpt-4",
		ProviderID:   "openai",
		CredentialID: "cred_1",
		CostUSD:      0.05,
		Success:      true,
	}
	
	if turn.RequestID != "req_123" {
		t.Errorf("RequestID = %v, want req_123", turn.RequestID)
	}
	if turn.CostUSD != 0.05 {
		t.Errorf("CostUSD = %v, want 0.05", turn.CostUSD)
	}
}

func TestV2TurnStructure(t *testing.T) {
	// Test that V2Turn struct can be instantiated
	turn := V2Turn{
		RequestID:        "req_123",
		TurnNo:           1,
		SessionID:        "gw_abc",
		TenantID:         "tenant_1",
		SubmitMode:       "full",
		Model:            "gpt-4",
		Provider:         "openai",
		CredentialID:     "cred_1",
		PromptTokens:     100,
		CompletionTokens: 50,
		CostUSD:          0.05,
		InjectionVerdict: "pass",
		OutputVerdict:    "pass",
		Success:          true,
		SourceKind:       "live",
		Quality:          "verified",
	}
	
	if turn.TurnNo != 1 {
		t.Errorf("TurnNo = %v, want 1", turn.TurnNo)
	}
	if turn.PromptTokens != 100 {
		t.Errorf("PromptTokens = %v, want 100", turn.PromptTokens)
	}
	if turn.SubmitMode != "full" {
		t.Errorf("SubmitMode = %v, want full", turn.SubmitMode)
	}
}

func TestV2BodyStructure(t *testing.T) {
	// Test that V2Body struct can be instantiated
	body := V2Body{
		SessionID: "gw_abc",
		TurnNo:    1,
		TenantID:  "tenant_1",
		RequestID: "req_123",
	}
	
	if body.TurnNo != 1 {
		t.Errorf("TurnNo = %v, want 1", body.TurnNo)
	}
	if body.SessionID != "gw_abc" {
		t.Errorf("SessionID = %v, want gw_abc", body.SessionID)
	}
}

func TestV2SessionStructure(t *testing.T) {
	// Test that V2Session struct can be instantiated
	session := V2Session{
		SessionID:        "gw_abc",
		TenantID:         "tenant_1",
		Status:           "active",
		TotalTurns:       10,
		TotalTokens:      1500,
		TotalCostUSD:     0.50,
		LastTurnNo:       10,
		LastModel:        "gpt-4",
		LastProvider:     "openai",
		PrimaryRequestID: "req_001",
	}
	
	if session.TotalTurns != 10 {
		t.Errorf("TotalTurns = %v, want 10", session.TotalTurns)
	}
	if session.TotalTokens != 1500 {
		t.Errorf("TotalTokens = %v, want 1500", session.TotalTokens)
	}
	if session.Status != "active" {
		t.Errorf("Status = %v, want active", session.Status)
	}
}

// Note: Integration tests for LoadV1Turns, LoadV2Turns, etc. require a real database
// and are better suited for integration test suite. These unit tests verify struct
// definitions and basic type safety.
