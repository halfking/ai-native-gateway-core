// Package admin — candidate_failure_handlers.go
//
// 2026-06-23 Phase 2 (P1) + Phase 3 (P2) of the minimax-m3 transient-error
// fix. Admin endpoints for the candidate_failure_logs table and the
// CandidateFailureMonitor alert ring.
//
// Phase 2 endpoints (DB-backed, persistent):
//
//	GET /api/candidate-failures                     — list recent failures (paged)
//	GET /api/candidate-failures/stats               — aggregated by (model, kind)
//	GET /api/candidate-failures/credential/{id}     — per-credential history
//
// Phase 3 endpoint (in-memory, live):
//
//	GET /api/candidate-failures/alerts              — recent alerts from monitor
//
// All endpoints are read-only and require the same admin auth as the rest
// of /api/* (Bearer admin JWT or X-Admin-User header check). They use the
// gateway's main DB pool, so they piggyback on the request_logs connection
// (no separate pool).
package admin

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/bg"
)

// candidateFailureHandlers bundles the read-only endpoints. nil-safe:
// each method is a no-op when h.db is nil.
type candidateFailureHandlers struct {
	db *pgxpool.Pool

	// alertsMu protects alertsGetter. Holds a closure that returns the
	// current alert snapshot. nil when no monitor is wired, in which case
	// the /alerts endpoint returns an empty array.
	alertsMu     sync.RWMutex
	alertsGetter func() []bg.CandidateFailureAlert
}

// CandidateFailureAlert is re-exported here so the admin package can expose
// the alert shape in its API response. The fields are owned by bg — the
// admin package never constructs or mutates one. Using a type alias keeps
// the two references identical (no copy-on-read), which matters for the
// in-memory ring buffer.
type CandidateFailureAlert = bg.CandidateFailureAlert

// SetRecentAlerts wires the live snapshot getter. Called by main.go
// after starting the CandidateFailureMonitor so /alerts returns hot data.
func (h *candidateFailureHandlers) SetRecentAlerts(getter func() []bg.CandidateFailureAlert) {
	h.alertsMu.Lock()
	defer h.alertsMu.Unlock()
	h.alertsGetter = getter
}

// listCandidateFailures returns the most recent N failures. Pagination is
// by (ts, id) keyset (not OFFSET) so large pages stay fast.
//
// Query params:
//   - limit:    max rows (1..500, default 100)
//   - since:    ISO-8601 lower bound (default 24h ago)
//   - kind:     optional error_kind filter (e.g. "transient", "network")
//   - retryable: optional "true"/"false" filter
func (h *candidateFailureHandlers) listCandidateFailures(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not configured")
		return
	}
	limit := parseIntQuery(r, "limit", 100, 1, 500)
	since := parseSinceQuery(r, "since", 24*time.Hour)
	kind := r.URL.Query().Get("kind")
	retryable := r.URL.Query().Get("retryable")

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			id, ts, request_id, tenant_id, credential_id, provider_id,
			raw_model_name, attempt_index, error_kind, error_message,
			upstream_status_code, upstream_response_preview, latency_ms,
			retryable
		FROM candidate_failure_logs
		WHERE ts >= $1
		  AND ($2 = '' OR error_kind = $2)
		  AND ($3 = '' OR retryable::text = $3)
		ORDER BY ts DESC, id DESC
		LIMIT $4
	`, since, kind, retryable, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type row struct {
		ID                      int64     `json:"id"`
		Ts                      time.Time `json:"ts"`
		RequestID               string    `json:"request_id"`
		TenantID                string    `json:"tenant_id"`
		CredentialID            int       `json:"credential_id"`
		ProviderID              int       `json:"provider_id"`
		RawModelName            string    `json:"raw_model_name"`
		AttemptIndex            int       `json:"attempt_index"`
		ErrorKind               string    `json:"error_kind"`
		ErrorMessage            string    `json:"error_message"`
		UpstreamStatusCode      *int      `json:"upstream_status_code,omitempty"`
		UpstreamResponsePreview *string   `json:"upstream_response_preview,omitempty"`
		LatencyMs               *int      `json:"latency_ms,omitempty"`
		Retryable               *bool     `json:"retryable,omitempty"`
	}
	out := make([]row, 0, limit)
	for rows.Next() {
		var x row
		if err := rows.Scan(
			&x.ID, &x.Ts, &x.RequestID, &x.TenantID, &x.CredentialID, &x.ProviderID,
			&x.RawModelName, &x.AttemptIndex, &x.ErrorKind, &x.ErrorMessage,
			&x.UpstreamStatusCode, &x.UpstreamResponsePreview, &x.LatencyMs,
			&x.Retryable,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  out,
		"count": len(out),
		"limit": limit,
		"since": since,
	})
}

// getCandidateFailuresByCredential returns the recent failure history for
// a specific credential, ordered by ts DESC.
func (h *candidateFailureHandlers) getCandidateFailuresByCredential(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not configured")
		return
	}
	credID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid credential id")
		return
	}
	limit := parseIntQuery(r, "limit", 50, 1, 200)
	since := parseSinceQuery(r, "since", 24*time.Hour)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			id, ts, request_id, raw_model_name, attempt_index, error_kind,
			upstream_status_code, upstream_response_preview, latency_ms
		FROM candidate_failure_logs
		WHERE credential_id = $1
		  AND ts >= $2
		ORDER BY ts DESC, id DESC
		LIMIT $3
	`, credID, since, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type row struct {
		ID                      int64     `json:"id"`
		Ts                      time.Time `json:"ts"`
		RequestID               string    `json:"request_id"`
		RawModelName            string    `json:"raw_model_name"`
		AttemptIndex            int       `json:"attempt_index"`
		ErrorKind               string    `json:"error_kind"`
		UpstreamStatusCode      *int      `json:"upstream_status_code,omitempty"`
		UpstreamResponsePreview *string   `json:"upstream_response_preview,omitempty"`
		LatencyMs               *int      `json:"latency_ms,omitempty"`
	}
	out := make([]row, 0, limit)
	for rows.Next() {
		var x row
		if err := rows.Scan(
			&x.ID, &x.Ts, &x.RequestID, &x.RawModelName, &x.AttemptIndex,
			&x.ErrorKind, &x.UpstreamStatusCode, &x.UpstreamResponsePreview,
			&x.LatencyMs,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":          out,
		"count":         len(out),
		"credential_id": credID,
		"since":         since,
	})
}

// getCandidateFailureStats aggregates the recent failures grouped by
// (raw_model_name, error_kind, credential_id). Returns the top offenders
// so the admin UI can show "minimax-m3 / transient: 47 in last 1h".
func (h *candidateFailureHandlers) getCandidateFailureStats(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "db not configured")
		return
	}
	since := parseSinceQuery(r, "since", 1*time.Hour)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			raw_model_name,
			error_kind,
			credential_id,
			provider_id,
			COUNT(*)                       AS count,
			COUNT(*) FILTER (WHERE retryable = TRUE) AS retryable_count,
			COUNT(DISTINCT upstream_status_code) AS distinct_status_codes,
			MAX(ts)                        AS last_seen,
			MIN(ts)                        AS first_seen
		FROM candidate_failure_logs
		WHERE ts >= $1
		GROUP BY raw_model_name, error_kind, credential_id, provider_id
		ORDER BY count DESC
		LIMIT 50
	`, since)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type row struct {
		RawModelName     string    `json:"raw_model_name"`
		ErrorKind        string    `json:"error_kind"`
		CredentialID     int       `json:"credential_id"`
		ProviderID       int       `json:"provider_id"`
		Count            int       `json:"count"`
		RetryableCount   int       `json:"retryable_count"`
		DistinctStatuses int       `json:"distinct_status_codes"`
		LastSeen         time.Time `json:"last_seen"`
		FirstSeen        time.Time `json:"first_seen"`
	}
	out := make([]row, 0, 50)
	for rows.Next() {
		var x row
		if err := rows.Scan(
			&x.RawModelName, &x.ErrorKind, &x.CredentialID, &x.ProviderID,
			&x.Count, &x.RetryableCount, &x.DistinctStatuses,
			&x.LastSeen, &x.FirstSeen,
		); err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed: "+err.Error())
			return
		}
		out = append(out, x)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  out,
		"count": len(out),
		"since": since,
	})
}

// listRecentAlerts returns the in-memory alert ring from the
// CandidateFailureMonitor. The getter is invoked on every request so
// callers see hot updates.
func (h *candidateFailureHandlers) listRecentAlerts(w http.ResponseWriter, r *http.Request) {
	h.alertsMu.RLock()
	getter := h.alertsGetter
	h.alertsMu.RUnlock()
	var out []bg.CandidateFailureAlert
	if getter != nil {
		out = getter()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":  out,
		"count": len(out),
	})
}

// parseIntQuery returns the int value of ?key= or def if absent / invalid.
// min/max are inclusive bounds applied silently.
func parseIntQuery(r *http.Request, key string, def, min, max int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// parseSinceQuery returns the lower bound for time-windowed queries.
// Accepts an explicit ISO-8601 timestamp via ?since=2026-06-22T10:00:00Z,
// otherwise falls back to now()-window.
func parseSinceQuery(r *http.Request, key string, window time.Duration) time.Time {
	v := r.URL.Query().Get(key)
	if v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t
		}
	}
	return time.Now().Add(-window)
}
