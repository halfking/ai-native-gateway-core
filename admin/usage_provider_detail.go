package admin

import (
	"context"
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleUsageProviderRoutes serves /api/usage/providers/* (detail, trend, export).
func (h *Handler) handleUsageProviderRoutes(w http.ResponseWriter, r *http.Request, remaining string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	path := strings.TrimPrefix(remaining, "providers/")
	if path == "export" {
		h.usageProvidersExport(w, r)
		return
	}
	parts := splitPath(path)
	if len(parts) == 0 {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	id, err := strconv.Atoi(parts[0])
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	if len(parts) == 1 {
		h.usageProviderSummary(w, r, id)
		return
	}
	switch parts[1] {
	case "trend":
		h.usageProviderTrend(w, r, id)
	case "models":
		h.usageProviderModels(w, r, id)
	case "daily-models":
		h.usageProviderDailyModels(w, r, id)
	case "export":
		h.usageProviderDetailExport(w, r, id)
	default:
		writeError(w, http.StatusNotFound, "endpoint not found")
	}
}

type providerUsageSummary struct {
	ProviderID       int     `json:"provider_id"`
	ProviderName     string  `json:"provider_name"`
	ProviderCode     string  `json:"provider_code"`
	RequestCount     int     `json:"request_count"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	SuccessRate      float64 `json:"success_rate"`
	UniqueModels     int     `json:"unique_models"`
	WindowStart      string  `json:"window_start"`
	WindowEnd        string  `json:"window_end"`
}

func (h *Handler) usageProviderSummary(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
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
		whereTenant = " AND u.tenant_id = $4"
		args = append(args, tid)
	}
	var summary providerUsageSummary
	err := h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT p.id,
			COALESCE(p.display_name, p.code),
			COALESCE(p.code, 'unknown'),
			COUNT(*)::int,
			COALESCE(SUM(u.prompt_tokens), 0)::int,
			COALESCE(SUM(u.completion_tokens), 0)::int,
			COALESCE(SUM(u.total_tokens), 0)::int,
			COALESCE(SUM(u.cost_usd), 0)::float8,
			COALESCE(AVG(CASE WHEN u.success THEN 1.0 ELSE 0.0 END), 0)::float8,
			COUNT(DISTINCT u.raw_model_name)::int
		FROM usage_ledger_with_current_month u
		JOIN providers p ON p.id = u.provider_id
		WHERE u.provider_id = $1 AND u.ts >= $2 AND u.ts < $3%s
		GROUP BY p.id, p.display_name, p.code
	`, whereTenant), args...).Scan(
		&summary.ProviderID, &summary.ProviderName, &summary.ProviderCode,
		&summary.RequestCount, &summary.PromptTokens, &summary.CompletionTokens,
		&summary.TotalTokens, &summary.TotalCostUSD, &summary.SuccessRate, &summary.UniqueModels,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	summary.WindowStart = startTime.UTC().Format(time.RFC3339)
	summary.WindowEnd = endTime.UTC().Format(time.RFC3339)
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) usageProviderTrend(w http.ResponseWriter, r *http.Request, providerID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	if !h.providerExists(ctx, providerID) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	period, periodErr := validateUsageTrendPeriod(queryString(r, "period"))
	if periodErr != nil {
		writeError(w, http.StatusBadRequest, periodErr.Error())
		return
	}
	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 30)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	if windowErr := validateTrendGranularityWindow(startTime, endTime, period); windowErr != nil {
		writeError(w, http.StatusBadRequest, windowErr.Error())
		return
	}
	dateFormat := trendPeriodDateFormat(period)
	tid := EffectiveTenantIDAll(r)
	tenantFilter := ""
	args := []any{period, providerID, dateFormat, startTime, endTime}
	if tid != "" {
		tenantFilter = " AND tenant_id = $6"
		args = append(args, tid)
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		WITH buckets AS (
			SELECT generate_series(
				DATE_TRUNC($1, $4::timestamptz),
				DATE_TRUNC($1, $5::timestamptz - INTERVAL '1 microsecond'),
				('1 ' || $1)::interval
			) AS bucket
		),
		agg AS (
			SELECT DATE_TRUNC($1, ts) AS bucket,
				COUNT(*)::int AS requests,
				COALESCE(SUM(prompt_tokens), 0)::int AS prompt_tokens,
				COALESCE(SUM(completion_tokens), 0)::int AS completion_tokens,
				COALESCE(SUM(total_tokens), 0)::int AS total_tokens,
				COALESCE(SUM(cost_usd), 0)::float8 AS cost_usd
			FROM usage_ledger_with_current_month
			WHERE provider_id = $2 AND ts >= $4 AND ts < $5%s
			GROUP BY 1
		)
		SELECT TO_CHAR(b.bucket, $3) AS period,
			COALESCE(a.requests, 0),
			COALESCE(a.prompt_tokens, 0),
			COALESCE(a.completion_tokens, 0),
			COALESCE(a.total_tokens, 0),
			COALESCE(a.cost_usd, 0)::float8
		FROM buckets b
		LEFT JOIN agg a ON a.bucket = b.bucket
		ORDER BY b.bucket
	`, tenantFilter), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()
	type trendEntry struct {
		Period           string  `json:"period"`
		Requests         int     `json:"requests"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalTokens      int     `json:"total_tokens"`
		CostUSD          float64 `json:"cost_usd"`
	}
	out := make([]trendEntry, 0)
	for rows.Next() {
		var e trendEntry
		if err := rows.Scan(&e.Period, &e.Requests, &e.PromptTokens, &e.CompletionTokens, &e.TotalTokens, &e.CostUSD); err != nil {
			continue
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) providerExists(ctx context.Context, providerID int) bool {
	var n int
	err := h.db.QueryRow(ctx, `SELECT 1 FROM providers WHERE id = $1`, providerID).Scan(&n)
	return err == nil
}

func (h *Handler) usageProvidersExport(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	startTime, endTime, rangeErr := resolveUsageTimeRange(r, 1)
	if rangeErr != nil {
		writeError(w, http.StatusBadRequest, rangeErr.Error())
		return
	}
	tid := EffectiveTenantIDAll(r)
	whereTenant := ""
	args := []any{startTime, endTime}
	if tid != "" {
		whereTenant = " AND u.tenant_id = $3"
		args = append(args, tid)
	}
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT p.id,
			COALESCE(p.display_name, p.code),
			COALESCE(p.code, ''),
			COUNT(*)::bigint,
			COALESCE(SUM(u.prompt_tokens), 0)::bigint,
			COALESCE(SUM(u.completion_tokens), 0)::bigint,
			COALESCE(SUM(u.total_tokens), 0)::bigint,
			COALESCE(SUM(u.cost_usd), 0)::float8,
			COALESCE(AVG(CASE WHEN u.success THEN 1.0 ELSE 0.0 END), 0)::float8
		FROM usage_ledger_with_current_month u
		JOIN providers p ON p.id = u.provider_id
		WHERE u.ts >= $1 AND u.ts < $2%s
		GROUP BY p.id, p.display_name, p.code
		ORDER BY SUM(u.cost_usd) DESC, COUNT(*) DESC
	`, whereTenant), args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export query failed")
		return
	}
	defer rows.Close()

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(
		"attachment; filename=provider-usage-%s_%s.csv",
		startTime.Format("20060102"), endTime.Add(-24*time.Hour).Format("20060102"),
	))
	w.Write([]byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM for Excel
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"provider_id", "provider_name", "provider_code",
		"requests", "prompt_tokens", "completion_tokens", "total_tokens",
		"cost_usd", "success_rate", "period_start", "period_end",
	})
	periodStart := startTime.Format("2006-01-02")
	periodEnd := endTime.Add(-24 * time.Hour).Format("2006-01-02")
	if endTime.Sub(startTime) <= 24*time.Hour {
		periodEnd = periodStart
	}
	for rows.Next() {
		var id int
		var name, code string
		var reqs, pt, ct, tt int64
		var cost, sr float64
		if err := rows.Scan(&id, &name, &code, &reqs, &pt, &ct, &tt, &cost, &sr); err != nil {
			continue
		}
		_ = cw.Write([]string{
			strconv.Itoa(id), name, code,
			strconv.FormatInt(reqs, 10),
			strconv.FormatInt(pt, 10),
			strconv.FormatInt(ct, 10),
			strconv.FormatInt(tt, 10),
			fmt.Sprintf("%.6f", cost),
			fmt.Sprintf("%.4f", sr),
			periodStart, periodEnd,
		})
	}
	cw.Flush()
}
