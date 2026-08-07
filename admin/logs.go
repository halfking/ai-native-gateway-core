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
	Ts                time.Time `json:"ts"`
	RequestID         string    `json:"request_id"`
	APIKeyID          *int      `json:"api_key_id"`
	EndUserID         *string   `json:"end_user_id"`
	ClientModel       *string   `json:"client_model"`
	OutboundModel     *string   `json:"outbound_model"`
	CredentialID      *int      `json:"credential_id"`
	CredentialLabel   *string   `json:"credential_label"`
	ProviderID        *int      `json:"provider_id"`
	ProviderName      *string   `json:"provider_name"`
	ProviderCode      *string   `json:"provider_code"`
	ClientProfile     *string   `json:"client_profile"`
	RequestMode       *string   `json:"request_mode"`
	PromptTokens      *int      `json:"prompt_tokens"`
	CompletionTokens  *int      `json:"completion_tokens"`
	CacheReadTokens   *int      `json:"cache_read_tokens"`
	CacheWriteTokens  *int      `json:"cache_write_tokens"`
	TotalTokens       *int      `json:"total_tokens"`
	CostUSD           *float64  `json:"cost_usd"`
	CostDisplay       *float64  `json:"cost_display"`
	CostCurrency      *string   `json:"cost_currency"`
	LatencyMs         *int      `json:"latency_ms"`
	Success           bool      `json:"success"`
	RequestStatus     string    `json:"request_status"`
	ErrorKind         *string   `json:"error_kind"`
	SearchText        *string   `json:"search_text"`
	IdentityHash      *string   `json:"identity_hash"`
	VirtualClientID   *string   `json:"virtual_client_id"`
	VirtualIP         *string   `json:"virtual_ip"`
	VirtualMAC        *string   `json:"virtual_mac"`
	AffinityHit       *bool     `json:"affinity_hit"`
	RequestChecksum   *string   `json:"request_checksum"`
	ResponseChecksum  *string   `json:"response_checksum"`
	TransformRuleID   *string   `json:"transform_rule_id"`
	EgressProtocol    *string   `json:"egress_protocol"`
	FailureStage      *string   `json:"failure_stage"`
	FailureDetailCode *string   `json:"failure_detail_code"`
	// 2026-06-19 T-NEW-7: the SOLE home for the upstream finish_reason
	// (stop, tool_calls, length, end_turn, function_call, max_tokens, …).
	// Distinct from FailureDetailCode which is now reserved for actual
	// failure / interruption codes.  Populated for BOTH success and
	// failure rows.  See db/migrations/018_upstream_finish_reason.sql.
	UpstreamFinishReason *string `json:"upstream_finish_reason,omitempty"`
	RequestPreview       *string `json:"request_preview"`
	TransformSummary     *string `json:"transform_summary"`
	ResponsePreview      *string `json:"response_preview"`
	StreamFirstChunkMs   *int    `json:"stream_first_chunk_ms"`
	StreamChunkCount     *int    `json:"stream_chunk_count"`
	StreamDoneReceived   *bool   `json:"stream_done_received"`
	StreamInterrupted    *bool   `json:"stream_interrupted"`
	StreamDoneSent       *bool   `json:"stream_done_sent"`
	UsageSource          *string `json:"usage_source"`
	GwSessionID          *string `json:"gw_session_id"`
	GwTaskID             *string `json:"gw_task_id"`
	APIKeyPrefix         *string `json:"api_key_prefix"`
	APIKeyOwnerUser      *string `json:"api_key_owner_user"`
	ApplicationCode      *string `json:"application_code"`
	CanonicalName        *string `json:"canonical_name"`
	// 2026-07-27: 标准模型名(migration 458)。冗余于 CanonicalName (来自
	// models_canonical JOIN),但 denormalize 后 GROUP BY 不再需要 JOIN。
	CanonicalModel *string `json:"canonical_model"`
	// 2026-07-27: 客户端感知扩展。
	AgentName      *string `json:"agent_name"`
	AgentType      *string `json:"agent_type"`
	ClientProtocol *string `json:"client_protocol"`
	ProviderModel  *string `json:"provider_model"`
	TraceSeq       *int    `json:"trace_seq,omitempty"`
	CreditsCharged *int64  `json:"credits_charged"`
	// v3 (2026-06-19) session-level outbound body fields.
	OutboundBody        json.RawMessage `json:"outbound_body,omitempty"`
	OutboundMsgCount    *int            `json:"outbound_msg_count,omitempty"`
	OutboundTokenEst    *int            `json:"outbound_token_est,omitempty"`
	OutboundMsgHashes   json.RawMessage `json:"outbound_msg_hashes,omitempty"`
	CompressionStrategy *string         `json:"compression_strategy,omitempty"`
	CompressionReason   *string         `json:"compression_reason,omitempty"`
	CompressionMeta     json.RawMessage `json:"compression_meta,omitempty"`
	ParentRequestID     *string         `json:"parent_request_id,omitempty"`
	// 2026-07-01: 附件数量 (migration 325)。列表接口返回，
	// 0 表示无附件（omitempty 省略 0，前端按 undefined/falsey 处理为无角标）。
	AttachmentCount int `json:"attachment_count,omitempty"`
	// 2026-08-06: session_titles.title joined on (gw_task_id,
	// COALESCE(NULLIF(gw_session_id,''),'')). Frontend uses this in
	// the request-logs list and detail drawer; nil when no title has
	// been generated or manually set.
	SessionTitle *string `json:"session_title,omitempty"`
}

type requestLogAggregate struct {
	TotalRequests    int64    `json:"total_requests"`
	PromptTokens     *int64   `json:"prompt_tokens"`
	CompletionTokens *int64   `json:"completion_tokens"`
	CacheReadTokens  *int64   `json:"cache_read_tokens"`
	CacheWriteTokens *int64   `json:"cache_write_tokens"`
	TotalTokens      *int64   `json:"total_tokens"`
	CostUSD          *float64 `json:"cost_usd"`
	CreditsCharged   *int64   `json:"credits_charged"`
}

type requestLogDetail struct {
	requestLogRow
	RequestBody  any `json:"request_body"`
	ResponseBody any `json:"response_body"`
	// 2026-07-01: 完整附件元数据数组 (migration 325)。仅详情接口返回，
	// 列表接口为节省载荷不加载。nil / 空数组表示无附件。
	Attachments     json.RawMessage `json:"attachments,omitempty"`
	RoutingAttempts json.RawMessage `json:"routing_attempts,omitempty"`
	RoutingSummary  *string         `json:"routing_summary,omitempty"`
}

const requestLogStatusExpr = `COALESCE(
	NULLIF(rl.request_status, ''),
	CASE
		WHEN rl.success THEN 'success'
		WHEN rl.error_kind IS NOT NULL AND rl.error_kind <> '' THEN 'failure'
		ELSE 'in_progress'
	END
)`

// requestLogsListCols is the slimmed column list for the /api/logs list
// endpoint. It intentionally omits the three JSONB blobs that dominate the
// payload size (50 rows went 444 kB -> 43 kB, -90%, measured 2026-06-24;
// see docs/llm-gateway-go/perf/2026-06-24-request-logs-baseline.md):
//   - outbound_body       (~11.6 kB/row avg, up to 592 kB/row)
//   - outbound_msg_hashes (~0.6 kB/row)
//   - compression_meta    (~0.17 kB/row)
//
// Those three are only needed in the detail drawer and are therefore only
// SELECTed by requestLogsDetailCols (used by getLog). The small siblings
// (outbound_msg_count / outbound_token_est / compression_strategy /
// compression_reason) are kept here because the list UI's compressionLabel
// helper renders badges from them.
const requestLogsListCols = `
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
	-- 2026-06-19 T-NEW-7: split the semantic overload of failure_detail_code.
	-- New column is the SOLE home for the upstream finish_reason.
	rl.upstream_finish_reason,
	rl.request_preview, rl.transform_summary, rl.response_preview,
	rl.stream_first_chunk_ms, rl.stream_chunk_count,
	rl.stream_done_received, rl.stream_interrupted, rl.stream_done_sent,
	rl.usage_source,
	rl.gw_session_id, rl.gw_task_id,
	COALESCE(NULLIF(TRIM(rl.api_key_prefix), ''), NULLIF(TRIM(ak.key_prefix), '')) AS api_key_prefix,
	COALESCE(NULLIF(TRIM(rl.api_key_owner_user), ''), ak.owner_user) AS api_key_owner_user,
	COALESCE(NULLIF(TRIM(rl.application_code), ''), app.code) AS application_code,
	mc.canonical_name,
	rl.canonical_model, -- 2026-07-27: migration 458,标准模型名 denormalize
	rl.agent_name,         -- 2026-07-27: 客户端类型(zcode/claude-code 等)
	rl.agent_type,         -- 2026-07-27: 客户端类型分组(web/cli/api/bot)
	rl.client_protocol,    -- 2026-07-27: openai-chat/anthropic-messages/gemini-generate
	mo_pick.provider_model,
	rl.credits_charged,
	-- v3 (2026-06-19) session-level outbound body summary fields (small,
	-- kept for the list UI's compression badge). The full JSONB bodies are
	-- only loaded by requestLogsDetailCols for the detail drawer.
	rl.outbound_msg_count,
	rl.outbound_token_est,
	rl.compression_strategy,
	rl.compression_reason,
	rl.parent_request_id,
	-- 2026-07-01: 附件数量 (migration 325)。列表只需数量以渲染角标，
	-- 完整的 attachments JSONB 由 requestLogsDetailCols 在详情抽屉加载。
	-- 2026-07-16 fix: attachments 列允许 JSON literal null (非 SQL NULL),
	-- 直接调 jsonb_array_length 会抛 cannot get array length of a scalar
	-- (SQLSTATE 22023),导致整个 SELECT 中途失败、list 接口静默返回 items=[].
	-- 用 jsonb_typeof 守门:只有真正是 array 时才调 array_length,
	-- 其他情况(SQL NULL / JSON null / object / scalar) 一律返回 0.
	CASE WHEN jsonb_typeof(rl.attachments) = 'array'
	     THEN jsonb_array_length(rl.attachments)
	     ELSE 0
	END AS attachment_count,
	-- 2026-08-06: session title. LEFT JOIN session_titles keyed by
	-- (task_id, scoped_session_id) where scoped_session_id falls back to ''
	-- when the request has no gw_session_id, matching the upsert path.
	st.title AS session_title
`

// requestLogsDetailCols extends the list columns with the three JSONB blobs
// needed by the detail drawer (outbound_body / outbound_msg_hashes /
// compression_meta). Used only by getLog (/api/logs/:id).
const requestLogsDetailCols = requestLogsListCols + `,
	rl.outbound_body,
	rl.outbound_msg_hashes,
	rl.compression_meta,
	-- 2026-07-01: 完整附件元数据 JSONB 数组 (migration 325)，
	-- 供详情抽屉的"附件"标签页渲染缩略图/下载链接。
	rl.attachments,
	rl.routing_attempts,
	rl.routing_summary
`

const requestLogsJoins = `
	LEFT JOIN providers p ON p.id = rl.provider_id
	LEFT JOIN credentials c ON c.id = rl.credential_id
	LEFT JOIN api_keys ak ON ak.id = rl.api_key_id
	LEFT JOIN applications app ON app.id = ak.application_id
	LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
	-- 2026-08-06: join session_titles to surface auto-generated /
	-- manually-edited session titles in the request-logs list. The
	-- composite key (task_id, scoped_session_id) matches the unique
	-- index on session_titles. scoped_session_id falls back to '' to
	-- cover request_logs rows where gw_session_id is NULL.
	-- 2026-08-06: previously the JOIN required st.task_id = rl.gw_task_id
	-- exactly, but auto-generated titles were hardcoded to task_id='auto'
	-- while the request's real gw_task_id is typically 'default' — the two
	-- never matched, so the list showed no titles for auto-generated ones.
	-- Now we match on scoped_session_id first and accept st.task_id = 'auto'
	-- (legacy auto-title rows) OR the real gw_task_id (post-fix rows and
	-- manually-edited titles). Empty gw_session_id rows still fall back to ''
	-- and only match legacy notopic titles (which have empty session ids).
	-- 2026-08-06 dedup: a session can hold BOTH a legacy ('auto', S) row and a
	-- manual/post-fix (gw_task_id, S) row (manual PUT writes the real task_id).
	-- A plain OR-join would match both and duplicate the request row in the
	-- list/detail. LATERAL + LIMIT 1 picks exactly one title per row,
	-- preferring the real-task title over the legacy 'auto' marker.
	LEFT JOIN LATERAL (
		SELECT st.title
		FROM session_titles st
		WHERE st.scoped_session_id = COALESCE(NULLIF(rl.gw_session_id, ''), '')
		  AND st.scoped_session_id <> ''
		  AND (st.task_id = rl.gw_task_id OR st.task_id = 'auto')
		ORDER BY CASE WHEN st.task_id = rl.gw_task_id THEN 0 ELSE 1 END
		LIMIT 1
	) st ON TRUE
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
					-- 2026-07-14: client-side columns are persisted lowercase.
					mo.standardized_name = lower(COALESCE(mc.canonical_name, rl.client_model, ''))
					OR mo.canonical_raw_name = lower(COALESCE(rl.outbound_model, rl.client_model, ''))
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
	// 2026-07-01 (migration 325): /api/logs/{request_id}/attachments
	// 列出某请求的附件元数据数组。request_id 本身不会等于 "attachments"，
	// 故以 "/attachments" 后缀作为判别（mux 已保证 remaining 形如 {id}/... ）。
	if strings.HasSuffix(remaining, "/attachments") {
		requestID := strings.TrimSuffix(remaining, "/attachments")
		if id, err := url.PathUnescape(strings.Trim(requestID, "/")); err == nil && id != "" {
			h.listRequestAttachments(w, r, id)
			return
		}
	}
	h.getLog(w, r)
}

func (h *Handler) handleLogsRoot(w http.ResponseWriter, r *http.Request) {
	// 2026-08-06: 与 handleLogs(带尾斜杠) 保持一致。无尾斜杠根路由
	// 此前缺少 nil-db 守卫，DB 不可用(no-DB 模式)时 listLogs 空指针 panic，
	// 恢复中间件将其转成 {"code":"panic"} 500。
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	h.listLogs(w, r)
}

// scanRequestListRow scans a row whose SELECT columns match requestLogsListCols
// (i.e. WITHOUT the outbound_body / outbound_msg_hashes / compression_meta
// JSONB blobs). Used by listLogs to keep the list payload small. The three
// omitted fields stay nil on the returned requestLogRow and, thanks to the
// `omitempty` JSON tags, never reach the client.
func scanRequestListRow(rows interface {
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
		&l.UpstreamFinishReason,
		&l.RequestPreview, &l.TransformSummary, &l.ResponsePreview,
		&l.StreamFirstChunkMs, &l.StreamChunkCount,
		&l.StreamDoneReceived, &l.StreamInterrupted, &l.StreamDoneSent,
		&l.UsageSource,
		&l.GwSessionID, &l.GwTaskID,
		&l.APIKeyPrefix, &l.APIKeyOwnerUser, &l.ApplicationCode,
		&l.CanonicalName,
		&l.CanonicalModel, &l.AgentName, &l.AgentType, &l.ClientProtocol,
		&l.ProviderModel, &l.CreditsCharged,
		// v3 session-level outbound body SUMMARY fields (small, kept for
		// the list UI's compression badge; the full JSONB bodies are only
		// loaded by scanRequestDetailRow for the detail drawer).
		&l.OutboundMsgCount, &l.OutboundTokenEst,
		&l.CompressionStrategy, &l.CompressionReason, &l.ParentRequestID,
		// 2026-07-01: attachment_count (migration 325)。
		&l.AttachmentCount,
		// 2026-08-06: session_titles.title join (see requestLogsJoins).
		&l.SessionTitle,
	}
	if withTraceSeq {
		dest = append(dest, &l.TraceSeq)
	}
	err := rows.Scan(dest...)
	return l, err
}

// (scanRequestLogRow / scanRequestDetailRow / requestLogsSelectCols
// historical aliases removed 2026-06-25: they were unused after the
// list/detail split and tripped golangci-lint's unused check.
//   - List path:  scanRequestListRow  + requestLogsListCols
//   - Detail path: inline Scan in getLog + requestLogsDetailCols
// The detail path inlines its Scan because it pulls two extra text columns
// (request_body / response_body) that are not part of requestLogRow.
// See docs/llm-gateway-go/perf/2026-06-24-request-logs-rollout.md.)

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

	// tenant_admin callers are scoped by the request log's own tenant.
	// API keys may be missing or deleted for historical rows, so requiring
	// ak.tenant_id here would hide valid metadata and make list/detail differ.
	if IsTenantAdmin(r) {
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
		case "in_progress", "success", "failure", "rate_limited":
			clauses = append(clauses, fmt.Sprintf("(%s) = $%d", requestLogStatusExpr, argIdx))
			args = append(args, v)
			argIdx++
		default:
			writeError(w, http.StatusBadRequest, "request_status must be 'in_progress', 'success', 'failure', or 'rate_limited'")
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
				-- 2026-07-14: model_aliases.raw_name is stored lowercase.
				WHERE ma.raw_name = lower(rl.client_model)
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
	traceSeqInner := ""
	traceSeqOuter := ""
	if chrono {
		orderBy = "rl.ts ASC"
		traceSeqInner = ", ROW_NUMBER() OVER (ORDER BY rl.ts ASC) AS trace_seq"
		traceSeqOuter = ", rl.trace_seq"
	}

	where := strings.Join(clauses, " AND ")

// For COUNT, we need the same JOINs to filter by ak.tenant_id for tenant_admin
	// 2026-07-06: 使用视图查询，避免遗漏 hot 表数据（migration 341）
	var count int
	var agg requestLogAggregate
	// Super-admin path keeps the original narrow shape (no api_keys join —
	// api_keys is not needed for the row count or for the SUM aggregate and
	// adding it would inflate the query plan on the busiest list endpoint).
	// Tenant-admin path retains the LEFT JOIN against api_keys so the WHERE
	// clause on rl.tenant_id is matched by the same row set used for COUNT
	// and the SUM aggregate below.
	superCountSQL := "SELECT COUNT(*) FROM request_logs_with_current_month rl WHERE " + where
	tenantCountSQL := "SELECT COUNT(*) FROM request_logs_with_current_month rl LEFT JOIN api_keys ak ON ak.id = rl.api_key_id WHERE " + where
	if IsTenantAdmin(r) {
		if err := h.db.QueryRow(ctx, tenantCountSQL, args...).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
	} else {
		if err := h.db.QueryRow(ctx, superCountSQL, args...).Scan(&count); err != nil {
			writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			return
		}
	}

	// Aggregate totals over the same filter set (independent of pagination).
	// Failure here MUST NOT take down the page; the list endpoint still has
	// valid items / count, so we log and ship zero-valued aggregate fields.
	// The SUM query reuses the same row-shape as the matching COUNT above
	// (super-admin vs tenant-admin) to guarantee the totals describe the
	// same row set the user is paginating over.
	var (
		promptSum     int64
		completionSum int64
		cacheReadSum  int64
		cacheWriteSum int64
		totalTokens   int64
		costSum       float64
		creditsSum    int64
	)
	aggSelect := `
		SELECT
			COALESCE(SUM(rl.prompt_tokens), 0)::bigint,
			COALESCE(SUM(rl.completion_tokens), 0)::bigint,
			COALESCE(SUM(rl.cache_read_tokens), 0)::bigint,
			COALESCE(SUM(rl.cache_write_tokens), 0)::bigint,
			COALESCE(SUM(rl.total_tokens), 0)::bigint,
			COALESCE(SUM(rl.cost_usd), 0)::float8,
			COALESCE(SUM(rl.credits_charged), 0)::bigint
		`
	var aggFromSQL string
	if IsTenantAdmin(r) {
		aggFromSQL = " FROM request_logs_with_current_month rl LEFT JOIN api_keys ak ON ak.id = rl.api_key_id WHERE " + where
	} else {
		aggFromSQL = " FROM request_logs_with_current_month rl WHERE " + where
	}
	if err := h.db.QueryRow(ctx, aggSelect+aggFromSQL, args...).Scan(
		&promptSum, &completionSum, &cacheReadSum, &cacheWriteSum,
		&totalTokens, &costSum, &creditsSum,
	); err != nil {
		slog.Warn("admin listLogs aggregate scan failed",
			"err", err.Error(),
			"count", count,
		)
		// Keep aggregate zero-valued; the page still renders list + count.
	} else {
		agg = requestLogAggregate{
			TotalRequests:    int64(count),
			PromptTokens:     &promptSum,
			CompletionTokens: &completionSum,
			CacheReadTokens:  &cacheReadSum,
			CacheWriteTokens: &cacheWriteSum,
			TotalTokens:      &totalTokens,
			CostUSD:          &costSum,
			CreditsCharged:   &creditsSum,
		}
	}

	offset := (page - 1) * pageSize
	listArgs := append(append([]any{}, args...), pageSize, offset)
	limitIdx := argIdx
	offsetIdx := argIdx + 1

	// Use the slimmed column list (requestLogsListCols) for the list
	// endpoint: omit outbound_body / outbound_msg_hashes / compression_meta
	// JSONB blobs. Those are only loaded by the detail drawer via getLog.
	// 2026-07-06: 使用视图查询，避免遗漏 hot 表数据（migration 341）
	// 2026-08-06 perf: LATERAL mo_pick 会随外层行数逐行执行。原查询在
	// WHERE 之后对全部命中行做 JOIN 再 LIMIT，导致 LATERAL 对数千行各跑
	// 一次 provider_models 全表扫描（实测 9.9s，接口 5s 超时）。改为两段式：
	// 先在内层子查询按 ts 排序 LIMIT 截断到一页（只取 rl.* 窄列），
	// 外层再对少量行做辅助表 LEFT JOIN + LATERAL。
	innerSQL := fmt.Sprintf(`
		SELECT rl.*%s
		FROM request_logs_with_current_month rl
		WHERE %s
		ORDER BY %s
		LIMIT $%d OFFSET $%d
	`, traceSeqInner, where, orderBy, limitIdx, offsetIdx)
	rows, err := h.db.Query(ctx, fmt.Sprintf(`
		SELECT %s%s
		FROM (%s) rl
		%s
		ORDER BY %s
	`, requestLogsListCols, traceSeqOuter, innerSQL, requestLogsJoins, orderBy), listArgs...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	items := make([]requestLogRow, 0)
	scanErrCount := 0
	for rows.Next() {
		l, err := scanRequestListRow(rows, chrono)
		if err != nil {
			// 2026-07-16 fix: 之前这里 silent continue,导致 mid-stream 错误
			// (例如 attachments 列出现 JSON null 触发 jsonb_array_length 抛错)
			// 静默吞掉,客户端拿到 items=[] 但 status=200 误以为查询成功.
			// 现在至少计数+日志,便于排查.
			scanErrCount++
			if scanErrCount <= 3 {
				slog.Warn("admin listLogs scan failed", "err", err.Error())
			}
			continue
		}
		items = append(items, l)
	}
	if err := rows.Err(); err != nil {
		// 2026-07-16 fix: pgx 在游标中途出错时通过 rows.Err() 报告,
		// 必须显式检查,否则会拿到空 items 但无任何日志.
		slog.Warn("admin listLogs rows.Err after iteration", "err", err.Error(), "items_returned", len(items), "count", count)
	} else if scanErrCount > 0 {
		slog.Warn("admin listLogs completed with partial scan failures", "scan_err_count", scanErrCount, "items_returned", len(items), "count", count)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"items":     items,
		"count":     count,
		"aggregate": agg,
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

	// Detail drawer needs the full payload including outbound_body /
	// outbound_msg_hashes / compression_meta, so use requestLogsDetailCols
	// (NOT the slimmed requestLogsListCols used by the list endpoint).
	// 2026-07-21 Ticket #11: Added LEFT JOIN to request_logs_bodies_with_current_month
	// to retrieve full request_body and response_body (moved to separate table in #10).
	// COALESCE ensures backwards compatibility with old data still in request_logs_hot.
	// 2026-07-06: 使用视图查询，避免遗漏 hot 表数据（migration 341）
	// 2026-07-23 BUGFIX: request_id 是唯一标识，JOIN 只需匹配 request_id
	err = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s, 
		       COALESCE(rb.request_body::text, rl.request_body::text) AS request_body,
		       COALESCE(rb.response_body::text, rl.response_body::text) AS response_body
		  FROM request_logs_with_current_month rl
		%s
		  LEFT JOIN request_logs_bodies_with_current_month rb 
		    ON rb.request_id = rl.request_id
		 WHERE rl.request_id = $1
		   AND ($2 OR rl.tenant_id = $3)
		 ORDER BY rl.ts DESC
		 LIMIT 1
	`, requestLogsDetailCols, requestLogsJoins), requestID, !IsTenantAdmin(r), GetTenantID(r)).Scan(
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
		&detail.UpstreamFinishReason,
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
		&detail.CanonicalModel, // 2026-07-27: 标准模型名 (migration 458)
		&detail.AgentName,      // 2026-07-27: 客户端类型
		&detail.AgentType,      // 2026-07-27: 客户端分组
		&detail.ClientProtocol, // 2026-07-27: 客户端协议
		&detail.ProviderModel,
		&detail.CreditsCharged,
		// v3 session-level outbound body summary fields (must mirror
		// requestLogsDetailCols order: list summary fields FIRST, then the
		// three JSONB blobs that only the detail drawer needs).
		&detail.OutboundMsgCount,
		&detail.OutboundTokenEst,
		&detail.CompressionStrategy,
		&detail.CompressionReason,
		&detail.ParentRequestID,
		// 2026-07-01: attachment_count (migration 325). Same COALESCE expression
		// as the list query; re-evaluated here so the detail payload also
		// exposes the count without forcing the client to parse attachments.
		&detail.AttachmentCount,
		// 2026-08-06: session_titles.title (see requestLogsListCols).
		&detail.SessionTitle,
		&detail.OutboundBody,
		&detail.OutboundMsgHashes,
		&detail.CompressionMeta,
		// 2026-07-01: 完整附件元数据 JSONB (migration 325)。
		&detail.Attachments,
		&detail.RoutingAttempts,
		&detail.RoutingSummary,
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
				"detail":     "query failed",
				"db_error":   err.Error(),
				"request_id": requestID,
			},
		})
		return
	}

	detail.RequestBody = decodeJSONText(requestBodyRaw)
	detail.ResponseBody = decodeJSONText(responseBodyRaw)
	// Outbound body: it's already a JSON RawMessage from JSONB scan; convert to
	// a structured payload so the UI can render it as a message list.
	if len(detail.OutboundBody) > 0 {
		detail.OutboundBody = normalizeJSONForAPI(detail.OutboundBody)
	}
	if len(detail.OutboundMsgHashes) > 0 {
		detail.OutboundMsgHashes = normalizeJSONForAPI(detail.OutboundMsgHashes)
	}
	if len(detail.CompressionMeta) > 0 {
		detail.CompressionMeta = normalizeJSONForAPI(detail.CompressionMeta)
	}
	writeJSON(w, http.StatusOK, detail)
}

// normalizeJSONForAPI is a no-op pass-through kept as a hook for future
// transformations (e.g. stripping sensitive fields before sending to the UI).
func normalizeJSONForAPI(raw json.RawMessage) json.RawMessage {
	return raw
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
		FROM request_logs_with_current_month rl
		LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
		LEFT JOIN LATERAL (
			SELECT canonical_id
			FROM model_aliases
			-- 2026-07-14: model_aliases.raw_name is persisted lowercase.
			WHERE raw_name = lower(rl.client_model)
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
