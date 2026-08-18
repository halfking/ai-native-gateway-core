package admin

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleStats exposes the canonical body-free statistics projections. It is
// intentionally an adapter endpoint: existing dashboard/usage endpoints keep
// their response contracts while consumers migrate to this shared source.
func (h *Handler) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	kind := strings.TrimPrefix(r.URL.Path, "/api/admin/stats/")
	if kind == "" || kind == r.URL.Path {
		kind = "summary"
	}
	start, end, err := statsRange(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tenant := statsTenantScope(r)
	switch kind {
	case "summary":
		h.handleStatsSummary(w, r, start, end, tenant)
	case "trend":
		h.handleStatsTrend(w, r, start, end, tenant)
	case "monthly":
		h.handleStatsMonthly(w, r, start, end, tenant)
	case "breakdown":
		h.handleStatsBreakdown(w, r, start, end, tenant)
	case "errors":
		h.handleStatsErrors(w, r, start, end, tenant)
	case "reconciliation":
		h.handleStatsReconciliation(w, r)
	case "reconciliation/approve":
		h.handleStatsReconciliationApprove(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown stats resource")
	}
}

func statsRange(r *http.Request) (time.Time, time.Time, error) {
	now := time.Now().UTC()
	end := now
	start := now.Add(-24 * time.Hour)
	if raw := r.URL.Query().Get("start"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		start = parsed.UTC()
	}
	if raw := r.URL.Query().Get("end"); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
		end = parsed.UTC()
	}
	if days, _ := strconv.Atoi(r.URL.Query().Get("days")); days > 0 {
		start = end.Add(-time.Duration(days) * 24 * time.Hour)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("end must be after start")
	}
	if end.Sub(start) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("stats window cannot exceed 366 days")
	}
	return start, end, nil
}

func statsTenantScope(r *http.Request) string {
	auth := GetAuthContext(r)
	requested := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if auth == nil {
		return requested
	}
	if auth.Role != "super_admin" && auth.Role != "admin_key" {
		return auth.TenantID
	}
	return requested
}

func (h *Handler) handleStatsSummary(w http.ResponseWriter, r *http.Request, start, end time.Time, tenant string) {
	where, args := statsWhere(start, end, tenant, r)
	where += " AND dimension_type = 'provider_model'"
	var requests, success, failures, timeouts, limited, prompt, completion, total, credits, latencyCount, latencySum int64
	var cost float64
	err := h.db.QueryRow(r.Context(), `
		SELECT COALESCE(SUM(request_count),0), COALESCE(SUM(success_count),0),
		       COALESCE(SUM(failure_count),0), COALESCE(SUM(timeout_count),0),
		       COALESCE(SUM(rate_limited_count),0), COALESCE(SUM(prompt_tokens),0),
		       COALESCE(SUM(completion_tokens),0), COALESCE(SUM(total_tokens),0),
		       COALESCE(SUM(credits_charged),0), COALESCE(SUM(cost_usd),0),
		       COALESCE(SUM(latency_count),0), COALESCE(SUM(latency_sum_ms),0)
		FROM stats_usage_daily WHERE `+where, args...).Scan(
		&requests, &success, &failures, &timeouts, &limited, &prompt, &completion,
		&total, &credits, &cost, &latencyCount, &latencySum)
	if err != nil {
		if isMissingStatsRelation(err) {
			writeJSON(w, http.StatusOK, map[string]any{"degraded": true, "error_code": "STATS_NOT_MIGRATED", "summary": map[string]any{}})
			return
		}
		writeError(w, http.StatusInternalServerError, "stats summary query failed")
		return
	}
	avg := float64(0)
	if latencyCount > 0 {
		avg = float64(latencySum) / float64(latencyCount)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": "stats_usage_daily", "as_of": time.Now().UTC().Format(time.RFC3339),
		"summary": map[string]any{"requests": requests, "success": success, "failures": failures, "timeouts": timeouts, "rate_limited": limited, "prompt_tokens": prompt, "completion_tokens": completion, "total_tokens": total, "credits_charged": credits, "cost_usd": cost, "avg_latency_ms": avg},
	})
}

func (h *Handler) handleStatsTrend(w http.ResponseWriter, r *http.Request, start, end time.Time, tenant string) {
	where, args := statsWhere(start, end, tenant, r)
	where += " AND dimension_type = 'provider_model'"
	rows, err := h.db.Query(r.Context(), `SELECT day_utc, COALESCE(SUM(request_count),0), COALESCE(SUM(success_count),0), COALESCE(SUM(failure_count),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0), COALESCE(SUM(credits_charged),0) FROM stats_usage_daily WHERE `+where+` GROUP BY day_utc ORDER BY day_utc`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats trend query failed")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var day time.Time
		var req, ok, fail, tokens, credits int64
		var cost float64
		if rows.Scan(&day, &req, &ok, &fail, &tokens, &cost, &credits) == nil {
			items = append(items, map[string]any{"bucket": day.Format("2006-01-02"), "requests": req, "success": ok, "failures": fail, "total_tokens": tokens, "cost_usd": cost, "credits_charged": credits})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": "stats_usage_daily", "items": items})
}

func (h *Handler) handleStatsMonthly(w http.ResponseWriter, r *http.Request, start, end time.Time, tenant string) {
	where, args := statsWhere(start, end, tenant, r)
	rows, err := h.db.Query(r.Context(), `SELECT month_start, status, tenant_id, provider_id, credential_id, canonical_id, raw_model_name, dimension_type, dimension_key, traffic_class, request_count, success_count, failure_count, total_tokens, credits_charged, cost_usd, row_checksum, closed_at FROM stats_usage_monthly WHERE `+where+` ORDER BY month_start DESC, request_count DESC LIMIT 500`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats monthly query failed")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var month time.Time
		var status, tenantID, model, dimensionType, dimensionKey, trafficClass, checksum string
		var provider, credential, canonical, req, ok, fail, tokens, credits int64
		var cost float64
		var closed sql.NullTime
		if rows.Scan(&month, &status, &tenantID, &provider, &credential, &canonical, &model, &dimensionType, &dimensionKey, &trafficClass, &req, &ok, &fail, &tokens, &credits, &cost, &checksum, &closed) == nil {
			item := map[string]any{"month": month.Format("2006-01"), "status": status, "tenant_id": tenantID, "provider_id": provider, "credential_id": credential, "canonical_id": canonical, "model": model, "dimension_type": dimensionType, "dimension_key": dimensionKey, "traffic_class": trafficClass, "requests": req, "success": ok, "failures": fail, "total_tokens": tokens, "credits_charged": credits, "cost_usd": cost, "checksum": checksum}
			if closed.Valid {
				item["closed_at"] = closed.Time.UTC().Format(time.RFC3339)
			}
			items = append(items, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": "stats_usage_monthly", "items": items})
}

func (h *Handler) handleStatsBreakdown(w http.ResponseWriter, r *http.Request, start, end time.Time, tenant string) {
	dimension := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("dimension")))
	columns := map[string]string{
		"tenant": "tenant_id", "provider": "provider_id::text", "credential": "credential_id::text",
		"model": "COALESCE(NULLIF(raw_model_name, ''), canonical_id::text)", "person": "dimension_key",
	}
	column, ok := columns[dimension]
	if !ok {
		dimension, column = "model", columns["model"]
	}
	where, args := statsWhere(start, end, tenant, r)
	dimensionTypes := map[string]string{"tenant": "tenant", "provider": "provider", "credential": "credential", "model": "provider_model", "person": "person"}
	where += " AND dimension_type = '" + dimensionTypes[dimension] + "'"
	limit := 25
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	rows, err := h.db.Query(r.Context(), `SELECT `+column+`, COALESCE(SUM(request_count),0), COALESCE(SUM(success_count),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0), COALESCE(SUM(credits_charged),0) FROM stats_usage_daily WHERE `+where+` GROUP BY 1 ORDER BY SUM(request_count) DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats breakdown query failed")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var key string
		var requests, success, tokens, credits int64
		var cost float64
		if rows.Scan(&key, &requests, &success, &tokens, &cost, &credits) == nil {
			items = append(items, map[string]any{"key": key, "requests": requests, "success": success, "total_tokens": tokens, "cost_usd": cost, "credits_charged": credits})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": "stats_usage_daily", "dimension": dimension, "items": items})
}

func (h *Handler) handleStatsErrors(w http.ResponseWriter, r *http.Request, start, end time.Time, tenant string) {
	where, args := statsWhere(start, end, tenant, r)
	where += " AND dimension_type = 'error' AND failure_count > 0"
	rows, err := h.db.Query(r.Context(), `SELECT dimension_key, COALESCE(SUM(failure_count),0), COALESCE(SUM(request_count),0), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0) FROM stats_usage_daily WHERE `+where+` GROUP BY dimension_key ORDER BY SUM(failure_count) DESC LIMIT 100`, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats errors query failed")
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var key string
		var failures, requests, tokens int64
		var cost float64
		if rows.Scan(&key, &failures, &requests, &tokens, &cost) == nil {
			items = append(items, map[string]any{"error": key, "failures": failures, "requests": requests, "total_tokens": tokens, "cost_usd": cost})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": "stats_usage_daily", "items": items})
}

func (h *Handler) handleStatsReconciliation(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	where := "1=1"
	args := []any{}
	auth := GetAuthContext(r)
	if auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
		where += " AND EXISTS (SELECT 1 FROM stats_reconciliation_diffs d_scope WHERE d_scope.run_id = stats_reconciliation_runs.run_id AND d_scope.tenant_id = $" + strconv.Itoa(len(args)+1) + ")"
		args = append(args, auth.TenantID)
	}
	if runID := strings.TrimSpace(r.URL.Query().Get("run_id")); runID != "" {
		where += " AND run_id = $" + strconv.Itoa(len(args)+1)
		args = append(args, runID)
	}
	runs, err := h.db.Query(r.Context(), `SELECT run_id, period_start, period_end, scope, status, events_seen, rows_compared, rows_repaired, diff_count, source_watermark, started_at, finished_at FROM stats_reconciliation_runs WHERE `+where+` ORDER BY started_at DESC LIMIT `+strconv.Itoa(limit), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "stats reconciliation query failed")
		return
	}
	defer runs.Close()
	runItems := make([]map[string]any, 0)
	for runs.Next() {
		var id, scope, status string
		var start, end, started time.Time
		var finished, watermark sql.NullTime
		var seen, compared, repaired, diffs int64
		if runs.Scan(&id, &start, &end, &scope, &status, &seen, &compared, &repaired, &diffs, &watermark, &started, &finished) == nil {
			runItems = append(runItems, map[string]any{"run_id": id, "period_start": start, "period_end": end, "scope": scope, "status": status, "events_seen": seen, "rows_compared": compared, "rows_repaired": repaired, "diff_count": diffs, "started_at": started, "finished_at": finished, "source_watermark": watermark})
		}
	}
	diffWhere := "1=1"
	diffArgs := []any{}
	if auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
		diffWhere += " AND tenant_id = $" + strconv.Itoa(len(diffArgs)+1)
		diffArgs = append(diffArgs, auth.TenantID)
	}
	if runID := strings.TrimSpace(r.URL.Query().Get("run_id")); runID != "" {
		diffWhere += " AND run_id = $" + strconv.Itoa(len(diffArgs)+1)
		diffArgs = append(diffArgs, runID)
	}
	diffRows, diffErr := h.db.Query(r.Context(), `SELECT run_id, tenant_id, dimension_type, dimension_key, metric, source_value, projected_value, difference, resolution, created_at FROM stats_reconciliation_diffs WHERE `+diffWhere+` ORDER BY created_at DESC LIMIT `+strconv.Itoa(limit), diffArgs...)
	if diffErr != nil {
		writeError(w, http.StatusInternalServerError, "stats reconciliation diff query failed")
		return
	}
	defer diffRows.Close()
	diffItems := make([]map[string]any, 0)
	for diffRows.Next() {
		var runID, tenantID, dimensionType, dimensionKey, metric, resolution string
		var source, projected, difference float64
		var created time.Time
		if diffRows.Scan(&runID, &tenantID, &dimensionType, &dimensionKey, &metric, &source, &projected, &difference, &resolution, &created) == nil {
			diffItems = append(diffItems, map[string]any{"run_id": runID, "tenant_id": tenantID, "dimension_type": dimensionType, "dimension_key": dimensionKey, "metric": metric, "source_value": source, "projected_value": projected, "difference": difference, "resolution": resolution, "created_at": created})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runItems, "diffs": diffItems})
}

func (h *Handler) handleStatsReconciliationApprove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	auth := GetAuthContext(r)
	if auth == nil || (auth.Role != "super_admin" && auth.Role != "admin_key") {
		writeError(w, http.StatusForbidden, "only super_admin or admin_key can approve reconciliation diffs")
		return
	}

	var req struct {
		DiffIDs  []int64 `json:"diff_ids"`
		Action   string  `json:"action"` // "approve" or "reject"
		Reason   string  `json:"reason"`
		Operator string  `json:"operator"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.DiffIDs) == 0 {
		writeError(w, http.StatusBadRequest, "diff_ids is required")
		return
	}

	if req.Action != "approve" && req.Action != "reject" {
		writeError(w, http.StatusBadRequest, "action must be 'approve' or 'reject'")
		return
	}

	operator := req.Operator
	if operator == "" && auth != nil {
		operator = fmt.Sprintf("user:%d", auth.UserID)
	}
	if operator == "" {
		operator = "admin"
	}

	ctx := r.Context()
	tx, err := h.db.Begin(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin transaction failed")
		return
	}
	defer tx.Rollback(ctx)

	approved := 0
	rejected := 0

	for _, diffID := range req.DiffIDs {
		// Fetch the diff
		var runID, tenantID, dimensionType, dimensionKey, metric string
		var sourceValue, projectedValue, difference float64
		var resolution string
		err := tx.QueryRow(ctx, `
			SELECT run_id, tenant_id, dimension_type, dimension_key, metric,
			       source_value, projected_value, difference, resolution
			FROM stats_reconciliation_diffs
			WHERE id = $1
		`, diffID).Scan(&runID, &tenantID, &dimensionType, &dimensionKey, &metric,
			&sourceValue, &projectedValue, &difference, &resolution)
		if err != nil {
			writeError(w, http.StatusNotFound, fmt.Sprintf("diff %d not found", diffID))
			return
		}

		// Verify tenant access for non-super-admin
		if auth != nil && auth.Role != "super_admin" && auth.Role != "admin_key" {
			if tenantID != auth.TenantID {
				writeError(w, http.StatusForbidden, fmt.Sprintf("cannot approve diff for tenant %s", tenantID))
				return
			}
		}

		// Check if already resolved
		if resolution == "approved" || resolution == "rejected" || resolution == "adjusted" {
			continue // skip already resolved
		}

		if req.Action == "approve" {
			// Update diff resolution
			_, err = tx.Exec(ctx, `
				UPDATE stats_reconciliation_diffs
				SET resolution = 'approved'
				WHERE id = $1
			`, diffID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "update diff failed")
				return
			}

			// Create adjustment record
			// Parse dimension_key to extract provider_id, credential_id, etc.
			// For now, we'll create a simple adjustment record
			_, err = tx.Exec(ctx, `
				INSERT INTO stats_adjustments
					(tenant_id, month_start, adjustment_type, dimension_type, dimension_key,
					 metric_name, delta, currency, reason, source_event_id, 
					 approved_by, approved_at, created_by, created_at)
				VALUES ($1, date_trunc('month', now())::date, 'reconciliation', $2, $3,
				        $4, $5, 'USD', $6, $7, $8, now(), $9, now())
			`, tenantID, dimensionType, dimensionKey, metric, difference, 
				req.Reason, runID, operator, operator)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "create adjustment failed")
				return
			}

			approved++
		} else {
			// Reject
			_, err = tx.Exec(ctx, `
				UPDATE stats_reconciliation_diffs
				SET resolution = 'rejected'
				WHERE id = $1
			`, diffID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "update diff failed")
				return
			}
			rejected++
		}
	}

	if err := tx.Commit(ctx); err != nil {
		writeError(w, http.StatusInternalServerError, "commit transaction failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"approved": approved,
		"rejected": rejected,
		"operator": operator,
	})
}

func statsWhere(start, end time.Time, tenant string, r *http.Request) (string, []any) {
	args := []any{start, end}
	where := "day_utc >= ($1 AT TIME ZONE 'UTC')::date AND day_utc < (($2 - INTERVAL '1 microsecond') AT TIME ZONE 'UTC')::date + 1"
	if strings.HasSuffix(r.URL.Path, "/monthly") {
		where = "month_start >= date_trunc('month', $1::timestamptz)::date AND month_start < date_trunc('month', $2::timestamptz)::date + INTERVAL '1 month'"
	}
	if tenant != "" {
		where += " AND tenant_id = $" + strconv.Itoa(len(args)+1)
		args = append(args, tenant)
	}
	query := r.URL.Query()
	for _, filter := range []struct {
		name   string
		column string
	}{
		{"provider_id", "provider_id"},
		{"credential_id", "credential_id"},
		{"canonical_id", "canonical_id"},
	} {
		if value := strings.TrimSpace(query.Get(filter.name)); value != "" {
			if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
				where += " AND " + filter.column + " = $" + strconv.Itoa(len(args)+1)
				args = append(args, parsed)
			}
		}
	}
	if model := strings.TrimSpace(query.Get("model")); model != "" {
		where += " AND raw_model_name = $" + strconv.Itoa(len(args)+1)
		args = append(args, model)
	}
	if traffic := strings.TrimSpace(query.Get("traffic_class")); traffic != "" {
		where += " AND traffic_class = $" + strconv.Itoa(len(args)+1)
		args = append(args, traffic)
	}
	return where, args
}

func isMissingStatsRelation(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "stats_usage_daily") || strings.Contains(strings.ToLower(err.Error()), "stats_usage_monthly")
}
