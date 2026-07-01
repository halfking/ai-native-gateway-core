package admin

// auto_route_correlations.go — P7.2: admin endpoint for auto-route
// correlation analysis.
//
// Reads the promoted columns on request_logs (task_type_chosen,
// confidence_num, model_chosen, strategy_used) to answer questions
// like:
//   - For a given (task_type, strategy), which models had the
//     best success rate in the last 7 days?
//   - For a given model, what's the success rate by task type?
//   - Is there a model that performs poorly on reasoning tasks
//     but well on chat? (would be a candidate to blacklist
//     via routing overrides)
//
// Endpoint:
//
//	GET /api/admin/auto-route/correlations?days=7
//	    ?by=strategy|model|task_type (default: model)
//	    ?min_samples=20 (default: 20)
//
// Returns 5 correlation tables:
//   - byModel:  per-model success/latency/cost
//   - byStrategy: per-strategy success/latency/cost
//   - byTaskType: per-task_type success/latency/cost
//   - byModelTask: per-(model, task_type) detail (catches outliers)
//   - verdict: top-3 models by composite score per task type
//
// NOTE: All SELECTs on request_logs in this file are wrapped with
// tenantLogsClause() (admin/session_tenant.go) — adds
// "AND tenant_id = $N" for tenant_admin callers on non-default tenants.
// Super-admin / legacy admin_key / default-tenant callers see all rows.

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AutoRouteCorrelationsResponse is the JSON wire format returned by
// the correlations endpoint. Fields are stable for the admin UI.
type AutoRouteCorrelationsResponse struct {
	WindowDays  int                  `json:"window_days"`
	ByModel     []CorrelationRow     `json:"by_model"`
	ByStrategy  []CorrelationRow     `json:"by_strategy"`
	ByTaskType  []CorrelationRow     `json:"by_task_type"`
	ByModelTask []CorrelationRowMT   `json:"by_model_task"`
	Verdict     []CorrelationVerdict `json:"verdict"`
	GeneratedAt string               `json:"generated_at"`
}

// CorrelationRow is a generic (label, samples, success, latency,
// cost, quality) row.
type CorrelationRow struct {
	Label      string  `json:"label"`
	Samples    int     `json:"samples"`
	Success    float64 `json:"success_rate"`
	AvgLatency int     `json:"avg_latency_ms"`
	AvgCost    float64 `json:"avg_cost_usd"`
	Quality    float64 `json:"avg_quality"` // optional: present when tuning_signals is joined
}

// CorrelationRowMT extends CorrelationRow with task_type for the
// per-(model, task_type) breakdown.
type CorrelationRowMT struct {
	Model      string  `json:"model"`
	TaskType   string  `json:"task_type"`
	Samples    int     `json:"samples"`
	Success    float64 `json:"success_rate"`
	AvgLatency int     `json:"avg_latency_ms"`
	AvgCost    float64 `json:"avg_cost_usd"`
}

// CorrelationVerdict is the top-N models per task type (ranked by
// success rate, ties broken by latency).
type CorrelationVerdict struct {
	TaskType   string  `json:"task_type"`
	Model      string  `json:"model"`
	Success    float64 `json:"success_rate"`
	AvgLatency int     `json:"avg_latency_ms"`
	Rank       int     `json:"rank"`
}

// handleAutoRouteCorrelations: GET /auto-route/correlations
func (h *AutoRouteHandlers) handleAutoRouteCorrelations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONErrCtx(w, r, http.StatusMethodNotAllowed, "admin_method_not_allowed")
		return
	}

	days := 7
	if d := r.URL.Query().Get("days"); d != "" {
		v, err := strconv.Atoi(d)
		if err != nil || v <= 0 || v > 90 {
			writeJSONErrCtx(w, r, http.StatusBadRequest, "admin_days_out_of_range")
			return
		}
		days = v
	}
	minSamples := 20
	if m := r.URL.Query().Get("min_samples"); m != "" {
		v, err := strconv.Atoi(m)
		if err != nil || v < 1 || v > 1000 {
			writeJSONErr(w, http.StatusBadRequest, "min_samples must be 1-1000")
			return
		}
		minSamples = v
	}

	resp := AutoRouteCorrelationsResponse{
		WindowDays:  days,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
	}

	// ── By model ─────────────────────────────────────────────
	byModelQuery := `
		SELECT
		    model_chosen,
		    COUNT(*) AS samples,
		    AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) AS success,
		    COALESCE(AVG(latency_ms), 0)::int AS avg_latency,
		    COALESCE(AVG(cost_usd), 0) AS avg_cost
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND model_chosen IS NOT NULL
		  AND ts >= NOW() - INTERVAL '1 day' * $1
		GROUP BY model_chosen
		HAVING COUNT(*) >= $2
		ORDER BY success DESC, avg_latency ASC
	`
	byModelQ, byModelArgs := applyTenantFilterToQuery(r, byModelQuery, []any{days, minSamples})
	if err := h.queryCorrelations(w, r, byModelQ, byModelArgs, &resp.ByModel, "model_chosen"); err != nil {
		return
	}

	// ── By strategy ───────────────────────────────────────────
	byStrategyQuery := `
		SELECT
		    strategy_used,
		    COUNT(*) AS samples,
		    AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) AS success,
		    COALESCE(AVG(latency_ms), 0)::int AS avg_latency,
		    COALESCE(AVG(cost_usd), 0) AS avg_cost
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND strategy_used IS NOT NULL
		  AND ts >= NOW() - INTERVAL '1 day' * $1
		GROUP BY strategy_used
		HAVING COUNT(*) >= $2
		ORDER BY success DESC, avg_latency ASC
	`
	byStrategyQ, byStrategyArgs := applyTenantFilterToQuery(r, byStrategyQuery, []any{days, minSamples})
	if err := h.queryCorrelations(w, r, byStrategyQ, byStrategyArgs, &resp.ByStrategy, "strategy_used"); err != nil {
		return
	}

	// ── By task type ──────────────────────────────────────────
	byTaskTypeQuery := `
		SELECT
		    task_type_chosen,
		    COUNT(*) AS samples,
		    AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) AS success,
		    COALESCE(AVG(latency_ms), 0)::int AS avg_latency,
		    COALESCE(AVG(cost_usd), 0) AS avg_cost
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND task_type_chosen IS NOT NULL
		  AND ts >= NOW() - INTERVAL '1 day' * $1
		GROUP BY task_type_chosen
		HAVING COUNT(*) >= $2
		ORDER BY samples DESC
	`
	byTaskTypeQ, byTaskTypeArgs := applyTenantFilterToQuery(r, byTaskTypeQuery, []any{days, minSamples})
	if err := h.queryCorrelations(w, r, byTaskTypeQ, byTaskTypeArgs, &resp.ByTaskType, "task_type_chosen"); err != nil {
		return
	}

	// ── Per-(model, task_type) — the outlier detector ─────────
	byModelTaskQuery := `
		SELECT
		    model_chosen,
		    task_type_chosen,
		    COUNT(*) AS samples,
		    AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) AS success,
		    COALESCE(AVG(latency_ms), 0)::int AS avg_latency,
		    COALESCE(AVG(cost_usd), 0) AS avg_cost
		FROM request_logs
		WHERE is_auto_request = TRUE
		  AND model_chosen IS NOT NULL
		  AND task_type_chosen IS NOT NULL
		  AND ts >= NOW() - INTERVAL '1 day' * $1
		GROUP BY model_chosen, task_type_chosen
		HAVING COUNT(*) >= $2
		ORDER BY success DESC, model_chosen, task_type_chosen
	`
	byModelTaskQ, byModelTaskArgs := applyTenantFilterToQuery(r, byModelTaskQuery, []any{days, minSamples})
	rows, err := h.db.Query(r.Context(), byModelTaskQ, byModelTaskArgs...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var r CorrelationRowMT
		if err := rows.Scan(&r.Model, &r.TaskType, &r.Samples, &r.Success,
			&r.AvgLatency, &r.AvgCost); err != nil {
			writeInternalErr(w, err)
			return
		}
		resp.ByModelTask = append(resp.ByModelTask, r)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	// ── Verdict: top-3 models per task type ────────────────────
	verdictQuery := `
		SELECT
		    task_type_chosen,
		    model_chosen,
		    AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) AS success,
		    COALESCE(AVG(latency_ms), 0)::int AS avg_latency
		FROM (
			SELECT
			    task_type_chosen, model_chosen, success, latency_ms,
			    ROW_NUMBER() OVER (
			        PARTITION BY task_type_chosen
			        ORDER BY AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END) DESC, latency_ms ASC
			    ) AS rn
			FROM request_logs
			WHERE is_auto_request = TRUE
			  AND ts >= NOW() - INTERVAL '1 day' * $1
			GROUP BY task_type_chosen, model_chosen
			HAVING COUNT(*) >= $2
		) ranked
		WHERE rn <= 3
		ORDER BY task_type_chosen, rn
	`
	verdictQ, verdictArgs := applyTenantFilterToQuery(r, verdictQuery, []any{days, minSamples})
	rows2, err := h.db.Query(r.Context(), verdictQ, verdictArgs...)
	if err != nil {
		writeInternalErr(w, err)
		return
	}
	defer rows2.Close()
	for rows2.Next() {
		var v CorrelationVerdict
		if err := rows2.Scan(&v.TaskType, &v.Model, &v.Success, &v.AvgLatency, &v.Rank); err != nil {
			writeInternalErr(w, err)
			return
		}
		resp.Verdict = append(resp.Verdict, v)
	}
	if err := rows2.Err(); err != nil {
		writeInternalErr(w, err)
		return
	}

	writeJSON(w, http.StatusOK, resp)
	slog.Debug("auto-route correlations computed",
		"days", days, "by_model", len(resp.ByModel),
		"by_strategy", len(resp.ByStrategy),
		"by_task_type", len(resp.ByTaskType),
		"by_model_task", len(resp.ByModelTask),
		"verdict", len(resp.Verdict))
}

// queryCorrelations is a small helper for the three identical-shape
// queries (byModel, byStrategy, byTaskType). It runs the query,
// scans the result, and appends to the destination slice.
//
// labelCol is the column name used as the row's Label.
func (h *AutoRouteHandlers) queryCorrelations(
	w http.ResponseWriter, r *http.Request,
	query string, args []any,
	dest *[]CorrelationRow, labelCol string,
) error {
	rows, err := h.db.Query(r.Context(), query, args...)
	if err != nil {
		writeInternalErr(w, err)
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var row CorrelationRow
		var label *string
		if err := rows.Scan(&label, &row.Samples, &row.Success,
			&row.AvgLatency, &row.AvgCost); err != nil {
			writeInternalErr(w, err)
			return err
		}
		if label != nil {
			row.Label = *label
		}
		*dest = append(*dest, row)
	}
	if err := rows.Err(); err != nil {
		writeInternalErr(w, err)
		return err
	}
	return nil
}

// RegisterAutoRouteCorrelationsRoute mounts the endpoint. We expose
// a separate Register function (vs the inline call in
// RegisterAutoRouteRoutes) so the new endpoint is easy to find.
func (h *AutoRouteHandlers) RegisterAutoRouteCorrelationsRoute(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("/api/admin/auto-route/correlations", adminWrap(h.handleAutoRouteCorrelations))
}

// applyTenantFilterToQuery injects "AND tenant_id = $N" (via
// tenantLogsClause) into the query just before its first "GROUP BY"
// marker, appending the tenant id to args. The first existing placeholder
// must be $1 — caller passes the existing positional args. If the caller
// is not a tenant admin on a non-default tenant, the SQL and args are
// returned unchanged (no-op).
//
// We use the "GROUP BY" anchor because every query in this file follows
// the shape: WHERE ... AND ts >= ... GROUP BY ... HAVING ... — so the
// tenant filter belongs between the WHERE and the GROUP BY.
func applyTenantFilterToQuery(r *http.Request, query string, args []any) (string, []any) {
	tenantFrag, tenantArgs, _ := tenantLogsClause(r, len(args)+1)
	if tenantFrag == "" {
		return query, args
	}
	if strings.Contains(query, "GROUP BY") {
		query = strings.Replace(query, "GROUP BY", tenantFrag+"\n\t\tGROUP BY", 1)
	} else {
		// fallback: append at end (still valid SQL — filter applies to all rows)
		query = query + tenantFrag
	}
	args = append(args, tenantArgs...)
	return query, args
}
