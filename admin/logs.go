package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type requestLogRow struct {
	Ts                 time.Time  `json:"ts"`
	RequestID          string     `json:"request_id"`
	APIKeyID           *int       `json:"api_key_id"`
	EndUserID          *string    `json:"end_user_id"`
	ClientModel        *string    `json:"client_model"`
	OutboundModel      *string    `json:"outbound_model"`
	CredentialID       *int       `json:"credential_id"`
	CredentialLabel    *string    `json:"credential_label"`
	ProviderID         *int       `json:"provider_id"`
	ProviderName       *string    `json:"provider_name"`
	ProviderCode       *string    `json:"provider_code"`
	ClientProfile      *string    `json:"client_profile"`
	RequestMode        *string    `json:"request_mode"`
	PromptTokens       *int       `json:"prompt_tokens"`
	CompletionTokens   *int       `json:"completion_tokens"`
	CacheReadTokens    *int       `json:"cache_read_tokens"`
	CacheWriteTokens   *int       `json:"cache_write_tokens"`
	TotalTokens        *int       `json:"total_tokens"`
	CostUSD            *float64   `json:"cost_usd"`
	CostDisplay        *float64   `json:"cost_display"`
	CostCurrency       *string    `json:"cost_currency"`
	LatencyMs          *int       `json:"latency_ms"`
	Success            bool       `json:"success"`
	RequestStatus      string     `json:"request_status"`
	ErrorKind          *string    `json:"error_kind"`
	SearchText         *string    `json:"search_text"`
	IdentityHash       *string    `json:"identity_hash"`
	VirtualClientID    *string    `json:"virtual_client_id"`
	VirtualIP          *string    `json:"virtual_ip"`
	VirtualMAC         *string    `json:"virtual_mac"`
	AffinityHit        *bool      `json:"affinity_hit"`
	RequestChecksum    *string    `json:"request_checksum"`
	ResponseChecksum   *string    `json:"response_checksum"`
	TransformRuleID    *string    `json:"transform_rule_id"`
	EgressProtocol     *string    `json:"egress_protocol"`
	FailureStage       *string    `json:"failure_stage"`
	FailureDetailCode  *string    `json:"failure_detail_code"`
	RequestPreview     *string    `json:"request_preview"`
	TransformSummary   *string    `json:"transform_summary"`
	ResponsePreview    *string    `json:"response_preview"`
	StreamFirstChunkMs *int       `json:"stream_first_chunk_ms"`
	StreamChunkCount   *int       `json:"stream_chunk_count"`
	StreamDoneReceived *bool      `json:"stream_done_received"`
	StreamInterrupted  *bool      `json:"stream_interrupted"`
	StreamDoneSent     *bool      `json:"stream_done_sent"`
	UsageSource        *string    `json:"usage_source"`
	GwSessionID        *string    `json:"gw_session_id"`
	GwTaskID           *string    `json:"gw_task_id"`
	APIKeyPrefix       *string    `json:"api_key_prefix"`
	APIKeyOwnerUser    *string    `json:"api_key_owner_user"`
	ApplicationCode    *string    `json:"application_code"`
	CanonicalName      *string    `json:"canonical_name"`
	ProviderModel      *string    `json:"provider_model"`
	TraceSeq           *int       `json:"trace_seq,omitempty"`
	CreditsCharged     *int64     `json:"credits_charged"`
}

type requestLogDetail struct {
	requestLogRow
	RequestBody  any `json:"request_body"`
	ResponseBody any `json:"response_body"`
}

const requestLogStatusExpr = `COALESCE(
	NULLIF(rl.request_status, ''),
	CASE
		WHEN rl.success THEN 'success'
		WHEN rl.error_kind IS NOT NULL AND rl.error_kind <> '' THEN 'failure'
		ELSE 'in_progress'
	END
)`

const requestLogsSelectCols = `
	rl.ts, rl.request_id, rl.api_key_id, rl.end_user_id,
	rl.client_model, rl.outbound_model,
	rl.credential_id, c.label AS credential_label,
	rl.provider_id, p.display_name AS provider_name,
	p.catalog_code AS provider_code,
	rl.client_profile, rl.request_mode,
	rl.prompt_tokens, rl.completion_tokens,
	rl.cache_read_tokens, rl.cache_write_tokens, rl.total_tokens,
	rl.cost_usd::float8, rl.cost_display::float8, rl.cost_currency, rl.latency_ms, rl.success,
	` + requestLogStatusExpr + ` AS request_status,
	rl.error_kind, rl.search_text,
	rl.identity_hash, rl.virtual_client_id, rl.virtual_ip, rl.virtual_mac,
	rl.affinity_hit, rl.request_checksum, rl.response_checksum,
	rl.transform_rule_id, rl.egress_protocol, rl.failure_stage, rl.failure_detail_code,
	rl.request_preview, rl.transform_summary, rl.response_preview,
	rl.stream_first_chunk_ms, rl.stream_chunk_count,
	rl.stream_done_received, rl.stream_interrupted, rl.stream_done_sent,
	rl.usage_source,
	rl.gw_session_id, rl.gw_task_id,
	COALESCE(NULLIF(TRIM(rl.api_key_prefix), ''), NULLIF(TRIM(ak.key_prefix), '')) AS api_key_prefix,
	COALESCE(NULLIF(TRIM(rl.api_key_owner_user), ''), ak.owner_user) AS api_key_owner_user,
	COALESCE(NULLIF(TRIM(rl.application_code), ''), app.code) AS application_code,
	mc.canonical_name,
	mo_pick.provider_model,
	rl.credits_charged
`

const requestLogsJoins = `
	LEFT JOIN providers p ON p.id = rl.provider_id
	LEFT JOIN credentials c ON c.id = rl.credential_id
	LEFT JOIN api_keys ak ON ak.id = rl.api_key_id
	LEFT JOIN applications app ON app.id = ak.application_id
	LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
	LEFT JOIN LATERAL (
		SELECT COALESCE(
			NULLIF(TRIM(mo.outbound_model_name), ''),
			NULLIF(TRIM(mo.raw_model_name), '')
		) AS provider_model
		FROM model_offers mo
		WHERE mo.credential_id = rl.credential_id
		  AND (
			(rl.canonical_id IS NOT NULL AND mo.canonical_id = rl.canonical_id)
			OR (
				rl.canonical_id IS NULL AND (
					lower(mo.standardized_name) = lower(COALESCE(mc.canonical_name, rl.client_model, ''))
					OR lower(mo.raw_model_name) = lower(COALESCE(rl.outbound_model, rl.client_model, ''))
				)
			)
		  )
		ORDER BY
			CASE
				WHEN rl.outbound_model IS NOT NULL
				 AND lower(COALESCE(NULLIF(TRIM(mo.outbound_model_name), ''), TRIM(mo.raw_model_name)))
					= lower(rl.outbound_model)
				THEN 0 ELSE 1
			END,
			CASE WHEN NULLIF(TRIM(mo.outbound_model_name), '') IS NOT NULL THEN 0 ELSE 1 END,
			CASE
				WHEN lower(TRIM(mo.raw_model_name)) <> lower(TRIM(COALESCE(mo.standardized_name, mc.canonical_name, rl.client_model, '')))
				THEN 0 ELSE 1
			END,
			mo.available DESC NULLS LAST,
			mo.id DESC
		LIMIT 1
	) mo_pick ON TRUE
`

func (h *Handler) handleLogs(w http.ResponseWriter, r *http.Request) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	remaining := r.URL.Path[len("/api/logs/"):]
	if remaining == "" {
		h.listLogs(w, r)
		return
	}
	if remaining == "top-models" {
		h.listTopModels(w, r)
		return
	}
	if remaining == "session-summary" {
		h.handleSessionSummary(w, r)
		return
	}
	if remaining == "session-summary-to-memora" {
		h.handleSessionSummaryToMemora(w, r)
		return
	}
	h.getLog(w, r)
}

func (h *Handler) handleLogsRoot(w http.ResponseWriter, r *http.Request) {
	h.listLogs(w, r)
}

func scanRequestLogRow(rows interface {
	Scan(dest ...any) error
}, withTraceSeq bool) (requestLogRow, error) {
	var l requestLogRow
	dest := []any{
		&l.Ts, &l.RequestID, &l.APIKeyID, &l.EndUserID,
		&l.ClientModel, &l.OutboundModel,
		&l.CredentialID, &l.CredentialLabel,
		&l.ProviderID, &l.ProviderName, &l.ProviderCode,
		&l.ClientProfile, &l.RequestMode,
		&l.PromptTokens, &l.CompletionTokens,
		&l.CacheReadTokens, &l.CacheWriteTokens, &l.TotalTokens,
		&l.CostUSD, &l.CostDisplay, &l.CostCurrency, &l.LatencyMs, &l.Success, &l.RequestStatus, &l.ErrorKind, &l.SearchText,
		&l.IdentityHash, &l.VirtualClientID, &l.VirtualIP, &l.VirtualMAC,
		&l.AffinityHit, &l.RequestChecksum, &l.ResponseChecksum,
		&l.TransformRuleID, &l.EgressProtocol, &l.FailureStage, &l.FailureDetailCode,
		&l.RequestPreview, &l.TransformSummary, &l.ResponsePreview,
		&l.StreamFirstChunkMs, &l.StreamChunkCount,
		&l.StreamDoneReceived, &l.StreamInterrupted, &l.StreamDoneSent,
		&l.UsageSource,
		&l.GwSessionID, &l.GwTaskID,
		&l.APIKeyPrefix, &l.APIKeyOwnerUser, &l.ApplicationCode,
		&l.CanonicalName, &l.ProviderModel, &l.CreditsCharged,
	}
	if withTraceSeq {
		dest = append(dest, &l.TraceSeq)
	}
	err := rows.Scan(dest...)
	return l, err
}

func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	now := time.Now().UTC()
	start := parseQueryTime(r, "from", now.Add(-24*time.Hour))
	end := parseQueryTime(r, "to", now)

	page := queryInt(r, "page", 1)
	if page < 1 {
		page = 1
	}
	pageSize := queryInt(r, "page_size", 100)
	if pageSize < 1 {
		pageSize = 100
	}
	if pageSize > 500 {
		pageSize = 500
	}

	clauses := []string{"rl.ts >= $1", "rl.ts <= $2"}
	args := []any{start, end}
	argIdx := 3

	addFilter := func(clause string, val any) {
		clauses = append(clauses, fmt.Sprintf(clause, argIdx))
		args = append(args, val)
		argIdx++
	}

	// tenant_admin callers may only see request logs for their own tenant's
	// api_keys. The join `ak` (LEFT JOIN api_keys ak ON ak.id = rl.api_key_id)
	// is already present in requestLogsJoins, so we can filter on ak.tenant_id.
	if IsTenantAdmin(r) {
		addFilter("ak.tenant_id = $%d", GetTenantID(r))
		addFilter("rl.tenant_id = $%d", GetTenantID(r))
	}
	if v := queryIntPtr(r, "api_key_id"); v != nil {
		addFilter("rl.api_key_id = $%d", *v)
	}
	if v := strings.TrimSpace(queryString(r, "request_id")); v != "" {
		addFilter("rl.request_id = $%d", v)
	}
	if v := queryIntPtr(r, "provider_id"); v != nil {
		addFilter("rl.provider_id = $%d", *v)
	}
	if v := queryIntPtr(r, "credential_id"); v != nil {
		addFilter("rl.credential_id = $%d", *v)
	}
	if v := strings.TrimSpace(queryString(r, "identity_hash")); v != "" {
		addFilter("rl.identity_hash = $%d", v)
	}
	if v := strings.TrimSpace(queryString(r, "q")); v != "" {
		addFilter("rl.search_text ILIKE $%d", "%"+v+"%")
	}
	if v := strings.TrimSpace(queryString(r, "error_kind")); v != "" {
		addFilter("rl.error_kind = $%d", v)
	}
	if v := strings.TrimSpace(queryString(r, "request_status")); v != "" {
		switch v {
		case "in_progress", "success", "failure":
			clauses = append(clauses, fmt.Sprintf("(%s) = $%d", requestLogStatusExpr, argIdx))
			args = append(args, v)
			argIdx++
		default:
			writeError(w, http.StatusBadRequest, "request_status must be 'in_progress', 'success', or 'failure'")
			return
		}
	} else if v := queryOptionalBool(r, "success"); v != nil {
		status := "failure"
		if *v {
			status = "success"
		}
		clauses = append(clauses, fmt.Sprintf("(%s) = $%d", requestLogStatusExpr, argIdx))
		args = append(args, status)
		argIdx++
	}
	if v := queryIntPtr(r, "canonical_id"); v != nil {
		addFilter("rl.canonical_id = $%d", *v)
	}
	if v := strings.TrimSpace(queryString(r, "model")); v != "" {
		pattern := "%" + v + "%"
		clauses = append(clauses, fmt.Sprintf(`(
			EXISTS (
				SELECT 1 FROM models_canonical mc
				WHERE mc.id = rl.canonical_id
				  AND mc.canonical_name ILIKE $%d
			)
			OR EXISTS (
				SELECT 1
				FROM model_aliases ma
				JOIN models_canonical mc ON mc.id = ma.canonical_id
				WHERE lower(ma.raw_name) = lower(rl.client_model)
				  AND ma.status = 'active'
				  AND mc.canonical_name ILIKE $%d
			)
			OR rl.client_model ILIKE $%d
		)`, argIdx, argIdx+1, argIdx+2))
		args = append(args, pattern, pattern, pattern)
		argIdx += 3
	}
	if v := strings.TrimSpace(queryString(r, "gw_session_id")); v != "" {
		addFilter("rl.gw_session_id = $%d", v)
	}
	if v := strings.TrimSpace(queryString(r, "gw_task_id")); v != "" {
		addFilter("rl.gw_task_id = $%d", v)
	}
	if v := strings.TrimSpace(queryString(r, "usage_source")); v != "" {
		if v != "llm" && v != "estimated" {
			writeError(w, http.StatusBadRequest, "usage_source must be 'llm' or 'estimated'")
			return
		}
		addFilter("rl.usage_source = $%d", v)
	}

	hasTaskFilter := strings.TrimSpace(queryString(r, "gw_task_id")) != ""
	hasSessionFilter := strings.TrimSpace(queryString(r, "gw_session_id")) != ""
	chrono := queryString(r, "chrono") == "1" || hasTaskFilter || hasSessionFilter
	orderBy := "rl.ts DESC"
	traceSeqCol := ""
	if chrono {
		orderBy = "rl.ts ASC"
		traceSeqCol = ", ROW_NUMBER() OVER (ORDER BY rl.ts ASC) AS trace_seq"
	}

	where := strings.Join(clauses, " AND ")

	// For COUNT, we need the same JOINs to filter by ak.tenant_id for tenant_admin
	var count int
	if IsTenantAdmin(r) {
		// COUNT with api_keys join so ak.tenant_id filter works
		if err := h.db.QueryRow(ctx, `
			SELECT COUNT(*) FROM request_logs rl
			LEFT JOIN api_keys ak ON ak.id = rl.api_key_id
			WHERE `+where, args...).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
	} else {
		if err := h.db.QueryRow(ctx, "SELECT COUNT(*) FROM request_logs rl WHERE "+where, args...).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
	}

	offset := (page - 1) * pageSize
	listArgs := append(append([]any{}, args...), pageSize, offset)
	limitIdx := argIdx
	offsetIdx := argIdx + 1

	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s%s
		FROM request_logs rl
		%s
		WHERE %s
		ORDER BY %s
		LIMIT $%d OFFSET $%d
	`, requestLogsSelectCols, traceSeqCol, requestLogsJoins, where, orderBy, limitIdx, offsetIdx), listArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]requestLogRow, 0)
	for rows.Next() {
		l, err := scanRequestLogRow(rows, chrono)
		if err != nil {
			continue
		}
		items = append(items, l)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items": items,
		"count": count,
	})
}

func (h *Handler) getLog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	requestID, err := url.PathUnescape(strings.Trim(r.URL.Path[len("/api/logs/"):], "/"))
	if err != nil || requestID == "" {
		writeError(w, http.StatusBadRequest, "invalid request id")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var detail requestLogDetail
	var requestBodyRaw []byte
	var responseBodyRaw []byte

	err = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s, rl.request_body::text, rl.response_body::text
		  FROM request_logs rl
		%s
		 WHERE rl.request_id = $1
		   AND ($2 OR ak.tenant_id = $3)
		 ORDER BY rl.ts DESC
		 LIMIT 1
	`, requestLogsSelectCols, requestLogsJoins), requestID, !IsTenantAdmin(r), GetTenantID(r)).Scan(
		&detail.Ts,
		&detail.RequestID,
		&detail.APIKeyID,
		&detail.EndUserID,
		&detail.ClientModel,
		&detail.OutboundModel,
		&detail.CredentialID,
		&detail.CredentialLabel,
		&detail.ProviderID,
		&detail.ProviderName,
		&detail.ProviderCode,
		&detail.ClientProfile,
		&detail.RequestMode,
		&detail.PromptTokens,
		&detail.CompletionTokens,
		&detail.CacheReadTokens,
		&detail.CacheWriteTokens,
		&detail.TotalTokens,
		&detail.CostUSD,
		&detail.CostDisplay,
		&detail.CostCurrency,
		&detail.LatencyMs,
		&detail.Success,
		&detail.RequestStatus,
		&detail.ErrorKind,
		&detail.SearchText,
		&detail.IdentityHash,
		&detail.VirtualClientID,
		&detail.VirtualIP,
		&detail.VirtualMAC,
		&detail.AffinityHit,
		&detail.RequestChecksum,
		&detail.ResponseChecksum,
		&detail.TransformRuleID,
		&detail.EgressProtocol,
		&detail.FailureStage,
		&detail.FailureDetailCode,
		&detail.RequestPreview,
		&detail.TransformSummary,
		&detail.ResponsePreview,
		&detail.StreamFirstChunkMs,
		&detail.StreamChunkCount,
		&detail.StreamDoneReceived,
		&detail.StreamInterrupted,
		&detail.StreamDoneSent,
		&detail.UsageSource,
		&detail.GwSessionID,
		&detail.GwTaskID,
		&detail.APIKeyPrefix,
		&detail.APIKeyOwnerUser,
		&detail.ApplicationCode,
		&detail.CanonicalName,
		&detail.ProviderModel,
		&detail.CreditsCharged,
		&requestBodyRaw,
		&responseBodyRaw,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "request log not found")
			return
		}
		slog.Warn("admin getLog scan failed", "request_id", requestID, "error", err.Error())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{
				"detail":    "query failed",
				"db_error":  err.Error(),
				"request_id": requestID,
			},
		})
		return
	}

	detail.RequestBody = decodeJSONText(requestBodyRaw)
	detail.ResponseBody = decodeJSONText(responseBodyRaw)
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) listTopModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	now := time.Now().UTC()
	start := parseQueryTime(r, "from", now.Add(-24*time.Hour))
	end := parseQueryTime(r, "to", now)
	limit := queryInt(r, "limit", 20)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}

	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(mc.id, mc2.id) AS canonical_id,
			COALESCE(mc.canonical_name, mc2.canonical_name, rl.client_model) AS canonical_name,
			COALESCE(mc.display_name, mc2.display_name, mc.canonical_name, mc2.canonical_name, rl.client_model) AS display_name,
			COUNT(*) AS request_count
		FROM request_logs rl
		LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
		LEFT JOIN LATERAL (
			SELECT canonical_id
			FROM model_aliases
			WHERE lower(raw_name) = lower(rl.client_model)
			  AND status = 'active'
			LIMIT 1
		) ma ON TRUE
		LEFT JOIN models_canonical mc2 ON mc2.id = ma.canonical_id
		WHERE rl.ts >= $1 AND rl.ts <= $2
		  AND rl.client_model IS NOT NULL AND rl.client_model != ''
		GROUP BY canonical_id, canonical_name, display_name
		ORDER BY request_count DESC
		LIMIT $3
	`, start, end, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type topModel struct {
		CanonicalID   *int   `json:"canonical_id"`
		CanonicalName string `json:"canonical_name"`
		DisplayName   string `json:"display_name"`
		RequestCount  int    `json:"request_count"`
	}
	items := make([]topModel, 0)
	for rows.Next() {
		var item topModel
		if err := rows.Scan(&item.CanonicalID, &item.CanonicalName, &item.DisplayName, &item.RequestCount); err != nil {
			continue
		}
		items = append(items, item)
	}
	if items == nil {
		items = []topModel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func parseQueryTime(r *http.Request, key string, def time.Time) time.Time {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def.UTC()
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC()
		}
	}
	return def.UTC()
}

func decodeJSONText(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}

	var decoded any
	if err := json.Unmarshal(raw, &decoded); err == nil {
		return decoded
	}
	return string(raw)
}
