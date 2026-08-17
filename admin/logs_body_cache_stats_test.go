// Copyright 2026 kaixuan.ai
// Handler-level tests for GET /api/admin/logs/body-cache-stats.
//
// 2026-08-17 audit: the endpoint shipped without handler tests (only the
// cache primitive had coverage). These are DB-free — the stats endpoint
// reads atomic counters and LRU length only — so they also run under
// -short, unlike the columnar tests in logs_get_log_columnar_test.go.
package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleBodyFetchCacheStats_GET_ReturnsSnapshotWithCap asserts the
// response contract: all counters, hit_rate, and the LRU cap. cap is
// exposed so the UI never hardcodes the capacity denominator.
func TestHandleBodyFetchCacheStats_GET_ReturnsSnapshotWithCap(t *testing.T) {
	h := &Handler{bodyFetchCache: newBodyFetchCache(128, time.Minute)}
	h.bodyFetchCache.Put("req-a", map[string]any{"k": "v"}, nil)
	h.bodyFetchCache.Get("req-a") // 1 hit
	h.bodyFetchCache.Get("req-b") // 1 miss

	rec := httptest.NewRecorder()
	h.handleBodyFetchCacheStats(rec, httptest.NewRequest(http.MethodGet, "/api/admin/logs/body-cache-stats", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Size      int     `json:"size"`
		Hits      int     `json:"hits"`
		Misses    int     `json:"misses"`
		Evictions int     `json:"evictions"`
		HitRate   float64 `json:"hit_rate"`
		Cap       int     `json:"cap"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, 1, body.Size)
	assert.Equal(t, 1, body.Hits)
	assert.Equal(t, 1, body.Misses)
	assert.Equal(t, 0, body.Evictions)
	assert.InDelta(t, 0.5, body.HitRate, 1e-9)
	assert.Equal(t, 128, body.Cap, "cap must be exposed so the UI never hardcodes the LRU limit")
}

// TestHandleBodyFetchCacheStats_EmptyCounters asserts hit_rate is 0 (not
// NaN) when no traffic has been recorded yet — the cold-start snapshot.
func TestHandleBodyFetchCacheStats_EmptyCounters(t *testing.T) {
	h := &Handler{bodyFetchCache: newBodyFetchCache(64, time.Minute)}

	rec := httptest.NewRecorder()
	h.handleBodyFetchCacheStats(rec, httptest.NewRequest(http.MethodGet, "/api/admin/logs/body-cache-stats", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, float64(0), body["hit_rate"])
	assert.Equal(t, float64(0), body["hits"])
}

func TestHandleBodyFetchCacheStats_MethodNotAllowed(t *testing.T) {
	h := &Handler{bodyFetchCache: newBodyFetchCache(8, time.Minute)}
	rec := httptest.NewRecorder()
	h.handleBodyFetchCacheStats(rec, httptest.NewRequest(http.MethodPost, "/api/admin/logs/body-cache-stats", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestHandleBodyFetchCacheStats_UninitializedCacheReturns503(t *testing.T) {
	h := &Handler{}
	rec := httptest.NewRecorder()
	h.handleBodyFetchCacheStats(rec, httptest.NewRequest(http.MethodGet, "/api/admin/logs/body-cache-stats", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
