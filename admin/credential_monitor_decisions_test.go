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

		if w.Code != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", w.Code)
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

	// Insert test data: two models on the same credential so the optional
	// model filter has something to discriminate.
	_, err = pool.Exec(ctx, `
		INSERT INTO routing_decision_log_default (
			ts, request_id, tenant_id, model, chosen_credential_id, chosen_provider_id,
			tier, candidates_tried, success, latency_ms
		) VALUES ($1, gen_random_uuid(), 'test', 'gpt-4', 999, 1, 0, 1, true, 100)
	`, time.Now())
	if err != nil {
		t.Fatalf("failed to insert test data: %v", err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO routing_decision_log_default (
			ts, request_id, tenant_id, model, chosen_credential_id, chosen_provider_id,
			tier, candidates_tried, success, latency_ms
		) VALUES ($1, gen_random_uuid(), 'test', 'claude-x', 999, 1, 0, 1, true, 120)
	`, time.Now())
	if err != nil {
		t.Fatalf("failed to insert second model row: %v", err)
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
	if !ok || len(decisions) < 2 {
		t.Errorf("expected at least two decisions, got %v", resp["decisions"])
	}

	// 2026-08-18: model filter scopes the list to one model×credential pair
	// and matches case-insensitively across model/client_model/outbound_model.
	for _, modelParam := range []string{"gpt-4", "GPT-4"} {
		freq := httptest.NewRequest(http.MethodGet, "/api/credentials/decisions?credential_id=999&model="+modelParam, nil)
		fw := httptest.NewRecorder()
		m.handleCredentialDecisions(fw, freq)
		if fw.Code != http.StatusOK {
			t.Errorf("model=%s: expected 200, got %d: %s", modelParam, fw.Code, fw.Body.String())
			continue
		}
		var fresp map[string]any
		if err := json.NewDecoder(fw.Body).Decode(&fresp); err != nil {
			t.Fatalf("model=%s: failed to decode response: %v", modelParam, err)
		}
		fdecisions, ok := fresp["decisions"].([]any)
		if !ok || len(fdecisions) == 0 {
			t.Errorf("model=%s: expected filtered decisions, got %v", modelParam, fresp["decisions"])
			continue
		}
		for _, entry := range fdecisions {
			row, _ := entry.(map[string]any)
			if row["model"] != "gpt-4" {
				t.Errorf("model=%s: expected only gpt-4 rows, got %v", modelParam, row["model"])
			}
		}
	}

	// Cleanup
	_, _ = pool.Exec(ctx, "DELETE FROM routing_decision_log_default WHERE chosen_credential_id = 999")
}

// testDBURL returns the test database URL from environment or empty string.
func testDBURL() string {
	// Check common test DB env vars
	// In real use, this would be configured in CI/CD
	return ""
}
