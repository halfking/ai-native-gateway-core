// Package logsearch exposes the Bleve-backed log full-text search
// endpoint as an admin sub-handler. It is wired into admin.Handler
// from admin/log_management.go so the route lives alongside the
// other /api/admin/logs/* endpoints.
//
// Phase A (2026-08-26): added Bleve full-text search on top of the
// existing lumberjack JSON pipeline. The endpoint is intentionally
// read-only; the index is written to by the BleveFanoutHandler in
// internal/logging (see bleve_fanout.go). When the indexer is
// disabled (LLM_GATEWAY_LOG_BLEVE_ENABLED!=true) the endpoint
// returns an empty envelope with enabled=false so the admin UI can
// degrade gracefully.
package logsearch

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/internal/logging"
)

// Response is the JSON envelope returned by GET /api/admin/logs/search.
// Mirrors logging.SearchEnvelope but is duplicated here to avoid an
// admin→internal reverse-import direction; both shapes stay in sync
// via the SearchHit doc.
type Response struct {
	Enabled bool                   `json:"enabled"`
	Total   uint64                 `json:"total"`
	TookMs  int64                  `json:"took_ms"`
	Hits    []logging.SearchHit    `json:"hits"`
	Stats   map[string]interface{} `json:"stats,omitempty"`
}

// HandleSearch is the http.Handler registered by admin.Handler.
//
// GET /api/admin/logs/search
//
// Query parameters:
//
//	q       — substring matched against msg
//	tenant  — exact-match term against tenant_id
//	level   — exact-match term against level (info|warn|error|debug)
//	from    — RFC3339 timestamp (inclusive)
//	to      — RFC3339 timestamp (inclusive)
//	regex   — when non-empty, applied as a regexp constraint on top of q
//	fuzzy   — when truthy (1/true/yes), use Bleve's fuzzy match for q
//	page    — 1-based page number (default 1)
//	size    — hits per page (default 50, max 200)
func HandleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	spec := logging.SearchSpec{
		Query:    strings.TrimSpace(q.Get("q")),
		TenantID: strings.TrimSpace(q.Get("tenant")),
		Level:    strings.TrimSpace(q.Get("level")),
		Regex:    strings.TrimSpace(q.Get("regex")),
	}
	if v := q.Get("from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			spec.From = &t
		}
	}
	if v := q.Get("to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			spec.To = &t
		}
	}
	spec.Fuzzy = isTruthy(q.Get("fuzzy"))
	if v := q.Get("page"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			spec.Page = n
		}
	}
	if v := q.Get("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			spec.Size = n
		}
	}

	idx := logging.CurrentBleveIndexer()
	if idx == nil {
		writeJSON(w, http.StatusOK, Response{
			Enabled: false,
			Stats: map[string]interface{}{
				"reason":  "fan-out disabled (set LLM_GATEWAY_LOG_BLEVE_ENABLED=true)",
			},
		})
		return
	}

	env, err := idx.Search(spec)
	if err != nil {
		http.Error(w, "bleve search error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusOK, Response{
		Enabled: true,
		Total:   env.Total,
		TookMs:  env.TookMs,
		Hits:    env.Hits,
		Stats:   idx.Stats(),
	})
}

// HandleStatus reports fan-out indexer counters so the admin UI can
// show queue depth, dropped records, and average batch latency.
//
// GET /api/admin/logs/search/status
func HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	idx := logging.CurrentBleveIndexer()
	if idx == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"enabled": false})
		return
	}
	writeJSON(w, http.StatusOK, idx.Stats())
}

func isTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, body interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
