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
	// 2026-08-09: 按模型分组的统计（当未指定具体模型筛选时）
	ByModel []modelAggregate `json:"by_model,omitempty"`
}

// modelAggregate 按单个模型的统计数据
type modelAggregate struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	CostUSD          float64 `json:"cost_usd"`
}

// shouldRunByModel 决定是否执行按模型分组聚合（by_model）。
// 仅在未指定模型筛选、存在请求且时间窗不超限（32 天，保护列表主查询
// 不超时）时运行。2026-08-09 审计发现该决策谓词无测试覆盖，提取为
// 纯函数以便测试。
func shouldRunByModel(modelFilterSpecified bool, count int, timeSpan time.Duration) bool {
	const maxByModelWindow = 32 * 24 * time.Hour
	return !modelFilterSpecified && count > 0 && timeSpan <= maxByModelWindow
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
	 COALESCE(rb.outbound_body, rl.outbound_body),
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
	LEFT JOIN request_logs_bodies_with_current_month rb
		ON rb.request_id = rl.request_id
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
		SELECT CASE WHEN tstate.tenant_id IS NOT NULL THEN COALESCE(tstate.title, '')
		            ELSE st.title END AS title
		FROM session_titles st
		LEFT JOIN public.session_title_states tstate
			ON tstate.tenant_id = rl.tenant_id AND tstate.scoped_session_id = COALESCE(NULLIF(rl.gw_session_id, ''), '')
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
	if remaining == "top-problems" {
		h.handleTopProblems(w, r)
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
		// 'corrected' (CO-2, 2026-08-15) marks estimated rows backfilled
		// with real usage from a later write path.
		if v != "llm" && v != "estimated" && v != "corrected" {
			writeError(w, http.StatusBadRequest, "usage_source must be 'llm', 'estimated' or 'corrected'")
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

	// 2026-08-09: 当未指定具体模型筛选时，提供按模型分组的统计数据。
	// 这让前端可以在统计卡片中展示不同模型的请求次数和token量分布。
	//
	// 2026-08-09 audit fix: 分组必须与列表/详情使用相同的 canonical 归并
	// 语义，否则同一模型会被拆成多张卡（历史分区 canonical_model 可能为
	// NULL，只能通过 canonical_id JOIN models_canonical 解析）。这里通过
	// LEFT JOIN models_canonical mc 取 mc.canonical_name 作为首选模型名，
	// 与 requestLogsJoins 的展示口径一致。
	//
	// 另外分组查询会在 5s 处理器预算内再做一次行集遍历。宽时间窗（超过
	// 一个自然月）或未命中索引的过滤下，为保护列表主查询不超时，这里显式
	// 跳过分组。
	//
	// 2026-08-10: 由 7 天上调至 32 天。前端 thisMonth 预设最大跨度可达
	// 一个月（~31 天），此前 7 天窗口会让"按模板统计"在月度视图下静默为空。
	// 注：上方的 SUM 聚合对本行集本就无条件遍历（任意窗口），月度 GROUP BY
	// 与其成本同量级；此上限仅兜底 thisYear / 自定义超长窗口。
	modelFilterSpecified := strings.TrimSpace(queryString(r, "model")) != "" ||
		queryIntPtr(r, "canonical_id") != nil
	timeSpan := end.Sub(start)
	if shouldRunByModel(modelFilterSpecified, count, timeSpan) {
		// 与 aggFromSQL 同构，额外 JOIN models_canonical 以解析 canonical 名。
		// tenant_admin 路径保留 api_keys JOIN 以匹配同一行集。
		var byModelFromSQL string
		if IsTenantAdmin(r) {
			byModelFromSQL = " FROM request_logs_with_current_month rl" +
				" LEFT JOIN api_keys ak ON ak.id = rl.api_key_id" +
				" LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id WHERE " + where
		} else {
			byModelFromSQL = " FROM request_logs_with_current_month rl" +
				" LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id WHERE " + where
		}
		modelExpr := `COALESCE(mc.canonical_name, rl.canonical_model, rl.client_model, '未知')`
		byModelSQL := `
				SELECT ` + modelExpr + ` AS model,
					COUNT(*) AS requests,
					COALESCE(SUM(rl.prompt_tokens), 0)::bigint AS prompt_tokens,
					COALESCE(SUM(rl.completion_tokens), 0)::bigint AS completion_tokens,
					COALESCE(SUM(rl.total_tokens), 0)::bigint AS total_tokens,
					COALESCE(SUM(rl.cost_usd), 0)::float8 AS cost_usd
				` + byModelFromSQL + `
				GROUP BY ` + modelExpr + `
				ORDER BY requests DESC
				LIMIT 20
			`
		byModelRows, err := h.db.Query(ctx, byModelSQL, args...)
		if err != nil {
			slog.Warn("admin listLogs by_model aggregate failed", "err", err.Error())
		} else {
			defer byModelRows.Close()
			byModel := make([]modelAggregate, 0)
			for byModelRows.Next() {
				var m modelAggregate
				if err := byModelRows.Scan(&m.Model, &m.Requests, &m.PromptTokens, &m.CompletionTokens, &m.TotalTokens, &m.CostUSD); err != nil {
					slog.Warn("admin listLogs by_model scan failed", "err", err.Error())
					continue
				}
				byModel = append(byModel, m)
			}
			// 2026-08-09 audit fix: 游标中途出错必须显式检查，否则会返回
			// 残缺的 by_model 并被前端当作完整分布展示。出错时整体省略。
			if err := byModelRows.Err(); err != nil {
				slog.Warn("admin listLogs by_model rows.Err after iteration", "err", err.Error())
				agg.ByModel = nil
			} else if len(byModel) > 0 {
				agg.ByModel = byModel
			}
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

	// 2026-08-17 BUGFIX: extend outer ctx to 30s for cold-path defense.
	//
	// Why 30s: dashboard "实时请求流 → 点击请求" 是高频路径（24h 内 hot 表命中，
	// 实测 < 100ms），但用户偶尔点"老请求"会触发 Citus columnar scan，
	// 单 ID 查询可能 30s+（heap idx 无法用，planner 必须 ColumnarScan 全表 +
	// 反压 JSONB chunk group）。把外层 ctx 设为 30s 是给冷路径留余量，
	// nginx proxy_read_timeout 默认 1200s 不受影响。
	//
	// 为什么不直接用 r.Context()：保留独立 ctx 让两端都能 slog 监控超时事件
	// （fetchRequestBodies 也会走自己的 hot/cold ctx 而非继承本 ctx）。
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var detail requestLogDetail

	// Detail drawer needs the full payload including outbound_body /
	// outbound_msg_hashes / compression_meta, so use requestLogsDetailCols
	// (NOT the slimmed requestLogsListCols used by the list endpoint).
	// 2026-07-21 Ticket #11: Added LEFT JOIN to request_logs_bodies_with_current_month
	// to retrieve full request_body and response_body (moved to separate table in #10).
	// COALESCE ensures backwards compatibility with old data still in request_logs_hot.
	// 2026-07-06: 使用视图查询，避免遗漏 hot 表数据（migration 341）
	// 2026-07-23 BUGFIX: request_id 是唯一标识，JOIN 只需匹配 request_id
	//
	// 2026-08-17 BUGFIX: dashboard "实时请求流 → 点击请求" 报 "query failed"
	// (HTTP 500 + db_error "timeout: context deadline exceeded")。
	// 根因：request_logs_bodies 的 2026_08 月分区是 Citus columnar (2020 MB)，
	// 不支持 btree 索引，planner 必须 ColumnarScan + 反压 JSONB chunk group，
	// 单 ID 查询 30s+ timeout，把 getLog 5s context 打爆。dashboard 实时流命中的
	// 请求体仍在 request_logs_bodies_hot（heap, <1ms），与 columnar 同走一个视图
	// UNION ALL 被迫全表扫描。
	//
	// 修复策略：把 body JOIN 从主查询剥离，拆成两步：
	//   (1) 主查询只读 request_logs_with_current_month（metadata + 主表内嵌 body）
	//   (2) 若主表内嵌 body 为空（hot 表已迁移 body 列到 sibling 表），
	//       单独查 body：先 request_logs_bodies_hot（idx 命中，<1ms），
	//       找不到再回退到 request_logs_bodies 视图（columnar，慢但可走 20s ctx）。
	// 这样 dashboard 实时流（24h 内请求）走 hot fast path，不会再 5s timeout。
	err = h.db.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s
		  FROM request_logs_with_current_month rl
		%s
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
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "request log not found")
			return
		}
		// 2026-08-17 BUGFIX: log elapsed time on metadata scan failure.
		// Distinct context: metadata query hit columnar scan and exceeded the
		// 30s outer ctx. We distinguish hot-miss vs columnar-cold via the
		// elapsed duration in logs so ops can triage which path to fix next.
		metaElapsed := time.Since(start)
		slog.WarnContext(ctx, "admin getLog scan failed",
			"request_id", requestID,
			"elapsed_ms", metaElapsed.Milliseconds(),
			"error", err.Error())
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
	metaElapsed := time.Since(start)
	if metaElapsed > 1*time.Second {
		slog.InfoContext(ctx, "admin getLog metadata slow",
			"request_id", requestID, "elapsed_ms", metaElapsed.Milliseconds())
	}

	// 2026-08-21: omit_body=1 用于抽屉分阶段首包（先出 meta，再二次拉全量 body）。
	omitBody := r.URL.Query().Get("omit_body") == "1" || r.URL.Query().Get("omit_body") == "true"
	if omitBody {
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
		return
	}

	// 2026-08-17 BUGFIX: 二阶段 body 读取，避开 columnar 扫描（见上方长注释）。
	// 优先 cache（<1ms）；miss → hot（idx 命中 <1ms）；找不到再查 columnar 视图
	// （慢路径，给独立 20s ctx）。
	var bodyErr error
	detail.RequestBody, detail.ResponseBody, bodyErr = h.fetchRequestBodies(ctx, requestID)
	if bodyErr != nil {
		// sql.ErrNoRows（两端都没找到 body）是预期情况 — 不打 WARN 噪音。
		// transport 错误（ctx cancel, conn refused, ...）才打 WARN 便于排查。
		if !errors.Is(bodyErr, sql.ErrNoRows) {
			slog.WarnContext(ctx, "admin getLog body fetch failed",
				"request_id", requestID,
				"total_elapsed_ms", time.Since(start).Milliseconds(),
				"error", bodyErr.Error())
		}
		// body 缺失不应让详情接口 500 — metadata 已成功返回，前端可正常展示
		// 请求/响应以外的所有字段（latency/tokens/cost/model…）。只把 body 置 nil。
		detail.RequestBody = nil
		detail.ResponseBody = nil
	}
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

// fetchRequestBodies 2026-08-17 BUGFIX 二阶段 body 读取：从 request_logs_bodies
// 中拉取单条请求的 request_body/response_body。
//
//   - 阶段 0：内存 LRU+TTL 缓存（rule 36 §1 持久化语义）。
//     dashboard 用户经常"开 → 关 → 再开"同一个 request_id 来回比对；
//     重复点击走 cache < 1ms 而非 5s columnar 扫描。命中条件：5min 内同 ID。
//   - 阶段 1：查 request_logs_bodies_hot (heap, 单条索引 <1ms)，
//     覆盖 dashboard "实时请求流 → 点击请求" 高频路径（24h 内请求都还在 hot）。
//   - 阶段 2：hot 找不到时回退到 request_logs_bodies 视图（含 columnar 月分区，
//     单 ID 扫描可能 30s+），走独立的 20s ctx。cold ctx 直接派生自 r.Context()
//     而非 metadata 用的 30s ctx——确保冷路径有完整 20s 余量，metadata
//     慢也不会拖累 body 读取（rule 11 §14 持续验证）。
//
// 之所以拆出来（而不是 LEFT JOIN 进主查询），是因为 UNION ALL 视图
// request_logs_bodies_with_current_month 会强制 planner 扫 columnar 分区；
// 在 hot-first 分支里提前 LIMIT 1 短路后，columnar 分区永远不会被触达。
//
// 返回值约定：cache hit → (body, body, nil)；hot 命中 → (body, body, nil)；
// cold 命中 → (body, body, nil)；两边都没行 → (nil, nil, sql.ErrNoRows)；
// transport 错误 → (nil, nil, err)。caller 把 body 置 nil 但 metadata 仍 200。
func (h *Handler) fetchRequestBodies(ctx context.Context, requestID string) (requestBody, responseBody any, err error) {
	start := time.Now()

	// 阶段 0: cache hit fast path。命中后立即返回（连 hot 1ms 都省了）。
	if entry, ok := h.bodyFetchCache.Get(requestID); ok {
		if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
			slog.WarnContext(ctx, "admin fetchRequestBodies cache hit unusually slow",
				"request_id", requestID, "elapsed_ms", elapsed.Milliseconds())
		}
		// entry.body / entry.resp 都是 nil 表示"两端都没找到"的 sentinel，
		// 还原为 sql.ErrNoRows 给 caller（保持原有契约）。
		if entry.body == nil && entry.resp == nil {
			return nil, nil, sql.ErrNoRows
		}
		return entry.body, entry.resp, nil
	}

	// 阶段 1: hot (heap, 索引秒级) — 派生自 metadata ctx（30s），
	// 单查询预算 3s；如果 metadata 自己卡到 30s 边界，hot 会跟着取消 — 这是
	// 期望行为（同一接口整体超时）。
	hotCtx, hotCancel := context.WithTimeout(ctx, 3*time.Second)
	defer hotCancel()
	var rb, ob []byte
	row := h.db.QueryRow(hotCtx, `
		SELECT request_body::text, response_body::text
		  FROM request_logs_bodies_hot
		 WHERE request_id = $1
		 LIMIT 1
	`, requestID)
	if scanErr := row.Scan(&rb, &ob); scanErr == nil {
		body, resp := decodeStoredBodyForAdmin(rb), decodeStoredBodyForAdmin(ob)
		h.bodyFetchCache.Put(requestID, body, resp)
		elapsed := time.Since(start)
		if elapsed > 1*time.Second {
			slog.InfoContext(ctx, "admin fetchRequestBodies hot path slow",
				"request_id", requestID, "elapsed_ms", elapsed.Milliseconds())
		}
		return body, resp, nil
	} else if !errors.Is(scanErr, sql.ErrNoRows) {
		// 真正的查询错误（非 not found）— 仍尝试阶段 2
		slog.WarnContext(ctx, "admin fetchRequestBodies hot scan failed",
			"request_id", requestID, "error", scanErr.Error())
	}

	// 阶段 2: columnar 月分区（可能慢，给 20s ctx）
	// 直接派生自请求 ctx（不是 metadata ctx），确保 metadata 卡顿不会拖累 body。
	// http.Request.Context() 通常由 nginx proxy_read_timeout（默认 1200s）兜底。
	coldCtx, coldCancel := context.WithTimeout(ctx, 20*time.Second)
	defer coldCancel()
	row = h.db.QueryRow(coldCtx, `
		SELECT request_body::text, response_body::text
		  FROM request_logs_bodies_with_current_month
		 WHERE request_id = $1
		 LIMIT 1
	`, requestID)
	if scanErr := row.Scan(&rb, &ob); scanErr != nil {
		elapsed := time.Since(start)
		// 两端都没找到 → 缓存 sql.ErrNoRows sentinel（5min 内重复查询直接命中）
		if errors.Is(scanErr, sql.ErrNoRows) {
			h.bodyFetchCache.Put(requestID, nil, nil)
			slog.InfoContext(ctx, "admin fetchRequestBodies no body anywhere",
				"request_id", requestID, "elapsed_ms", elapsed.Milliseconds())
			return nil, nil, sql.ErrNoRows
		}
		if errors.Is(scanErr, context.DeadlineExceeded) {
			slog.WarnContext(ctx, "admin fetchRequestBodies cold path timeout",
				"request_id", requestID, "elapsed_ms", elapsed.Milliseconds())
		}
		// transport-class error（ctx cancel, conn refused, ...）不缓存 — 让
		// 下一次请求能 retry（rule 22 §4 错误缓存防抖）。
		return nil, nil, scanErr
	}
	body, resp := decodeStoredBodyForAdmin(rb), decodeStoredBodyForAdmin(ob)
	h.bodyFetchCache.Put(requestID, body, resp)
	elapsed := time.Since(start)
	slog.InfoContext(ctx, "admin fetchRequestBodies cold path hit",
		"request_id", requestID, "elapsed_ms", elapsed.Milliseconds())
	return body, resp, nil
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

// parseQueryTimeStrict is like parseQueryTime but returns ok=false when a
// non-empty value fails to parse, so callers can reject malformed timestamps
// with a 400 instead of silently falling back to the default (which would
// answer a different time range than the client requested).
func parseQueryTimeStrict(r *http.Request, key string, def time.Time) (time.Time, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return def.UTC(), true
	}
	for _, layout := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05"} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.UTC(), true
		}
	}
	return def.UTC(), false
}

func decodeStoredBodyForAdmin(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	body := string(raw)
	if env, head := unwrapBody(&body); env != nil {
		if head != nil && strings.TrimSpace(*head) != "" {
			decoded := decodeJSONText([]byte(*head))
			if _, ok := decoded.(string); !ok {
				return decoded
			}
		}
		return map[string]any{
			"messages": []map[string]any{{
				"role":    "gateway",
				"content": env.displayNote(),
			}},
			"_gw_body_summary_display": map[string]any{
				"mode":           env.Mode,
				"bytes":          env.Bytes,
				"head_truncated": env.HeadTruncated,
			},
		}
	}
	return decodeJSONText(raw)
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
