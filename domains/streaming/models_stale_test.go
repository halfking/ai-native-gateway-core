package streaming

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestModelsHandler_ServesLastGoodOnDeadDB pins the 2026-09-04 availability
// gear: once a successful query has populated the last-good cache, DB
// failures (and even a lost pool) keep serving the cached list within
// modelsStaleGrace instead of 5xx-ing a pure read endpoint.
func TestModelsHandler_ServesLastGoodOnDeadDB(t *testing.T) {
	// Port 1 is unbound on every supported platform: dial fails with
	// "connection refused" immediately, simulating a crashed PostgreSQL.
	pool, err := pgxpool.New(context.Background(), "postgres://u:p@127.0.0.1:1/db?sslmode=disable")
	if err != nil {
		t.Fatalf("construct dead pool: %v", err)
	}
	defer pool.Close()

	h := NewModelsHandler()
	h.SetDB(pool)
	h.rememberGoodEntries([]modelEntry{{ID: "gpt-x", Object: "model"}})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 serving last-good during outage; body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Object string       `json:"object"`
		Data   []modelEntry `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(payload.Data) != 1 || payload.Data[0].ID != "gpt-x" {
		t.Fatalf("data = %+v, want the cached gpt-x entry", payload.Data)
	}
}

// TestModelsHandler_NilPoolWithoutCacheStill503 pins that the gear only
// applies when this process actually served a list before — a DB-less boot
// with no cache keeps the explicit 503 (no hallucinated model lists).
func TestModelsHandler_NilPoolWithoutCacheStill503(t *testing.T) {
	h := NewModelsHandler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestModelsHandler_StaleGraceExpiry pins the bound: entries older than
// modelsStaleGrace must not be served.
func TestModelsHandler_StaleGraceExpiry(t *testing.T) {
	h := NewModelsHandler()
	h.rememberGoodEntries([]modelEntry{{ID: "gpt-x", Object: "model"}})
	h.staleMu.Lock()
	h.staleAt = time.Now().Add(-(modelsStaleGrace + time.Minute))
	h.staleMu.Unlock()

	if _, ok := h.lastGoodEntries(); ok {
		t.Fatal("entry past modelsStaleGrace must not serve")
	}
}
