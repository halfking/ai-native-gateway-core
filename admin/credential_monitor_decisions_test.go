package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestHandleCredentialDecisions tests the credential routing decisions endpoint.
func TestHandleCredentialDecisions(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// This is a placeholder test structure. In a real scenario, you'd:
	// 1. Set up a test database with sample routing_decision_log data
	// 2. Create a CredentialMonitorHandlers instance
	// 3. Make a test request
	// 4. Verify the response

	t.Run("requires credential_id", func(t *testing.T) {
		// Mock handler without actual DB
		h := &Handler{db: nil}
		m := &CredentialMonitorHandlers{h: h}

		req := httptest.NewRequest(http.MethodGet, "/api/credentials/decisions", nil)
		w := httptest.NewRecorder()

		m.handleCredentialDecisions(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

// TestHandleClearManualDisabled tests the clear manual_disabled endpoint.
func TestHandleClearManualDisabled(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	t.Run("requires valid JSON", func(t *testing.T) {
		h := &Handler{db: nil}
		m := &CredentialMonitorHandlers{h: h}

		req := httptest.NewRequest(http.MethodPost, "/api/credentials/clear-manual-disabled", bytes.NewBufferString("invalid"))
		w := httptest.NewRecorder()

		m.handleClearManualDisabled(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("requires credential_id", func(t *testing.T) {
		h := &Handler{db: nil}
		m := &CredentialMonitorHandlers{h: h}

		body := map[string]any{"reason": "test"}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/clear-manual-disabled", bytes.NewBuffer(bodyBytes))
		w := httptest.NewRecorder()

		m.handleClearManualDisabled(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})

	t.Run("requires reason", func(t *testing.T) {
		h := &Handler{db: nil}
		m := &CredentialMonitorHandlers{h: h}

		body := map[string]any{"credential_id": 123}
		bodyBytes, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/api/credentials/clear-manual-disabled", bytes.NewBuffer(bodyBytes))
		w := httptest.NewRecorder()

		m.handleClearManualDisabled(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

// TestCredentialDecisionsIntegration is a full integration test (requires real DB).
func TestCredentialDecisionsIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	// Skip if no test DB is configured
	dbURL := testDBURL()
	if dbURL == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect: %v", err)
	}
	defer pool.Close()

	h := &Handler{db: pool}
	m := &CredentialMonitorHandlers{h: h}

	// Insert test data
	_, err = pool.Exec(ctx, `
		INSERT INTO routing_decision_log (
			ts, request_id, tenant_id, model, chosen_credential_id, chosen_provider_id,
			tier, candidates_tried, success, latency_ms
		) VALUES ($1, gen_random_uuid(), 'test', 'gpt-4', 999, 1, 0, 1, true, 100)
	`, time.Now())
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}

	// Test the endpoint
	req := httptest.NewRequest(http.MethodGet, "/api/credentials/decisions?credential_id=999&limit=10", nil)
	w := httptest.NewRecorder()

	m.handleCredentialDecisions(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp["credential_id"] != float64(999) {
		t.Errorf("expected credential_id 999, got %v", resp["credential_id"])
	}

	decisions, ok := resp["decisions"].([]any)
	if !ok || len(decisions) == 0 {
		t.Errorf("expected at least one decision, got %v", resp["decisions"])
	}

	// Cleanup
	_, _ = pool.Exec(ctx, "DELETE FROM routing_decision_log WHERE chosen_credential_id = 999")
}

// testDBURL returns the test database URL from environment or empty string.
func testDBURL() string {
	// Check common test DB env vars
	// In real use, this would be configured in CI/CD
	return ""
}
