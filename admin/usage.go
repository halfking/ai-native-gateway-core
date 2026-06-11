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
	case remaining == "summary":
		h.usageSummary(w, r)
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
	case remaining == "by-tenant":
		h.usageByTenant(w, r)
	case remaining == "tenants":
		h.listTenants(w, r)
	default:
		h.usageKeyDetail(w, r)
	}
}

func (h *Handler) handleUsageSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	h.usageSummary(w, r)
}

func (h *Handler) usageSummary(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var summary struct {
		TotalRequests       int     `json:"total_requests"`
		TotalPromptTokens   int     `json:"total_prompt_tokens"`
		TotalCompletionTok  int     `json:"total_completion_tokens"`
		TotalCostUSD        float64 `json:"total_cost_usd"`
		AvgLatencyMs        float64 `json:"avg_latency_ms"`
		SuccessRate         float64 `json:"success_rate"`
	}
	row := h.db.QueryRow(ctx, `
		SELECT
			COUNT(*)                                        AS total_requests,
			COALESCE(SUM(prompt_tokens), 0)                 AS total_prompt_tokens,
			COALESCE(SUM(completion_tokens), 0)             AS total_completion_tokens,
			COALESCE(SUM(cost_usd), 0.0)                    AS total_cost_usd,
			COALESCE(AVG(latency_ms), 0.0)                  AS avg_latency_ms,
			COALESCE(
				SUM(CASE WHEN success THEN 1 ELSE 0 END)::FLOAT
				/ NULLIF(COUNT(*), 0), 1.0
			)                                               AS success_rate
		FROM usage_ledger
		WHERE tenant_id = 'default'
		  AND ts >= now() - ($1 * INTERVAL '1 day')
	`, days)
	if err := row.Scan(
		&summary.TotalRequests,
		&summary.TotalPromptTokens,
		&summary.TotalCompletionTok,
		&summary.TotalCostUSD,
		&summary.AvgLatencyMs,
		&summary.SuccessRate,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "summary query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (h *Handler) usageDashboard(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var overview struct {
		TotalAPIKeys           int `json:"total_api_keys"`
		ActiveAPIKeys          int `json:"active_api_keys"`
		ActiveAPIKeysInWindow  int `json:"active_api_keys_in_window"`
		TotalModels            int `json:"total_models"`
		ActiveModelsInWindow   int `json:"active_models_in_window"`
		TotalProviders         int `json:"total_providers"`
		ActiveProviders        int `json:"active_providers"`
		OfflineModels          int `json:"offline_models"`
		OfflineCredentials     int `json:"offline_credentials"`
		TotalCredentials       int `json:"total_credentials"`
	}

	row := h.db.QueryRow(ctx, `
		WITH usage_window AS (
			SELECT *
			  FROM usage_ledger
			 WHERE tenant_id = 'default'
			   AND ts >= now() - ($1 * INTERVAL '1 day')
		),
		active_key_window AS (
			SELECT COUNT(DISTINCT api_key_id) AS cnt
			  FROM usage_window
			 WHERE api_key_id IS NOT NULL
		),
		active_model_window AS (
			SELECT COUNT(DISTINCT COALESCE(NULLIF(raw_model_name, ''), canonical_id::text)) AS cnt
			  FROM usage_window
			 WHERE raw_model_name IS NOT NULL OR canonical_id IS NOT NULL
		)
		SELECT
			(SELECT COUNT(*) FROM api_keys WHERE tenant_id = 'default')                                                  AS total_api_keys,
			(SELECT COUNT(*) FROM api_keys WHERE tenant_id = 'default' AND enabled = TRUE)                               AS active_api_keys,
			(SELECT COALESCE(cnt, 0) FROM active_key_window)                                                             AS active_api_keys_in_window,
			(SELECT COUNT(*) FROM models_canonical)                                                                      AS total_models,
			(SELECT COALESCE(cnt, 0) FROM active_model_window)                                                           AS active_models_in_window,
			(SELECT COUNT(*) FROM providers WHERE tenant_id = 'default')                                                  AS total_providers,
			(SELECT COUNT(*) FROM providers WHERE tenant_id = 'default' AND enabled = TRUE)                              AS active_providers,
			(SELECT COUNT(*) FROM models_canonical mc WHERE COALESCE(mc.status, 'active') <> 'active')                    AS offline_models,
			(SELECT COUNT(*) FROM credentials c WHERE c.tenant_id = 'default' AND COALESCE(c.status, 'active') <> 'active') AS offline_credentials,
			(SELECT COUNT(*) FROM credentials c WHERE c.tenant_id = 'default')                                            AS total_credentials
	`, days)
	if err := row.Scan(
		&overview.TotalAPIKeys,
		&overview.ActiveAPIKeys,
		&overview.ActiveAPIKeysInWindow,
		&overview.TotalModels,
		&overview.ActiveModelsInWindow,
		&overview.TotalProviders,
		&overview.ActiveProviders,
		&overview.OfflineModels,
		&overview.OfflineCredentials,
		&overview.TotalCredentials,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "dashboard query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, overview)
}

func (h *Handler) usageHotKeys(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	limit := queryInt(r, "limit", 10)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			ul.api_key_id,
			ak.key_prefix,
			app.code AS application_code,
			ak.owner_user,
			COUNT(*)                                         AS request_count,
			COALESCE(SUM(ul.total_tokens), 0)                AS total_tokens,
			COALESCE(SUM(ul.cost_usd), 0.0)                  AS total_cost_usd,
			MAX(ul.ts)                                       AS last_used_at
		FROM usage_ledger ul
		LEFT JOIN api_keys ak ON ak.id = ul.api_key_id
		LEFT JOIN applications app ON app.id = ak.application_id
		WHERE ul.tenant_id = 'default'
		  AND ul.ts >= now() - ($1 * INTERVAL '1 day')
		  AND ul.api_key_id IS NOT NULL
		GROUP BY ul.api_key_id, ak.key_prefix, app.code, ak.owner_user
		ORDER BY total_tokens DESC, total_cost_usd DESC, request_count DESC
		LIMIT $2
	`, days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hot-keys query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type hotKey struct {
		APIKeyID        *int       `json:"api_key_id"`
		KeyPrefix       *string    `json:"key_prefix"`
		ApplicationCode *string    `json:"application_code"`
		OwnerUser       *string    `json:"owner_user"`
		RequestCount    int        `json:"request_count"`
		TotalTokens     int        `json:"total_tokens"`
		TotalCostUSD    float64    `json:"total_cost_usd"`
		LastUsedAt      *time.Time `json:"last_used_at"`
	}
	keys := make([]hotKey, 0)
	for rows.Next() {
		var k hotKey
		if err := rows.Scan(
			&k.APIKeyID,
			&k.KeyPrefix,
			&k.ApplicationCode,
			&k.OwnerUser,
			&k.RequestCount,
			&k.TotalTokens,
			&k.TotalCostUSD,
			&k.LastUsedAt,
		); err != nil {
			continue
		}
		keys = append(keys, k)
	}
	writeJSON(w, http.StatusOK, keys)
}

func (h *Handler) usageByProvider(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	limit := queryInt(r, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			p.id,
			COALESCE(p.display_name, p.code)                                              AS provider_name,
			COALESCE(p.code, 'unknown')                                                   AS provider_code,
			COUNT(*)                                                                     AS request_count,
			COALESCE(SUM(u.prompt_tokens), 0)                                            AS prompt_tokens,
			COALESCE(SUM(u.completion_tokens), 0)                                        AS completion_tokens,
			COALESCE(SUM(u.cost_usd), 0.0)                                               AS total_cost_usd,
			COALESCE(AVG(CASE WHEN u.success THEN 1 ELSE 0 END), 0.0)                    AS success_rate
		FROM usage_ledger u
		JOIN credentials c ON c.id = u.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE u.tenant_id = 'default'
		  AND u.ts >= now() - ($1 * INTERVAL '1 day')
		GROUP BY p.id, p.display_name, p.code
		ORDER BY total_cost_usd DESC, request_count DESC
		LIMIT $2
	`, days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "by-provider query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type providerUsage struct {
		ProviderID       int     `json:"provider_id"`
		ProviderName     string  `json:"provider_name"`
		ProviderCode     string  `json:"provider_code"`
		RequestCount     int     `json:"request_count"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		TotalCostUSD     float64 `json:"total_cost_usd"`
		SuccessRate      float64 `json:"success_rate"`
	}
	usage := make([]providerUsage, 0)
	for rows.Next() {
		var u providerUsage
		if err := rows.Scan(
			&u.ProviderID, &u.ProviderName, &u.ProviderCode,
			&u.RequestCount, &u.PromptTokens, &u.CompletionTokens,
			&u.TotalCostUSD, &u.SuccessRate,
		); err != nil {
			continue
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageByModel(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	limit := queryInt(r, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			ul.raw_model_name                                              AS model,
			COALESCE(p.code, 'unknown')                                    AS provider_code,
			COUNT(*)                                                       AS total_requests,
			COALESCE(SUM(ul.total_tokens),
			         SUM(ul.prompt_tokens) + SUM(ul.completion_tokens), 0) AS total_tokens,
			COALESCE(SUM(ul.cost_usd), 0.0)                                AS total_cost_usd,
			COALESCE(AVG(ul.latency_ms), 0.0)                              AS avg_latency_ms
		FROM usage_ledger ul
		LEFT JOIN providers p ON p.id = ul.provider_id
		WHERE ul.tenant_id = 'default'
		  AND ul.ts >= now() - ($1 * INTERVAL '1 day')
		GROUP BY ul.raw_model_name, p.code
		ORDER BY total_cost_usd DESC, total_requests DESC
		LIMIT $2
	`, days, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "by-model query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type modelUsage struct {
		Model         string  `json:"model"`
		ProviderCode  string  `json:"provider_code"`
		TotalRequests int     `json:"total_requests"`
		TotalTokens   int     `json:"total_tokens"`
		TotalCostUSD  float64 `json:"total_cost_usd"`
		AvgLatencyMs  float64 `json:"avg_latency_ms"`
	}
	usage := make([]modelUsage, 0)
	for rows.Next() {
		var u modelUsage
		if err := rows.Scan(
			&u.Model,
			&u.ProviderCode,
			&u.TotalRequests,
			&u.TotalTokens,
			&u.TotalCostUSD,
			&u.AvgLatencyMs,
		); err != nil {
			continue
		}
		usage = append(usage, u)
	}
	writeJSON(w, http.StatusOK, usage)
}

func (h *Handler) usageByKey(w http.ResponseWriter, r *http.Request) {
	days := queryInt(r, "days", 7)
	if days < 1 {
		days = 1
	}
	if days > 90 {
		days = 90
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			ul.api_key_id,
			ak.key_prefix,
			COUNT(*)                                       AS request_count,
			COALESCE(SUM(ul.cost_usd), 0.0)                AS cost_usd,
			COALESCE(SUM(ul.prompt_tokens), 0)             AS prompt_tokens,
			COALESCE(SUM(ul.completion_tokens), 0)         AS completion_tokens
		FROM usage_ledger ul
		LEFT JOIN api_keys ak ON ak.id = ul.api_key_id
		WHERE ul.tenant_id = 'default'
		  AND ul.ts >= now() - ($1 * INTERVAL '1 day')
		GROUP BY ul.api_key_id, ak.key_prefix
		ORDER BY cost_usd DESC
	`, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "by-key query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type keyUsage struct {
		APIKeyID         *int    `json:"api_key_id"`
		KeyPrefix        *string `json:"key_prefix"`
		RequestCount     int     `json:"request_count"`
		CostUSD          float64 `json:"cost_usd"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
	}
	usage := make([]keyUsage, 0)
	for rows.Next() {
		var u keyUsage
		if err := rows.Scan(
			&u.APIKeyID, &u.KeyPrefix,
			&u.RequestCount, &u.CostUSD,
			&u.PromptTokens, &u.CompletionTokens,
		); err != nil {
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

	var keyExists int
	err := h.db.QueryRow(ctx, `SELECT 1 FROM api_keys WHERE id = $1`, keyID).Scan(&keyExists)
	if err != nil {
		writeError(w, http.StatusNotFound, "API key not found")
		return
	}

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
	days := queryInt(r, "days", 30)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

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
		  AND ul.ts >= now() - ($1 * INTERVAL '1 day')
		GROUP BY app.code
		ORDER BY total_cost_usd DESC
	`, days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "by-application query failed: "+err.Error())
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

func (h *Handler) usageByTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := queryString(r, "tenant")
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant parameter required")
		return
	}
	days := queryInt(r, "days", 30)
	if days < 1 {
		days = 1
	}
	if days > 365 {
		days = 365
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type tenantUsage struct {
		TenantID         string  `json:"tenant_id"`
		TotalRequests    int     `json:"total_requests"`
		TotalPromptTok   int     `json:"total_prompt_tokens"`
		TotalCompTok     int     `json:"total_completion_tokens"`
		TotalCostUSD     float64 `json:"total_cost_usd"`
		UniqueKeys       int     `json:"unique_keys"`
		UniqueModels     int     `json:"unique_models"`
		UniqueApps       int     `json:"unique_applications"`
	}

	var u tenantUsage
	err := h.db.QueryRow(ctx, `
		SELECT
			$1::text,
			COUNT(*),
			COALESCE(SUM(ul.prompt_tokens), 0),
			COALESCE(SUM(ul.completion_tokens), 0),
			COALESCE(SUM(ul.cost_usd), 0.0),
			COUNT(DISTINCT ul.api_key_id),
			COUNT(DISTINCT ul.canonical_id),
			COUNT(DISTINCT ul.application_id)
		FROM usage_ledger ul
		WHERE ul.tenant_id = $1
		  AND ul.ts >= now() - ($2 * INTERVAL '1 day')
	`, tenantID, days).Scan(
		&u.TenantID, &u.TotalRequests, &u.TotalPromptTok, &u.TotalCompTok,
		&u.TotalCostUSD, &u.UniqueKeys, &u.UniqueModels, &u.UniqueApps,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tenant usage query failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (h *Handler) listTenants(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			ak.tenant_id,
			COUNT(*) AS key_count,
			COALESCE(SUM(ak.total_requests), 0) AS total_requests,
			COALESCE(SUM(ak.total_prompt_tokens + ak.total_completion_tokens), 0) AS total_tokens,
			COALESCE(SUM(ak.total_cost_usd), 0)::float8 AS total_cost_usd
		FROM api_keys ak
		GROUP BY ak.tenant_id
		ORDER BY total_cost_usd DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "tenants query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type tenantSummary struct {
		TenantID     string  `json:"tenant_id"`
		KeyCount     int     `json:"key_count"`
		TotalReqs    int64   `json:"total_requests"`
		TotalTokens  int64   `json:"total_tokens"`
		TotalCostUSD float64 `json:"total_cost_usd"`
	}
	tenants := make([]tenantSummary, 0)
	for rows.Next() {
		var t tenantSummary
		if err := rows.Scan(&t.TenantID, &t.KeyCount, &t.TotalReqs, &t.TotalTokens, &t.TotalCostUSD); err != nil {
			continue
		}
		tenants = append(tenants, t)
	}
	writeJSON(w, http.StatusOK, tenants)
}
