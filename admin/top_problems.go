// Package admin — top-problems report (T4 of the 154-server audit plan).
//
// Background
// T1's audit surfaced a "Top problem credentials / models" list. This file
// implements the corresponding read-only admin endpoint so operators can
// pull that list on demand without re-running the offline audit script.
//
// Endpoints:
//   GET /api/logs/top-problems?from=<rfc3339>&to=<rfc3339>&limit=<n>
//   → {
//        "items": [
//          {
//            "kind": "credential"|"model",
//            "id": <int or string>,
//            "label": <string>,
//            "request_count": <int>,
//            "failure_count": <int>,
//            "failure_rate": <float in [0,1]>,
//            "top_failure_kind": <error_kind string or "">,
//            "top_failure_detail": <string or "">
//          },
//          ...
//        ],
//        "errors": [ ...optional, only when one dimension failed... ]
//      }
//
// The endpoint returns the union of the top-N worst credentials AND top-N
// worst models, ranked by absolute failure_count (then failure_rate as
// tiebreak). limit applies per dimension (N credentials + N models). Use
// `limit=5` for a quick check, `limit=20` for the full audit view.
//
// Safe-by-construction behaviour:
//   - Read-only. No writes to any DB table.
//   - Time-bounded. Hard 10-second query timeout via context.WithTimeout.
//   - Limited. Hard cap limit=200 to keep JSON responses bounded.
//   - Tenant-scoped. Joins via `request_logs_with_current_month` which is
//     already tenant-aware (admin auth middleware enforces tenant scope).
//   - Partial-result tolerant. If one dimension's query fails, the
//     successful dimension's results still ship with an `errors` array.
//
// Failure-mode contract:
//   - DB unavailable → 503 with a JSON error body.
//   - Bad query params → 400 with field-specific message.
//   - One dimension query fails → 200 with partial items + errors array.
//   - Empty result → 200 with `{"items": []}`.
package admin

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// topProblemsItem is one row in the top-problems report.
type topProblemsItem struct {
	Kind             string  `json:"kind"`                // "credential" or "model"
	ID               any     `json:"id"`                  // int (credential_id) or string (client_model)
	Label            string  `json:"label"`              // human-readable label
	RequestCount     int64   `json:"request_count"`
	FailureCount     int64   `json:"failure_count"`
	FailureRate      float64 `json:"failure_rate"`       // 0..1
	TopFailureKind   string  `json:"top_failure_kind"`   // empty when no failures
	TopFailureDetail string  `json:"top_failure_detail"` // empty when no failures
}

const (
	topProblemsQueryTimeout  = 10 * time.Second
	topProblemsMaxLimit     = 200
	// topProblemsMinRequests is the HAVING floor — the same floor T1's audit
	// used to avoid ranking items whose failure rate has no statistical
	// meaning (≤30 observations).
	topProblemsMinRequests  = 30
)

// handleTopProblems serves GET /api/logs/top-problems.
//
// Query params:
//   - from (rfc3339 or YYYY-MM-DD HH:MM:SS, default: 24h ago)
//   - to   (rfc3339 or YYYY-MM-DD HH:MM:SS, default: now)
//   - limit (1..200, default: 20)
//   - kind (optional filter: "credential" | "model", default: both)
func (h *Handler) handleTopProblems(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), topProblemsQueryTimeout)
	defer cancel()

	now := time.Now().UTC()
	start, ok := parseQueryTimeStrict(r, "from", now.Add(-24*time.Hour))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid 'from' timestamp")
		return
	}
	end, ok := parseQueryTimeStrict(r, "to", now)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid 'to' timestamp")
		return
	}
	if !end.After(start) {
		writeError(w, http.StatusBadRequest, "to must be after from")
		return
	}
	limit := queryInt(r, "limit", 20)
	if limit < 1 {
		limit = 1
	}
	if limit > topProblemsMaxLimit {
		limit = topProblemsMaxLimit
	}
	kindFilter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("kind")))

	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "admin database not configured")
		return
	}

	var (
		items []topProblemsItem
		errs  []string
	)
	if kindFilter == "" || kindFilter == "credential" {
		creds, err := queryTopProblemCredentials(ctx, h.db, start, end, limit)
		if err != nil {
			slog.WarnContext(ctx, "top-problems: credential query failed", "error", err)
			errs = append(errs, "credential: "+err.Error())
		} else {
			items = append(items, creds...)
		}
	}
	if kindFilter == "" || kindFilter == "model" {
		models, err := queryTopProblemModels(ctx, h.db, start, end, limit)
		if err != nil {
			slog.WarnContext(ctx, "top-problems: model query failed", "error", err)
			errs = append(errs, "model: "+err.Error())
		} else {
			items = append(items, models...)
		}
	}

	// Rank: failures DESC, then failure_rate DESC, then request_count DESC.
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].FailureCount != items[j].FailureCount {
			return items[i].FailureCount > items[j].FailureCount
		}
		if items[i].FailureRate != items[j].FailureRate {
			return items[i].FailureRate > items[j].FailureRate
		}
		return items[i].RequestCount > items[j].RequestCount
	})

	if items == nil {
		items = []topProblemsItem{}
	}
	resp := map[string]any{
		"items": items,
		"meta": map[string]any{
			"from":  start.Format(time.RFC3339),
			"to":    end.Format(time.RFC3339),
			"limit": limit,
			"count": len(items),
		},
	}
	if len(errs) > 0 {
		resp["errors"] = errs
	}
	writeJSON(w, http.StatusOK, resp)
}

// queryTopProblemCredentials ranks credentials by total failure_count over
// the given window. Filters to credentials with at least
// topProblemsMinRequests requests so the failure rate is statistically
// meaningful (matches T1 audit's HAVING clause).
func queryTopProblemCredentials(ctx context.Context, db *pgxpool.Pool, start, end time.Time, limit int) ([]topProblemsItem, error) {
	const sql = `
SELECT
  rl.credential_id,
  COALESCE(c.alias, '') AS label,
  COUNT(*) AS request_count,
  COUNT(*) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE) AS failure_count,
  CASE WHEN COUNT(*) > 0
       THEN (COUNT(*) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE))::float8 / COUNT(*)::float8
       ELSE 0
  END AS failure_rate,
  COALESCE(MODE() WITHIN GROUP (ORDER BY rl.error_kind) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE), '') AS top_failure_kind,
  COALESCE(MODE() WITHIN GROUP (ORDER BY rl.failure_detail_code) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE), '') AS top_failure_detail
FROM request_logs_with_current_month rl
LEFT JOIN credentials c ON c.id = rl.credential_id
WHERE rl.ts >= $1 AND rl.ts <= $2
  AND rl.credential_id IS NOT NULL
GROUP BY rl.credential_id, c.alias
HAVING COUNT(*) >= $4
ORDER BY failure_count DESC,
         (COUNT(*) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE))::float8 / COUNT(*) DESC
LIMIT $3
`
	rows, err := db.Query(ctx, sql, start, end, limit, topProblemsMinRequests)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]topProblemsItem, 0, limit)
	for rows.Next() {
		var item topProblemsItem
		var id int
		if err := rows.Scan(&id, &item.Label, &item.RequestCount, &item.FailureCount, &item.FailureRate, &item.TopFailureKind, &item.TopFailureDetail); err != nil {
			return nil, err
		}
		item.Kind = "credential"
		item.ID = id
		items = append(items, item)
	}
	return items, rows.Err()
}

// queryTopProblemModels ranks client_model values by total failure_count
// over the given window. Same HAVING floor as the credential query for
// statistical-meaningfulness parity.
func queryTopProblemModels(ctx context.Context, db *pgxpool.Pool, start, end time.Time, limit int) ([]topProblemsItem, error) {
	const sql = `
SELECT
  rl.client_model,
  COUNT(*) AS request_count,
  COUNT(*) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE) AS failure_count,
  CASE WHEN COUNT(*) > 0
       THEN (COUNT(*) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE))::float8 / COUNT(*)::float8
       ELSE 0
  END AS failure_rate,
  COALESCE(MODE() WITHIN GROUP (ORDER BY rl.error_kind) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE), '') AS top_failure_kind,
  COALESCE(MODE() WITHIN GROUP (ORDER BY rl.failure_detail_code) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE), '') AS top_failure_detail
FROM request_logs_with_current_month rl
WHERE rl.ts >= $1 AND rl.ts <= $2
  AND rl.client_model IS NOT NULL AND rl.client_model != ''
GROUP BY rl.client_model
HAVING COUNT(*) >= $4
ORDER BY failure_count DESC,
         (COUNT(*) FILTER (WHERE COALESCE(rl.success, FALSE) = FALSE))::float8 / COUNT(*) DESC
LIMIT $3
`
	rows, err := db.Query(ctx, sql, start, end, limit, topProblemsMinRequests)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]topProblemsItem, 0, limit)
	for rows.Next() {
		var item topProblemsItem
		if err := rows.Scan(&item.Label, &item.RequestCount, &item.FailureCount, &item.FailureRate, &item.TopFailureKind, &item.TopFailureDetail); err != nil {
			return nil, err
		}
		item.Kind = "model"
		item.ID = item.Label
		items = append(items, item)
	}
	return items, rows.Err()
}