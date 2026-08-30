package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
)

func TestLoadV1TurnsJoinsBodiesByRequestIDAndTimestampAcrossStores(t *testing.T) {
	mockDB, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new mock pool: %v", err)
	}
	defer mockDB.Close()

	firstTS := time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC)
	secondTS := firstTS.Add(time.Minute)
	metaRows := mockDB.NewRows([]string{
		"request_id", "ts", "gw_session_id", "tenant_id", "client_model", "provider_id", "credential_id",
		"prompt_tokens", "completion_tokens", "cache_read_tokens", "cache_write_tokens", "cost_usd", "success",
	}).AddRow("req_reused", firstTS, "gw_fixture", "tenant_fixture", "gpt-test", "provider-test", "credential-test", 1, 2, 0, 0, 0.01, true).
		AddRow("req_reused", secondTS, "gw_fixture", "tenant_fixture", "gpt-test", "provider-test", "credential-test", 3, 4, 0, 0, 0.02, true)
	mockDB.ExpectQuery("FROM request_logs").WithArgs("tenant_fixture", "gw_fixture").WillReturnRows(metaRows)

	mockDB.ExpectQuery("request_logs_bodies").WithArgs("req_reused", firstTS).WillReturnRows(
		mockDB.NewRows([]string{"request_body", "response_body"}).AddRow(
			json.RawMessage(`{"messages":[{"role":"user","content":"hot body"}]}`),
			json.RawMessage(`{"choices":[]}`),
		),
	)
	mockDB.ExpectQuery("request_logs_bodies").WithArgs("req_reused", secondTS).WillReturnRows(
		mockDB.NewRows([]string{"request_body", "response_body"}).AddRow(
			json.RawMessage(`{"messages":[{"role":"user","content":"partition body"}]}`),
			json.RawMessage(`{"choices":[]}`),
		),
	)

	loader := NewSessionLoader(mockDB)
	turns, err := loader.LoadV1Turns(context.Background(), "tenant_fixture", "gw_fixture")
	if err != nil {
		t.Fatalf("load V1 turns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("got %d turns, want 2", len(turns))
	}
	if string(turns[0].RequestBody) != `{"messages":[{"role":"user","content":"hot body"}]}` {
		t.Errorf("first request body = %s", turns[0].RequestBody)
	}
	if string(turns[1].RequestBody) != `{"messages":[{"role":"user","content":"partition body"}]}` {
		t.Errorf("second request body = %s", turns[1].RequestBody)
	}
	if err := mockDB.ExpectationsWereMet(); err != nil {
		t.Fatalf("mock expectations: %v", err)
	}
}

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

func TestCanonicalQueryContracts(t *testing.T) {
	data, err := os.ReadFile("loader.go")
	if err != nil {
		t.Fatalf("read loader.go: %v", err)
	}
	source := string(data)
	for _, want := range []string{
		"public.session_turns_with_current_month",
		"public.session_bodies_with_current_month",
		"gw_session_id",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("loader source missing canonical contract %q", want)
		}
	}
	if strings.Contains(source, "FROM session_turns") || strings.Contains(source, "FROM session_bodies") {
		t.Fatal("validator must not read V2 parent tables directly")
	}
}

// Note: Integration tests for LoadV1Turns, LoadV2Turns, etc. require a real database
// and are better suited for integration test suite. These unit tests verify struct
// definitions, basic type safety, and the read-only query contract.
