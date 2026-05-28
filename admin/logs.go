package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type requestLogDetail struct {
	Ts                 time.Time `json:"ts"`
	RequestID          string    `json:"request_id"`
	APIKeyID           *int      `json:"api_key_id"`
	EndUserID          *string   `json:"end_user_id"`
	ClientModel        *string   `json:"client_model"`
	OutboundModel      *string   `json:"outbound_model"`
	CredentialID       *int      `json:"credential_id"`
	CredentialLabel    *string   `json:"credential_label"`
	ProviderID         *int      `json:"provider_id"`
	ProviderName       *string   `json:"provider_name"`
	ProviderCode       *string   `json:"provider_code"`
	ClientProfile      *string   `json:"client_profile"`
	RequestMode        *string   `json:"request_mode"`
	PromptTokens       *int      `json:"prompt_tokens"`
	CompletionTokens   *int      `json:"completion_tokens"`
	CacheReadTokens    *int      `json:"cache_read_tokens"`
	CacheWriteTokens   *int      `json:"cache_write_tokens"`
	TotalTokens        *int      `json:"total_tokens"`
	CostUSD            *float64  `json:"cost_usd"`
	LatencyMs          *int      `json:"latency_ms"`
	Success            bool      `json:"success"`
	ErrorKind          *string   `json:"error_kind"`
	SearchText         *string   `json:"search_text"`
	IdentityHash       *string   `json:"identity_hash"`
	VirtualClientID    *string   `json:"virtual_client_id"`
	VirtualIP          *string   `json:"virtual_ip"`
	VirtualMAC         *string   `json:"virtual_mac"`
	AffinityHit        *bool     `json:"affinity_hit"`
	StreamFirstChunkMs *int      `json:"stream_first_chunk_ms"`
	StreamChunkCount   *int      `json:"stream_chunk_count"`
	StreamInterrupted  *bool     `json:"stream_interrupted"`
	StreamDoneSent     *bool     `json:"stream_done_sent"`
	RequestBody        any       `json:"request_body"`
	ResponseBody       any       `json:"response_body"`
}

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
	h.getLog(w, r)
}

func (h *Handler) handleLogsRoot(w http.ResponseWriter, r *http.Request) {
	h.listLogs(w, r)
}

func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	limit := queryInt(r, "limit", 50)
	if limit > 500 {
		limit = 500
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, request_id, ts, client_model, credential_id, provider_id,
		       success, latency_ms, prompt_tokens, completion_tokens, cost_usd::float8
		FROM request_logs
		ORDER BY ts DESC
		LIMIT $1
	`, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type logEntry struct {
		ID               int      `json:"id"`
		RequestID        string   `json:"request_id"`
		Ts               time.Time `json:"ts"`
		ClientModel      *string  `json:"client_model"`
		CredentialID     *int     `json:"credential_id"`
		ProviderID       *int     `json:"provider_id"`
		Success          bool     `json:"success"`
		LatencyMs        *int     `json:"latency_ms"`
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		CostUSD          *float64 `json:"cost_usd"`
	}
	var logs []logEntry
	for rows.Next() {
		var l logEntry
		if err := rows.Scan(&l.ID, &l.RequestID, &l.Ts, &l.ClientModel,
			&l.CredentialID, &l.ProviderID, &l.Success, &l.LatencyMs,
			&l.PromptTokens, &l.CompletionTokens, &l.CostUSD); err != nil {
			continue
		}
		logs = append(logs, l)
	}
	writeJSON(w, http.StatusOK, logs)
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

	err = h.db.QueryRow(ctx, `
		SELECT rl.ts, rl.request_id, rl.api_key_id, rl.end_user_id,
		       rl.client_model, rl.outbound_model,
		       rl.credential_id, c.label AS credential_label,
		       rl.provider_id, p.display_name AS provider_name,
		       p.catalog_code AS provider_code,
		       rl.client_profile, rl.request_mode,
		       rl.prompt_tokens, rl.completion_tokens,
		       rl.cache_read_tokens, rl.cache_write_tokens, rl.total_tokens,
		       rl.cost_usd::float8, rl.latency_ms, rl.success, rl.error_kind, rl.search_text,
		       rl.identity_hash, rl.virtual_client_id, rl.virtual_ip, rl.virtual_mac,
		       rl.affinity_hit,
		       rl.stream_first_chunk_ms, rl.stream_chunk_count,
		       rl.stream_interrupted, NULL::boolean AS stream_done_sent,
		       rl.request_body::text, rl.response_body::text
		  FROM request_logs rl
	 LEFT JOIN providers p ON p.id = rl.provider_id
	 LEFT JOIN credentials c ON c.id = rl.credential_id
		 WHERE rl.request_id = $1
		 ORDER BY rl.ts DESC
		 LIMIT 1
	`, requestID).Scan(
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
		&detail.LatencyMs,
		&detail.Success,
		&detail.ErrorKind,
		&detail.SearchText,
		&detail.IdentityHash,
		&detail.VirtualClientID,
		&detail.VirtualIP,
		&detail.VirtualMAC,
		&detail.AffinityHit,
		&detail.StreamFirstChunkMs,
		&detail.StreamChunkCount,
		&detail.StreamInterrupted,
		&detail.StreamDoneSent,
		&requestBodyRaw,
		&responseBodyRaw,
	)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			writeError(w, http.StatusNotFound, "request log not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	detail.RequestBody = decodeJSONText(requestBodyRaw)
	detail.ResponseBody = decodeJSONText(responseBodyRaw)
	writeJSON(w, http.StatusOK, detail)
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
