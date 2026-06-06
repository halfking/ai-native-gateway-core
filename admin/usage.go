package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

func (h *Handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	remaining := r.URL.Path[len("/api/usage/"):]
	switch {
	case remaining == "dashboard":
		h.usageDashboard(w, r)
	case remaining == "hot-keys":
		h.usageHotKeys(w, r)
	case remaining == "by-provider":
		h.usageByProvider(w, r)
	case remaining == "by-model":
		h.usageByModel(w, r)
	case remaining == "by-key":
		h.usageByKey(w, r)
	case remaining == "by-application":
		h.usageByApplication(w, r)
	case remaining == "summary":
		h.usageDashboard(w, r)
	default:
		h.usageKeyDetail(w, r)
	}
}

func (h *Handler) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.usageDashboard(w, r)
}

func (h *Handler) usageDashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var stats struct {
		TotalRequests   int     `json:"total_requests"`
		TotalTokens     int     `json:"total_tokens"`
		TotalCostUSD    float64 `json:"total_cost_usd"`
		SuccessRate     float64 `json:"success_rate"`
		ActiveKeys      int     `json:"active_keys"`
		ActiveProviders int     `json:"active_providers"`
	}

	h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0),
		       COALESCE(AVG(CASE WHEN success THEN 1 ELSE 0 END),0)
		FROM usage_ledger WHERE ts > now() - interval '24 hours'
	`).Scan(&stats.TotalRequests, &stats.TotalTokens, &stats.TotalCostUSD, &stats.SuccessRate)

	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM api_keys WHERE enabled = TRUE AND tenant_id = 'default' AND COALESCE(status, 'active') <> 'revoked'`).Scan(&stats.ActiveKeys)
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM providers WHERE enabled = TRUE AND tenant_id = 'default'`).Scan(&stats.ActiveProviders)

	writeJSON(w, http.StatusOK, stats)
}

func (h *Handler) usageHotKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT api_key_id, COUNT(*) as req_count, SUM(cost_usd)::float8 as total_cost
		FROM usage_ledger
		WHERE ts > now() - interval '24 hours'
		GROUP BY api_key_id
		ORDER BY req_count DESC
		LIMIT 20
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type hotKey struct {
		APIKeyID  *int     `json:"api_key_id"`
		ReqCount  int      `json:"request_count"`
		TotalCost *float64 `json:"total_cost_usd"`
	}
	keys := make([]hotKey, 0)
	for rows.Next() {
		var k hotKey
		if err := rows.Scan(&k.APIKeyID, &k.ReqCount, &k.TotalCost); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *Handler) usageByProvider(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT p.id, COALESCE(p.display_name, p.code) as provider_name,
		       COUNT(*) as req_count,
		       COALESCE(SUM(u.prompt_tokens),0) as prompt_tokens,
		       COALESCE(SUM(u.completion_tokens),0) as completion_tokens,
		       COALESCE(SUM(u.cost_usd),0)::float8 as total_cost,
		       COALESCE(AVG(CASE WHEN u.success THEN 1 ELSE 0 END),0) as success_rate
		FROM usage_ledger u
		JOIN credentials c ON c.id = u.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE u.ts > now() - interval '24 hours'
		GROUP BY p.id, p.display_name, p.code
		ORDER BY total_cost DESC
		LIMIT 50
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type providerUsage struct {
		ProviderID      int      `json:"provider_id"`
		ProviderName    string   `json:"provider_name"`
		ReqCount        int      `json:"request_count"`
		PromptTokens    int      `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalCost       float64  `json:"total_cost_usd"`
		SuccessRate     float64  `json:"success_rate"`
	}
	usage := make([]providerUsage, 0)
	for rows.Next() {
		var u providerUsage
		if err := rows.Scan(&u.ProviderID, &u.ProviderName, &u.ReqCount, &u.PromptTokens, &u.CompletionTokens, &u.TotalCost, &u.SuccessRate); err != nil {
			continue
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageByModel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT raw_model_name, COUNT(*) as req_count,
		       SUM(prompt_tokens) as prompt_tokens,
		       SUM(completion_tokens) as completion_tokens,
		       SUM(cost_usd)::float8 as total_cost
		FROM usage_ledger
		WHERE ts > now() - interval '24 hours'
		GROUP BY raw_model_name
		ORDER BY req_count DESC
		LIMIT 50
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type modelUsage struct {
		Model            string   `json:"model"`
		ReqCount         int      `json:"request_count"`
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		TotalCost        *float64 `json:"total_cost_usd"`
	}
	usage := make([]modelUsage, 0)
	for rows.Next() {
		var u modelUsage
		if err := rows.Scan(&u.Model, &u.ReqCount, &u.PromptTokens, &u.CompletionTokens, &u.TotalCost); err != nil {
			continue
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageByKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT api_key_id, COUNT(*) as req_count,
		       SUM(cost_usd)::float8 as total_cost
		FROM usage_ledger
		WHERE ts > now() - interval '24 hours'
		GROUP BY api_key_id
		ORDER BY total_cost DESC NULLS LAST
		LIMIT 50
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type keyUsage struct {
		APIKeyID  *int     `json:"api_key_id"`
		ReqCount  int      `json:"request_count"`
		TotalCost *float64 `json:"total_cost_usd"`
	}
	usage := make([]keyUsage, 0)
	for rows.Next() {
		var u keyUsage
		if err := rows.Scan(&u.APIKeyID, &u.ReqCount, &u.TotalCost); err != nil {
			continue
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageKeyDetail(w http.ResponseWriter, r *http.Request) {
	remaining := r.URL.Path[len("/api/usage/"):]
	if remaining == "" {
		h.usageDashboard(w, r)
		return
	}

	parts := splitPath(remaining)
	keyIDStr := parts[0]
	keyID, err := strconv.Atoi(keyIDStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid key id")
		return
	}

	if len(parts) > 1 {
		switch parts[1] {
		case "models":
			h.usageKeyModels(w, r, keyID)
			return
		case "trend":
			h.usageKeyTrend(w, r, keyID)
			return
		case "remaining":
			h.usageKeyRemaining(w, r, keyID)
			return
		}
	}

	days := queryInt(r, "days", 7)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var keyPrefix string
	err = h.db.QueryRow(ctx, `SELECT COALESCE(key_prefix,'') FROM api_keys WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'`, keyID).Scan(&keyPrefix)
	if err != nil {
		writeError(w, http.StatusNotFound, "API key not found")
		return
	}

	var totalReqs, promptTok, compTok, totalTok int
	var cost, avgLatency, successRate float64
	var uniqueModels int
	var firstAt, lastAt *time.Time
	h.db.QueryRow(ctx, `
		SELECT COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		       COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0)::float8,
		       COALESCE(AVG(latency_ms),0)::float8,
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END)::FLOAT / NULLIF(COUNT(*),0), 1.0),
		       COUNT(DISTINCT raw_model_name),
		       MIN(ts), MAX(ts)
		FROM usage_ledger WHERE api_key_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
	`, keyID, days).Scan(&totalReqs, &promptTok, &compTok, &totalTok, &cost, &avgLatency, &successRate, &uniqueModels, &firstAt, &lastAt)

	resp := map[string]any{
		"key_id":               keyID,
		"key_prefix":           keyPrefix,
		"total_requests":       totalReqs,
		"total_prompt_tokens":  promptTok,
		"total_completion_tokens": compTok,
		"total_tokens":         totalTok,
		"total_cost_usd":       cost,
		"avg_latency_ms":       avgLatency,
		"success_rate":         successRate,
		"unique_models":        uniqueModels,
		"first_request_at":     nil,
		"last_request_at":      nil,
	}
	if firstAt != nil {
		resp["first_request_at"] = firstAt.Format(time.RFC3339)
	}
	if lastAt != nil {
		resp["last_request_at"] = lastAt.Format(time.RFC3339)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) usageKeyModels(w http.ResponseWriter, r *http.Request, keyID int) {
	days := queryInt(r, "days", 7)
	limit := queryInt(r, "limit", 50)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT COALESCE(raw_model_name,'unknown'), COUNT(*),
		       COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		       COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0)::float8,
		       COALESCE(AVG(latency_ms),0)::float8,
		       COALESCE(SUM(CASE WHEN success THEN 1 ELSE 0 END)::FLOAT / NULLIF(COUNT(*),0), 1.0),
		       MIN(ts), MAX(ts)
		FROM usage_ledger WHERE api_key_id = $1 AND ts >= now() - ($2 * INTERVAL '1 day')
		GROUP BY raw_model_name ORDER BY SUM(cost_usd) DESC NULLS LAST LIMIT $3
	`, keyID, days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type modelUsage struct {
		Model            string   `json:"model"`
		RequestCount     int      `json:"request_count"`
		PromptTokens     int      `json:"prompt_tokens"`
		CompletionTokens int      `json:"completion_tokens"`
		TotalTokens      int      `json:"total_tokens"`
		CostUSD          float64  `json:"cost_usd"`
		AvgLatencyMs     float64  `json:"avg_latency_ms"`
		SuccessRate      float64  `json:"success_rate"`
		FirstUsedAt      *string  `json:"first_used_at"`
		LastUsedAt       *string  `json:"last_used_at"`
	}
	usage := make([]modelUsage, 0)
	for rows.Next() {
		var u modelUsage
		var firstAt, lastAt *time.Time
		if err := rows.Scan(&u.Model, &u.RequestCount, &u.PromptTokens, &u.CompletionTokens,
			&u.TotalTokens, &u.CostUSD, &u.AvgLatencyMs, &u.SuccessRate, &firstAt, &lastAt); err != nil {
			continue
		}
		if firstAt != nil {
			s := firstAt.Format(time.RFC3339)
			u.FirstUsedAt = &s
		}
		if lastAt != nil {
			s := lastAt.Format(time.RFC3339)
			u.LastUsedAt = &s
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageKeyTrend(w http.ResponseWriter, r *http.Request, keyID int) {
	period := queryString(r, "period")
	if period == "" {
		period = "day"
	}
	days := queryInt(r, "days", 30)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	dateFormat := "YYYY-MM-DD"
	if period == "week" {
		dateFormat = "IYYY-IW"
	} else if period == "month" {
		dateFormat = "YYYY-MM"
	}

	rows, err := h.db.Query(ctx, `
		SELECT TO_CHAR(DATE_TRUNC($1, ts), $4) AS period,
		       COUNT(*), COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0),
		       COALESCE(SUM(total_tokens),0), COALESCE(SUM(cost_usd),0)::float8
		FROM usage_ledger WHERE api_key_id = $2 AND ts >= now() - ($3 * INTERVAL '1 day')
		GROUP BY DATE_TRUNC($1, ts) ORDER BY DATE_TRUNC($1, ts)
	`, period, keyID, days, dateFormat)
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
	trends := make([]trendEntry, 0)
	for rows.Next() {
		var t trendEntry
		if err := rows.Scan(&t.Period, &t.Requests, &t.PromptTokens, &t.CompletionTokens, &t.TotalTokens, &t.CostUSD); err != nil {
			continue
		}
		trends = append(trends, t)
	}
	writeJSON(w, http.StatusOK, trends)
}

func (h *Handler) usageByApplication(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	period := queryString(r, "period")
	if period == "" {
		period = "month"
	}

	intervalMap := map[string]string{
		"today": "1 day",
		"week":  "7 days",
		"month": "30 days",
		"year":  "365 days",
	}
	interval := intervalMap[period]
	if interval == "" {
		interval = "30 days"
	}

	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(app.code, 'unknown') AS application_code,
			COUNT(*) AS request_count,
			COALESCE(SUM(ul.cost_usd), 0.0) AS total_cost_usd,
			COALESCE(SUM(ul.total_tokens), 0) AS total_tokens,
			COALESCE(SUM(ul.prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(ul.completion_tokens), 0) AS completion_tokens,
			COUNT(DISTINCT ul.api_key_id) AS unique_keys,
			COUNT(DISTINCT ul.canonical_id) AS unique_models
		FROM usage_ledger ul
		LEFT JOIN applications app ON app.id = ul.application_id
		WHERE ul.tenant_id = 'default'
		  AND ul.ts >= now() - ($1::INTERVAL)
		GROUP BY app.code
		ORDER BY total_cost_usd DESC
	`, interval)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type appUsage struct {
		ApplicationCode  string  `json:"application_code"`
		RequestCount     int     `json:"request_count"`
		TotalCostUSD     float64 `json:"total_cost_usd"`
		TotalTokens      int     `json:"total_tokens"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		UniqueKeys       int     `json:"unique_keys"`
		UniqueModels     int     `json:"unique_models"`
	}
	usage := make([]appUsage, 0)
	for rows.Next() {
		var u appUsage
		if err := rows.Scan(&u.ApplicationCode, &u.RequestCount, &u.TotalCostUSD,
			&u.TotalTokens, &u.PromptTokens, &u.CompletionTokens,
			&u.UniqueKeys, &u.UniqueModels); err != nil {
			continue
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageKeyRemaining(w http.ResponseWriter, r *http.Request, keyID int) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	days := queryInt(r, "days", 30)

	var keyPrefix string
	var budgetUSD *float64
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(key_prefix,''), budget_usd
		FROM api_keys WHERE id = $1 AND COALESCE(status, 'active') <> 'revoked'
	`, keyID).Scan(&keyPrefix, &budgetUSD)
	if err != nil {
		writeError(w, http.StatusNotFound, "API key not found")
		return
	}

	var spentUSD float64
	h.db.QueryRow(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0.0)
		FROM usage_ledger
		WHERE tenant_id = 'default'
		  AND api_key_id = $1
		  AND ts >= now() - ($2 * INTERVAL '1 day')
	`, keyID, days).Scan(&spentUSD)

	var remainingUSD *float64
	quotaOK := true
	if budgetUSD != nil {
		rem := *budgetUSD - spentUSD
		remainingUSD = &rem
		quotaOK = rem > 0
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"key_id":        keyID,
		"key_prefix":    keyPrefix,
		"budget_usd":    budgetUSD,
		"spent_usd":     spentUSD,
		"remaining_usd": remainingUSD,
		"quota_ok":      quotaOK,
	})
}
