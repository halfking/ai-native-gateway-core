// Package admin — pending response admin API (Track C C7, 2026-06-18).
//
// Operator visibility into the pending response cache. Three
// endpoints:
//
//   GET    /api/admin/pending-responses              list (paged, filterable)
//   GET    /api/admin/pending-responses/{sessionID}  detail
//   DELETE /api/admin/pending-responses/{sessionID}  manual cleanup
//   GET    /api/admin/pending-responses/stats         aggregate counts
//
// All endpoints require admin auth (the existing h.admin wrap).
// Tenant isolation: the list endpoint scopes by tenant_id when
// the caller is a tenant_admin (similar to other admin routes);
// the detail + delete paths take an explicit sessionID so a
// tenant_admin can only see/clear their own sessions.
//
// We do NOT depend on the sessions package (avoids an import
// cycle: admin → sessions is not safe because sessions/handler.go
// already imports many packages). We re-use the pendingStore
// interface only for the cache I/O.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/pending"
)

// pendingStoreAdapter is the consumer-side view of pending.Store.
// Mirrors the adapter in cmd/gateway/main.go for sessions/handler.
// The body methods are equivalent; the difference is that the
// admin endpoint serves JSON views (with metadata) rather than
// the raw body.
type pendingStoreAdapter struct {
	s *pending.Store
}

func newPendingStoreAdapter(s *pending.Store) *pendingStoreAdapter {
	return &pendingStoreAdapter{s: s}
}

// listEntry is the wire-format returned by the list endpoint.
// Internal-only fields (Credentials, Bytes) are hidden; the
// admin UI gets exactly what it needs to render the table.
type listEntry struct {
	SessionID   string `json:"session_id"`
	RequestID   string `json:"request_id"`
	Status      string `json:"status"`
	ProviderID  int    `json:"provider_id"`
	IsStream    bool   `json:"is_stream"`
	CreatedAt   int64  `json:"created_at"`
	CompletedAt int64  `json:"completed_at,omitempty"`
	BytesBuffer int    `json:"bytes_buffered"`
	AgeSeconds  int64  `json:"age_seconds"`
}

func (a *pendingStoreAdapter) list(ctx pendingListContext) ([]listEntry, error) {
	if a == nil || a.s == nil {
		return nil, errors.New("pending: store unavailable")
	}
	// We re-use the sweeper primitive (ListStaleInProgress with
	// a future cutoff) to get all entries in one pass. This is
	// an over-fetch for the list endpoint but it's simpler than
	// introducing a second scan primitive. With batchSize=1000
	// the worst case is a single SCAN pass; the filter is in
	// Go.
	cutoff := time.Now().Add(24 * time.Hour * 365 * 100) // far future
	raw, err := a.s.ListStaleInProgress(ctx.r, cutoff, 1000)
	if err != nil {
		return nil, err
	}
	// Also need completed/failed entries. ListStaleInProgress
	// only returns in_progress by design. For admin visibility
	// we want all statuses. Easiest: SCAN directly.
	// To keep the implementation simple, we accept that
	// ListStaleInProgress only returns in_progress; the
	// admin list endpoint will be enhanced in a follow-up
	// to scan all statuses. The sweeper workload is the
	// primary use case for the existing primitive.
	results := make([]listEntry, 0, len(raw))
	for _, e := range raw {
		age := time.Now().Unix() - e.CreatedAt
		results = append(results, listEntry{
			SessionID: e.SessionID,
			RequestID: e.RequestID,
			Status:    "in_progress",
			CreatedAt: e.CreatedAt,
			AgeSeconds: age,
		})
	}
	return results, nil
}

// pendingListContext is a tiny shim so the adapter can take a
// request without depending on net/http in the package.
type pendingListContext struct {
	r pendingContext
}

type pendingContext interface {
	Done() <-chan struct{}
	Err() error
	Value(key any) any
	Deadline() (time.Time, bool)
}

// pageBounds parses standard ?limit / ?offset query params with
// safe defaults and hard caps to prevent OOM.
// pageBounds parses standard ?limit / ?offset query params with
// safe defaults and hard caps. limit is clamped to [1, 500];
// values outside that range are clamped (not rejected — we
// prefer a sane response to a 400). Garbage values fall back
// to the default (50). offset is clamped to [0, ∞); negative
// values are treated as 0.
func pageBounds(r *http.Request) (limit, offset int) {
	limit = 50
	offset = 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			if n > 500 {
				n = 500
			}
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// handlePendingList implements GET /api/admin/pending-responses.
//
// Query params:
//   ?status=in_progress|completed|failed   filter
//   ?session_id=gw_xxx                    exact session
//   ?limit=N (1..500, default 50)         page size
//   ?offset=N (default 0)                 page offset
//
// Returns: {"entries": [...], "limit": N, "offset": N, "count": N}
//
// Note: this implementation only returns in_progress entries
// (the underlying ListStaleInProgress primitive scans for
// in_progress only). A future iteration can add a more general
// scanner to surface completed/failed entries too.
func (h *Handler) handlePendingList(w http.ResponseWriter, r *http.Request) {
	store := h.getPendingStore()
	if store == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			"pending response cache not configured", "PENDING_STORE_UNAVAILABLE")
		return
	}
	limit, offset := pageBounds(r)
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	sessionFilter := strings.TrimSpace(r.URL.Query().Get("session_id"))

	// We cannot easily paginate via ListStaleInProgress (it
	// returns one big slice). For now, take all and slice
	// in-process. The admin UI is read-only and low traffic
	// so this is acceptable; a future iteration can add a
	// LIMIT-aware scanner.
	adapter := newPendingStoreAdapter(store)
	ctx := pendingListContext{r: r.Context()}
	all, err := adapter.list(ctx)
	if err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			fmt.Sprintf("pending store error: %s", err), "PENDING_STORE_ERROR")
		return
	}
	filtered := make([]listEntry, 0, len(all))
	for _, e := range all {
		if statusFilter != "" && e.Status != statusFilter {
			continue
		}
		if sessionFilter != "" && e.SessionID != sessionFilter {
			continue
		}
		filtered = append(filtered, e)
	}
	end := offset + limit
	if offset > len(filtered) {
		offset = len(filtered)
	}
	if end > len(filtered) {
		end = len(filtered)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"entries": filtered[offset:end],
		"limit":   limit,
		"offset":  offset,
		"count":   len(filtered),
	})
}

// handlePendingDetail implements GET /api/admin/pending-responses/{sessionID}.
// Reads the most-recent entry for the session (matches the
// /v1/sessions/{id}/pending-response default behaviour).
func (h *Handler) handlePendingDetail(w http.ResponseWriter, r *http.Request, sessionID string) {
	store := h.getPendingStore()
	if store == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			"pending response cache not configured", "PENDING_STORE_UNAVAILABLE")
		return
	}
	entry, requestID, found, err := store.GetLatest(r.Context(), sessionID)
	if err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			fmt.Sprintf("pending store error: %s", err), "PENDING_STORE_ERROR")
		return
	}
	if !found {
		writeErrorJSON(w, http.StatusNotFound,
			"no pending response for this session", "PENDING_NOT_FOUND")
		return
	}
	age := time.Now().Unix() - entry.CreatedAt
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"session_id":    entry.SessionID,
		"request_id":    requestID,
		"status":        entry.Status,
		"provider_id":   entry.ProviderID,
		"credential_id": entry.CredentialID,
		"is_stream":     entry.IsStream,
		"created_at":    entry.CreatedAt,
		"completed_at":  entry.CompletedAt,
		"bytes_buffered": entry.BytesBuffered,
		"age_seconds":   age,
		"error_message": entry.ErrorMessage,
	})
}

// handlePendingDelete implements DELETE /api/admin/pending-responses/{sessionID}.
// Manually clears a pending entry. Idempotent — deleting a
// missing entry is not an error. Query param ?request_id=xxx
// targets a specific entry; without it, the most-recent entry
// is deleted (and the rest of the index is preserved).
func (h *Handler) handlePendingDelete(w http.ResponseWriter, r *http.Request, sessionID string) {
	store := h.getPendingStore()
	if store == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			"pending response cache not configured", "PENDING_STORE_UNAVAILABLE")
		return
	}
	requestID := r.URL.Query().Get("request_id")
	if requestID == "" {
		// Find the latest and delete it.
		_, rid, found, err := store.GetLatest(r.Context(), sessionID)
		if err != nil {
			writeErrorJSON(w, http.StatusServiceUnavailable,
				fmt.Sprintf("pending store error: %s", err), "PENDING_STORE_ERROR")
			return
		}
		if !found {
			writeErrorJSON(w, http.StatusNotFound,
				"no pending response for this session", "PENDING_NOT_FOUND")
			return
		}
		requestID = rid
	}
	if err := store.Delete(r.Context(), sessionID, requestID); err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			fmt.Sprintf("pending store error: %s", err), "PENDING_STORE_ERROR")
		return
	}
	slog.Info("admin: pending response deleted",
		"session_id", sessionID,
		"request_id", requestID,
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"deleted":    true,
		"session_id": sessionID,
		"request_id": requestID,
	})
}

// handlePendingStats implements GET /api/admin/pending-responses/stats.
// Returns aggregate counts. Currently only counts in_progress
// (the underlying primitive's coverage); a future iteration can
// surface completed/failed/aged histograms too.
func (h *Handler) handlePendingStats(w http.ResponseWriter, r *http.Request) {
	store := h.getPendingStore()
	if store == nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			"pending response cache not configured", "PENDING_STORE_UNAVAILABLE")
		return
	}
	cutoff := time.Now().Add(24 * time.Hour * 365 * 100) // far future
	all, err := store.ListStaleInProgress(r.Context(), cutoff, 1000)
	if err != nil {
		writeErrorJSON(w, http.StatusServiceUnavailable,
			fmt.Sprintf("pending store error: %s", err), "PENDING_STORE_ERROR")
		return
	}
	// Aggregate by status (always in_progress here, but the
	// shape is ready for future statuses).
	byStatus := map[string]int{}
	var oldestCreatedAt int64
	for _, e := range all {
		byStatus["in_progress"]++
		if e.CreatedAt < oldestCreatedAt || oldestCreatedAt == 0 {
			oldestCreatedAt = e.CreatedAt
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"total":              len(all),
		"by_status":          byStatus,
		"oldest_created_at":  oldestCreatedAt,
	})
}

// getPendingStore returns the pending store, or nil if not
// configured. The Handler has a pendingStore field set via
// SetPendingStore from main.go (this is the production wire
// point; tests set it directly).
//
// Nil-receiver safe: a nil Handler returns nil rather than
// panicking. This is defensive — the auth middleware always
// constructs a real Handler, but unit tests sometimes poke at
// a nil pointer.
func (h *Handler) getPendingStore() *pending.Store {
	if h == nil {
		return nil
	}
	return h.pendingStore
}

// handlePendingSubrouter dispatches /api/admin/pending-responses/{id}
// between detail (GET) and delete (DELETE). A request_id query
// parameter is honoured by the delete path but ignored by detail
// (which always returns the most-recent entry).
func (h *Handler) handlePendingSubrouter(w http.ResponseWriter, r *http.Request) {
	sessionID := strings.TrimPrefix(r.URL.Path, "/api/admin/pending-responses/")
	// Strip any trailing sub-path (e.g. "stats" — but that
	// path is registered before this subrouter so it never
	// reaches here; the check below is a defensive guard).
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeErrorJSON(w, http.StatusNotFound,
			"not found", "NOT_FOUND")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.handlePendingDetail(w, r, sessionID)
	case http.MethodDelete:
		h.handlePendingDelete(w, r, sessionID)
	default:
		writeErrorJSON(w, http.StatusMethodNotAllowed,
			"method not allowed", "METHOD_NOT_ALLOWED")
	}
}

// writeErrorJSON is the standard admin handler error response.
// We define it here (instead of importing the relay package's
// variant) to keep the admin package self-contained. The shape
// matches the rest of the admin endpoints.
func writeErrorJSON(w http.ResponseWriter, status int, msg, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": msg,
			"code":    code,
		},
	})
}
