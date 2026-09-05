package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type sessionIdentityFixture struct {
	SchemaVersion int               `json:"schema_version"`
	TenantID      string            `json:"tenant_id"`
	GwSessionID   string            `json:"gw_session_id"`
	SessionID     string            `json:"session_id"`
	SessionPK     int64             `json:"session_pk"`
	RequestID     string            `json:"request_id"`
	AttemptID     string            `json:"attempt_id"`
	AttemptNo     int               `json:"attempt_no"`
	Semantics     map[string]string `json:"semantics"`
}

func loadSessionIdentityFixture(t *testing.T) sessionIdentityFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "fixtures", "session_identity_v1_valid.json"))
	if err != nil {
		t.Fatalf("read identity fixture: %v", err)
	}
	var fixture sessionIdentityFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode identity fixture: %v", err)
	}
	return fixture
}

func TestSessionIdentityV1Fixture(t *testing.T) {
	fixture := loadSessionIdentityFixture(t)
	if fixture.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", fixture.SchemaVersion)
	}
	if fixture.TenantID == "" || fixture.GwSessionID == "" || fixture.SessionID == "" || fixture.RequestID == "" || fixture.AttemptID == "" {
		t.Fatalf("identity fixture has an empty required text identifier: %+v", fixture)
	}
	if fixture.SessionPK <= 0 || fixture.AttemptNo <= 0 {
		t.Fatalf("session_pk and attempt_no must be positive: %+v", fixture)
	}
	seen := make(map[string]string, 4)
	for name, id := range map[string]string{
		"gw_session_id": fixture.GwSessionID,
		"session_id":    fixture.SessionID,
		"request_id":    fixture.RequestID,
		"attempt_id":    fixture.AttemptID,
	} {
		if previous, ok := seen[id]; ok {
			t.Fatalf("%s reuses %s value %q", name, previous, id)
		}
		seen[id] = name
	}
	wantSemantics := map[string]string{
		"gw_session_id": "gateway_logical_session_text",
		"session_id":    "sessions_v2_text",
		"session_pk":    "public_sessions_numeric_surrogate",
		"request_id":    "server_request_identity",
		"attempt_id":    "execution_attempt_identity",
		"attempt_no":    "one_based_attempt_sequence",
	}
	for key, want := range wantSemantics {
		if got := fixture.Semantics[key]; got != want {
			t.Fatalf("semantics[%q] = %q, want %q", key, got, want)
		}
	}
}
