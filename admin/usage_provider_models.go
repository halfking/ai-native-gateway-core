package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type providerModelUsage struct {
	Model            string  `json:"model"`
	RequestCount     int     `json:"request_count"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
	SuccessRate      float64 `json:"success_rate"`
}

type providerDailyModelUsage struct {
	Date             string  `json:"date"`
	Model            string  `json:"model"`
	RequestCount     int     `json:"request_count"`
	TotalTokens      int     `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

func (h *Handler) usageProviderModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if !h.providerExists(ctx, providerID) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 30)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	limit := queryInt(r, "limit", 100)
	if limit < 1 {
		limit = 1
	}
	if limit > 500 {
		limit = 500
	}
	tid := EffectiveTenantIDAll(r)
	whereTenant := ""
	args := []any{providerID, startTime, endTime, limit}
	if tid != "" {
		whereTenant = " AND tenant_id = $5"
		args = append(args, tid)
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT COALESCE(NULLIF(raw_model_name, ''), 'unknown'),
			COUNT(*)::int,
			COALESCE(SUM(prompt_tokens), 0)::int,
			COALESCE(SUM(completion_tokens), 0)::int,
			COALESCE(SUM(total_tokens), 0)::int,
			COALESCE(SUM(cost_usd), 0)::float8,
			COALESCE(AVG(latency_ms), 0)::float8,
			COALESCE(AVG(CASE WHEN success THEN 1.0 ELSE 0.0 END), 0)::float8
		FROM usage_ledger_with_current_month
		WHERE provider_id = $1 AND ts >= $2 AND ts < $3%s
		GROUP BY 1
		ORDER BY SUM(cost_usd) DESC, COUNT(*) DESC
		LIMIT $4
	`, whereTenant), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := make([]providerModelUsage, 0)
	for rows.Next() {
		var u providerModelUsage
		if err := rows.Scan(&u.Model, &u.RequestCount, &u.PromptTokens, &u.CompletionTokens,
			&u.TotalTokens, &u.CostUSD, &u.AvgLatencyMs, &u.SuccessRate); err != nil {
			continue
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) usageProviderDailyModels(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if !h.providerExists(ctx, providerID) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 30)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	tid := EffectiveTenantIDAll(r)
	whereTenant := ""
	args := []any{providerID, startTime, endTime}
	if tid != "" {
		whereTenant = " AND tenant_id = $4"
		args = append(args, tid)
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT TO_CHAR(DATE_TRUNC('day', ts AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
			COALESCE(NULLIF(raw_model_name, ''), 'unknown'),
			COUNT(*)::int,
			COALESCE(SUM(total_tokens), 0)::int,
			COALESCE(SUM(cost_usd), 0)::float8
		FROM usage_ledger_with_current_month
		WHERE provider_id = $1 AND ts >= $2 AND ts < $3%s
		GROUP BY 1, 2
		ORDER BY 1 DESC, SUM(cost_usd) DESC
	`, whereTenant), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	out := make([]providerDailyModelUsage, 0)
	for rows.Next() {
		var u providerDailyModelUsage
		if err := rows.Scan(&u.Date, &u.Model, &u.RequestCount, &u.TotalTokens, &u.CostUSD); err != nil {
			continue
		}
		out = append(out, u)
	}
	writeJSON(w, http.StatusOK, out)
}

// usageProviderDetailExport streams per-provider daily model CSV (reconciliation).
func (h *Handler) usageProviderDetailExport(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if !h.providerExists(ctx, providerID) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	var name, code string
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(display_name, code), COALESCE(code, '')
		FROM providers WHERE id = $1
	`, providerID).Scan(&name, &code)

	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 30)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	tid := EffectiveTenantIDAll(r)
	whereTenant := ""
	args := []any{providerID, startTime, endTime}
	if tid != "" {
		whereTenant = " AND tenant_id = $4"
		args = append(args, tid)
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT TO_CHAR(DATE_TRUNC('day', ts AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
			COALESCE(NULLIF(raw_model_name, ''), 'unknown'),
			COUNT(*)::bigint,
			COALESCE(SUM(total_tokens), 0)::bigint,
			COALESCE(SUM(cost_usd), 0)::float8
		FROM usage_ledger_with_current_month
		WHERE provider_id = $1 AND ts >= $2 AND ts < $3%s
		GROUP BY 1, 2
		ORDER BY 1 DESC, SUM(cost_usd) DESC
	`, whereTenant), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export query failed")
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		"attachment; filename=provider-%d-daily-models-%s_%s.csv",
		providerID,
		startTime.Format("20060102"),
		endTime.Add(-24*time.Hour).Format("20060102"),
	))
	w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	periodStart := startTime.Format("2006-01-02")
	periodEnd := endTime.Add(-24 * time.Hour).Format("2006-01-02")
	if endTime.Sub(startTime) <= 24*time.Hour {
		periodEnd = periodStart
	}
	_ = cw.Write([]string{
		"period_start", "period_end",
		"date", "provider_id", "provider_name", "provider_code", "model", "requests", "total_tokens", "cost_usd",
	})
	for rows.Next() {
		var date, model string
		var reqs, tokens int64
		var cost float64
		if err := rows.Scan(&date, &model, &reqs, &tokens, &cost); err != nil {
			continue
		}
		_ = cw.Write([]string{
			periodStart, periodEnd,
			date, strconv.Itoa(providerID), name, code, model,
			strconv.FormatInt(reqs, 10),
			strconv.FormatInt(tokens, 10),
			fmt.Sprintf("%.6f", cost),
		})
	}
	cw.Flush()
}
