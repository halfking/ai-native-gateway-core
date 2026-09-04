package admin

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/kaixuan/llm-gateway-go/domains/sessiondigest"
)

// session_turns_v2.go — V2 会话详情端点实现（2026-08-07）。
//
// 供新前端 SessionDetailPage（web/src/views/admin/SessionDetailPage.vue）
// 使用，数据源为 gateway.session_* 表（V2 sessions 影子写）。挂载于既有
// /api/admin/sessions/<id>/* 子路由（handleSessionSubrouter），与 V1 子路由
// 并存，前端路径不变（web/src/api/sessions_v2.ts）。
//
// 端点：
//   GET  /api/admin/sessions/<id>/turns            → 轮次列表（cursor 分页）
//   GET  /api/admin/sessions/<id>/turns/<n>        → 单轮详情
//   GET  /api/admin/sessions/<id>/snapshot         → 会话快照（标题/总结/汇总）
//   POST /api/admin/sessions/<id>/instant-summary  → 即时总结（LLM）
//
// 鉴权由 handleSessionSubrouter 外层 admin() 中间件保证；cursor 签名使用
// Handler.secret（无硬编码 key）。

const (
	defaultTurnsListLimit = 50
	maxTurnsListLimit     = 200
	// Child requests are metadata-only and bounded so a single pathological
	// parent cannot expand one page without limit.
	maxChildRequestsPerParent = 100
	maxChildRequestsPerPage   = 1000
)

// sessionTurnsDB 是轮次读路径实际需要的数据库方法子集（与
// sessionDetailV2DB 同款接口缝模式）：*pgxpool.Pool 在生产隐式满足，
// pgxmock.PgxPoolIface 在集成测试（turn_digest_integration_test.go）注入。
// 只暴露 Query/QueryRow —— 读路径没有副作用，接口收缩让越界调用编译失败。
type sessionTurnsDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// serveSessionTurnSubroute 处理 action 以 "turns/" 开头的子路径：
//
//	turns/<turnNo>                          → 单轮详情
//	turns/<turnNo>/attachments/<id>/url     → 附件签名 URL（未接线则 404）
func (h *Handler) serveSessionTurnSubroute(w http.ResponseWriter, r *http.Request, sessionID, action string) {
	rest := strings.TrimPrefix(action, "turns/")
	seg := strings.SplitN(rest, "/", 2)
	turnNoStr := seg[0]
	if len(seg) == 1 {
		h.serveSessionTurnDetail(w, r, sessionID, turnNoStr)
		return
	}
	if len(seg) == 2 {
		parts := strings.Split(seg[1], "/")
		if len(parts) == 3 && parts[0] == "attachments" && parts[2] == "url" && r.Method == http.MethodGet {
			http.NotFound(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// serveSessionTurnsList 返回会话轮次列表（按 turn_no 倒序，cursor 分页）。
func (h *Handler) serveSessionTurnsList(w http.ResponseWriter, r *http.Request, sessionID string) {
	serveSessionTurnsListDB(h.db, h.secret, w, r, sessionID)
}

// serveSessionTurnsListDB 是 serveSessionTurnsList 的接口缝版本：db 与
// cursor 签名 key 显式传入，集成测试用 pgxmock 注入而不构造完整 Handler。
func serveSessionTurnsListDB(db sessionTurnsDB, secret string, w http.ResponseWriter, r *http.Request, sessionID string) {
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	limit := defaultTurnsListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxTurnsListLimit {
			limit = n
		}
	}
	beforeTurnNo := int(^uint(0) >> 1)
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := validateCursor(encoded, []byte(secret), tenantID, sessionID)
		if err != nil {
			if errors.Is(err, errCursorMismatch) {
				writeError(w, http.StatusBadRequest, "cursor mismatch")
			} else {
				writeError(w, http.StatusBadRequest, "invalid cursor")
			}
			return
		}
		beforeTurnNo = decoded.TurnNo
	}

	rows, err := db.Query(r.Context(), `
		SELECT t.turn_no, t.ts, COALESCE(t.title,''), COALESCE(t.summary,''),
		       COALESCE(t.prompt_tokens,0), COALESCE(t.completion_tokens,0), COALESCE(t.cost_usd,0),
		       COALESCE(t.model,''), COALESCE(t.provider,''), COALESCE(t.status_code,0),
		       COALESCE(t.submit_mode,''), COALESCE(t.injection_verdict,''), COALESCE(t.output_verdict,''),
		       COALESCE(t.attachment_count,0), t.request_id,
		       COALESCE(t.cache_read_tokens,0), COALESCE(t.latency_ms,0), COALESCE(t.success,FALSE),
			   t.error_kind, t.compression_applied, t.compression_tokens_saved, t.digest,
			   b.request_delta, b.response_delta
		FROM public.session_turns_with_current_month t
		LEFT JOIN public.session_bodies_unified b
		  ON b.tenant_id=t.tenant_id AND b.session_id=t.session_id
		 AND b.turn_no=t.turn_no AND b.request_id=t.request_id
		WHERE t.tenant_id=$1 AND t.session_id=$2 AND t.turn_no < $3
		ORDER BY t.turn_no DESC LIMIT $4`, tenantID, sessionID, beforeTurnNo, limit+1)
	if err != nil {
		slog.ErrorContext(r.Context(), "serveSessionTurnsList query failed",
			"session_id", sessionID, "tenant_id", tenantID,
			"before_turn_no", beforeTurnNo, "limit", limit, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "query turns failed")
		return
	}
	defer rows.Close()

	items := make([]TurnListItem, 0, limit)
	for rows.Next() {
		var (
			it                          TurnListItem
			requestID                   string
			errorKind                   *string // nullable in DB (migration 526)
			cacheReadTokens, latencyMs  int
			success, compressionApplied bool
			compressionTokensSaved      *int
			persistedDigestRaw          []byte
			requestRaw, responseRaw     []byte
		)
		if err := rows.Scan(&it.TurnNo, &it.Ts, &it.Title, &it.Summary, &it.RequestTokens,
			&it.ResponseTokens, &it.CostUSD, &it.Model, &it.Provider, &it.StatusCode,
			&it.SubmitMode, &it.InjectionVerdict, &it.OutputVerdict, &it.AttachmentCount,
			&requestID, &cacheReadTokens, &latencyMs, &success, &errorKind,
			&compressionApplied, &compressionTokensSaved, &persistedDigestRaw, &requestRaw, &responseRaw); err != nil {
			writeError(w, http.StatusInternalServerError, "scan turn failed")
			return
		}
		request := decodeStoredJSON("request_delta", requestID, requestRaw)
		response := decodeStoredJSON("response_delta", requestID, responseRaw)
		meta := map[string]any{
			"prompt_tokens": it.RequestTokens, "completion_tokens": it.ResponseTokens,
			"cost_usd": it.CostUSD, "cache_read_tokens": cacheReadTokens,
			"latency_ms": latencyMs, "status_code": it.StatusCode,
			"success": success, "error_kind": stringPtrValue(errorKind),
		}
		governance := map[string]any{
			"submit_mode": it.SubmitMode, "injection_verdict": it.InjectionVerdict,
			"output_verdict": it.OutputVerdict, "compression_applied": compressionApplied,
			"compression_tokens_saved": intPtrValue(compressionTokensSaved),
		}
		it.Digest = persistedDigestOrFallback(persistedDigestRaw, request, response, meta, governance)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "iterate turns failed")
		return
	}

	hasMore := len(items) > limit
	var nextCursor string
	if hasMore {
		items = items[:limit]
		nextCursor, _ = encodeCursor(cursorPayload{
			TenantID: tenantID, SessionID: sessionID,
			TurnNo: items[len(items)-1].TurnNo, TS: time.Now(),
		}, []byte(secret))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID, "turns": items, "has_more": hasMore, "next_cursor": nextCursor,
	})
}

// turnDetailV2Response 是 serveSessionTurnDetail 返回给前端 SessionTurnDrawer 的嵌套形状。
// 与 SessionTurnV2 的扁平结构不同，前端五个 tab 分别消费 request/response/meta/governance/attachments。
type turnDetailV2Response struct {
	Model       string         `json:"model"`
	CostUSD     float64        `json:"cost_usd"`
	Request     any            `json:"request"`
	Response    any            `json:"response"`
	Compression map[string]any `json:"compression,omitempty"`
	Meta        map[string]any `json:"meta"`
	Governance  map[string]any `json:"governance"`
	Attachments []attachmentV2 `json:"attachments"`
	Digest      *TurnDigest    `json:"digest,omitempty"`
}

// buildCompressionDiagnosticsV2 keeps gateway-derived outbound state separate
// from the client request shown in the session detail view. The outbound body
// remains available for privileged diagnostics and is still the exact body
// used for compression/session recovery.
func buildCompressionDiagnosticsV2(
	requestID string,
	applied bool,
	strategy *string,
	tokensSaved *int,
	metaRaw, outboundRaw []byte,
) map[string]any {
	if !applied && strategy == nil && tokensSaved == nil && len(metaRaw) == 0 && len(outboundRaw) == 0 {
		return nil
	}

	out := make(map[string]any)
	if applied {
		out["applied"] = true
	}
	if strategy != nil && *strategy != "" {
		out["strategy"] = *strategy
	}
	if tokensSaved != nil {
		out["tokens_saved"] = *tokensSaved
	}
	if len(metaRaw) > 0 {
		out["meta"] = decodeStoredJSON("compression_meta", requestID, metaRaw)
	}
	if len(outboundRaw) > 0 {
		out["outbound_body"] = decodeStoredJSON("outbound_body", requestID, outboundRaw)
	}
	return out
}

// attachmentV2 是前端 SessionTurnDrawer 「附件」tab 消费的形状。
// 会话正文里的 AttachmentRef 没有 att_id，复用 object_key 作为稳定标识。
type attachmentV2 struct {
	AttID  string `json:"att_id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	MIME   string `json:"mime,omitempty"`
	Object string `json:"object,omitempty"`
}

// serveSessionTurnDetail 返回单轮详情（session_turns + session_bodies JOIN），
// 输出嵌套结构供前端 SessionTurnDrawer 五 tab 消费。
func (h *Handler) serveSessionTurnDetail(w http.ResponseWriter, r *http.Request, sessionID, turnNoStr string) {
	serveSessionTurnDetailDB(h.db, w, r, sessionID, turnNoStr)
}

// serveSessionTurnDetailDB 是 serveSessionTurnDetail 的接口缝版本：集成测试
// 用 pgxmock 注入（见 turn_digest_integration_test.go），生产路径经 Handler
// 包装调用，行为完全一致。
func serveSessionTurnDetailDB(db sessionTurnsDB, w http.ResponseWriter, r *http.Request, sessionID, turnNoStr string) {
	turnNo, err := strconv.Atoi(turnNoStr)
	if err != nil || turnNo <= 0 {
		writeError(w, http.StatusBadRequest, "invalid turn_no")
		return
	}
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)

	query := `
		SELECT
			t.turn_no, t.request_id, t.ts,
			t.submit_mode, t.compression_applied, t.compression_strategy,
			t.compression_meta, t.compression_tokens_saved,
			t.injection_verdict, t.output_verdict,
			t.model, t.provider,
			t.prompt_tokens, t.completion_tokens, t.cache_read_tokens, t.cache_write_tokens,
			t.cost_usd, t.latency_ms, t.status_code, t.success, t.error_kind,
				t.source_kind, t.quality, t.digest,
				t.t0_arrived_at, t.t1_total_enqueued_at, t.t2_total_dequeued_at,
				t.t3_model_enqueued_at, t.t4_model_dequeued_at, t.t5_cred_enqueued_at,
				t.t6_cred_dequeued_at, t.t7_forward_start_at, t.t8_response_start_at,
				t.t9_response_end_at,
				b.request_delta, b.response_delta, b.outbound_body,
				b.request_attachments, b.response_attachments
		FROM public.session_turns_with_current_month t
			LEFT JOIN public.session_bodies_unified b
				ON t.tenant_id = b.tenant_id
				AND t.session_id = b.session_id
				AND t.turn_no = b.turn_no
				AND t.request_id = b.request_id
			WHERE t.session_id = $1 AND t.tenant_id = $2 AND t.turn_no = $3
		LIMIT 1`
	var (
		turnNoOut                                                                   int
		requestID, submitMode, injectionVerdict, outputVerdict, sourceKind, quality string
		ts                                                                          time.Time
		compressionApplied                                                          bool
		compressionStrategy                                                         *string
		compressionMetaRaw, requestDeltaRaw, responseDeltaRaw, outboundBodyRaw      []byte
		persistedDigestRaw                                                          []byte
		compressionTokensSaved                                                      *int

		model, provider, errorKind                                                               *string
		promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens                        *int
		costUSD                                                                                  *float64
		latencyMs, statusCode                                                                    *int
		success                                                                                  *bool
		requestAttachmentsRaw, responseAttachmentsRaw                                            []byte
		t0ArrivedAt, t1TotalEnqueuedAt, t2TotalDequeuedAt, t3ModelEnqueuedAt, t4ModelDequeuedAt  *time.Time
		t5CredEnqueuedAt, t6CredDequeuedAt, t7ForwardStartAt, t8ResponseStartAt, t9ResponseEndAt *time.Time
	)
	err = db.QueryRow(r.Context(), query, sessionID, tenantID, turnNo).Scan(
		&turnNoOut, &requestID, &ts,
		&submitMode, &compressionApplied, &compressionStrategy,
		&compressionMetaRaw, &compressionTokensSaved,
		&injectionVerdict, &outputVerdict,
		&model, &provider,
		&promptTokens, &completionTokens, &cacheReadTokens, &cacheWriteTokens,
		&costUSD, &latencyMs, &statusCode, &success, &errorKind,
		&sourceKind, &quality, &persistedDigestRaw,
		&t0ArrivedAt, &t1TotalEnqueuedAt, &t2TotalDequeuedAt,
		&t3ModelEnqueuedAt, &t4ModelDequeuedAt, &t5CredEnqueuedAt,
		&t6CredDequeuedAt, &t7ForwardStartAt, &t8ResponseStartAt,
		&t9ResponseEndAt,
		&requestDeltaRaw, &responseDeltaRaw, &outboundBodyRaw,
		&requestAttachmentsRaw, &responseAttachmentsRaw)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if err.Error() == "no rows in result set" {
			writeError(w, http.StatusNotFound, "turn not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "query turn failed")
		return
	}

	request := decodeStoredJSON("request_delta", requestID, requestDeltaRaw)
	response := decodeStoredJSON("response_delta", requestID, responseDeltaRaw)
	meta := map[string]any{
		"turn_no":            turnNoOut,
		"request_id":         requestID,
		"ts":                 ts,
		"provider":           stringPtrValue(provider),
		"prompt_tokens":      intPtrValue(promptTokens),
		"completion_tokens":  intPtrValue(completionTokens),
		"cache_read_tokens":  intPtrValue(cacheReadTokens),
		"cache_write_tokens": intPtrValue(cacheWriteTokens),
		"latency_ms":         intPtrValue(latencyMs),
		"status_code":        intPtrValue(statusCode),
		"success":            boolPtrValue(success),
		"error_kind":         stringPtrValue(errorKind),
		"source_kind":        sourceKind,
		"quality":            quality,
	}
	if costUSD != nil {
		meta["cost_usd"] = *costUSD
	}
	for key, value := range map[string]*time.Time{
		"t0_arrived_at": t0ArrivedAt, "t1_total_enqueued_at": t1TotalEnqueuedAt,
		"t2_total_dequeued_at": t2TotalDequeuedAt, "t3_model_enqueued_at": t3ModelEnqueuedAt,
		"t4_model_dequeued_at": t4ModelDequeuedAt, "t5_cred_enqueued_at": t5CredEnqueuedAt,
		"t6_cred_dequeued_at": t6CredDequeuedAt, "t7_forward_start_at": t7ForwardStartAt,
		"t8_response_start_at": t8ResponseStartAt, "t9_response_end_at": t9ResponseEndAt,
	} {
		if value != nil {
			meta[key] = value
		}
	}
	meta["timing_semantics"] = "request_level_last_write"
	governance := map[string]any{
		"submit_mode":              submitMode,
		"compression_applied":      compressionApplied,
		"compression_strategy":     stringPtrValue(compressionStrategy),
		"compression_tokens_saved": intPtrValue(compressionTokensSaved),
		"injection_verdict":        injectionVerdict,
		"output_verdict":           outputVerdict,
	}
	resp := turnDetailV2Response{
		Request:     request,
		Response:    response,
		Compression: buildCompressionDiagnosticsV2(requestID, compressionApplied, compressionStrategy, compressionTokensSaved, compressionMetaRaw, outboundBodyRaw),
		Meta:        meta,
		Governance:  governance,
		Attachments: buildTurnAttachments(requestID, requestAttachmentsRaw, responseAttachmentsRaw),
		Digest:      persistedDigestOrFallback(persistedDigestRaw, request, response, meta, governance),
	}
	if model != nil {
		resp.Model = *model
	}
	if costUSD != nil {
		resp.CostUSD = *costUSD
	}
	writeJSON(w, http.StatusOK, resp)
}

// digestFallbackReason classifies why persistedDigestOrFallback could not use
// the persisted digest envelope. Used as a low-cardinality metric + log label.
type digestFallbackReason string

const (
	digestFallbackMissing   digestFallbackReason = "missing"   // NULL column (pre-migration-456 turn)
	digestFallbackMalformed digestFallbackReason = "malformed" // non-empty but invalid JSON
	digestFallbackVersion   digestFallbackReason = "version"   // future/unsupported schema_version or algorithm_version
)

// digestFallbackTotal counts turns served through the on-the-fly digest
// rebuild instead of the persisted session_turns.digest envelope. A
// persistent non-zero rate means new writes are failing to persist digests
// (see sessiondigest.Marshal in session_writer_v2.go) or rows pre-date
// migration 456. Unlabeled by request/session per GW-00 cardinality rules.
var digestFallbackTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "llmgw_session_turn_digest_fallback_total",
		Help: "Session turns whose admin digest was rebuilt on the fly instead of read from the persisted sessiondigest envelope (label = why)",
	},
	[]string{"reason"},
)

// persistedDigestOrFallback prefers the persisted, versioned digest envelope
// and falls back to rebuilding one from the turn's stored bodies. The fallback
// is not free: it re-parses request/response JSON on the request hot path and
// silently masks rows whose persisted digest is corrupt or of an unsupported
// schema version. Both outcomes are logged (request-scoped) and counted
// (digestFallbackTotal) so operators can tell "old row, never had a digest"
// apart from "writer bug producing unusable digests".
func persistedDigestOrFallback(raw []byte, request, response any, meta, governance map[string]any) *TurnDigest {
	persisted, err := sessiondigest.Unmarshal(raw)
	if err == nil && persisted != nil {
		return turnDigestFromPayload(persisted.Payload)
	}
	reason := digestFallbackMissing
	switch {
	case err != nil && len(raw) > 0:
		// Unmarshal distinguishes malformed JSON ("json: cannot ..." /
		// "unexpected end of JSON input") from a version mismatch
		// ("unsupported digest version"). Keep the buckets coarse.
		if strings.Contains(err.Error(), "unsupported digest version") {
			reason = digestFallbackVersion
		} else {
			reason = digestFallbackMalformed
		}
	case len(raw) > 0 && string(raw) == "null":
		// Explicit JSON null: writers emit this when Build returns nil.
		// Counted as missing — the envelope legitimately does not exist.
		reason = digestFallbackMissing
	}
	digestFallbackTotal.WithLabelValues(string(reason)).Inc()
	slog.Warn("session turn digest fallback to on-the-fly rebuild",
		"reason", string(reason),
		"request_id", firstStringValue(meta, "request_id"),
		"turn_no", firstIntValue(meta, "turn_no"),
		"raw_len", len(raw),
		"unmarshal_error", errString(err))
	return buildTurnDigest(request, response, meta, governance)
}

func firstStringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func firstIntValue(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func turnDigestFromPayload(d sessiondigest.Digest) *TurnDigest {
	out := &TurnDigest{
		UserInput:       d.UserInput,
		AssistantOutput: d.AssistantOutput,
		Metrics: TurnMetrics{
			TokensUsed: d.Metrics.TokensUsed, Cost: d.Metrics.Cost, LatencyMs: d.Metrics.LatencyMs,
			CacheHitRate: d.Metrics.CacheHitRate, CompressionRate: d.Metrics.CompressionRate,
		},
	}
	for _, event := range d.Events {
		out.Events = append(out.Events, TurnEvent{Type: event.Type, Category: event.Category, Message: event.Message})
	}
	if d.ToolUsage != nil {
		out.ToolUsage = &ToolUsageSummary{ToolCallCount: d.ToolUsage.ToolCallCount, ToolsUsed: append([]string(nil), d.ToolUsage.ToolsUsed...)}
	}
	return out
}

// buildTurnAttachments 把 session_bodies 的 request/response_attachments jsonb 数组
// 转换为前端 SessionTurnDrawer 期望的 {att_id, name, size} 列表。
// 去重规则：同一 object_key/sha256 在 request/response 都出现时只保留第一个。
//
// V2 AttachmentRef 的字段名见 domains/session/v2.AttachmentRef：name / object_key /
// mime_type / size_bytes / sha256。本地定义 attachmentRefShape 以避免 admin 包对
// domains 的反向依赖（admin → domains 单向）。
func buildTurnAttachments(requestID string, raws ...[]byte) []attachmentV2 {
	type attachmentRefShape struct {
		Name      string `json:"name"`
		ObjectKey string `json:"object_key"`
		MIMEType  string `json:"mime_type"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	}
	seen := map[string]struct{}{}
	out := []attachmentV2{}
	for _, raw := range raws {
		if len(raw) == 0 || string(raw) == "null" {
			continue
		}
		var refs []attachmentRefShape
		if err := json.Unmarshal(raw, &refs); err != nil {
			var one attachmentRefShape
			if err2 := json.Unmarshal(raw, &one); err2 == nil && (one.Name != "" || one.ObjectKey != "") {
				refs = []attachmentRefShape{one}
			} else {
				continue
			}
		}
		for _, r := range refs {
			key := r.ObjectKey
			if key == "" {
				key = r.SHA256
			}
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, attachmentV2{
				AttID:  key,
				Name:   r.Name,
				Size:   r.SizeBytes,
				MIME:   r.MIMEType,
				Object: r.ObjectKey,
			})
		}
	}
	return out
}

func stringPtrValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// turnBodyItem 是 serveSessionTurnsBodies 返回的单轮正文投影。
// 正文来自 public.session_bodies（V2 增量存储），每轮只保存「本轮新增」
// 的请求消息 + 本轮回复 + 实际发往 LLM 的 outbound，避免 request_logs
// 那种每轮全量套娃。全面切到 session_turns 模式后，这是会话正文的唯一来源。
type turnBodyItem struct {
	TurnNo        int    `json:"turn_no"`
	RequestID     string `json:"request_id"`
	RequestDelta  any    `json:"request_delta"`
	ResponseDelta any    `json:"response_delta"`
	OutboundBody  any    `json:"outbound_body"`
}

// serveSessionTurnsBodies 返回会话所有轮次的正文（按 turn_no 升序），
// 数据源为 public.session_bodies。前端「会话轮次」视图据此渲染每轮用户指令
// 摘要与单轮消息过滤；request_logs 正文派生作为空数据时的兜底。
//
// GET /api/admin/sessions/<id>/turns/bodies?limit=
func (h *Handler) serveSessionTurnsBodies(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	limit := defaultTurnsListLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= maxTurnsListLimit {
			limit = n
		}
	}

	rows, err := h.db.Query(r.Context(), `
		SELECT turn_no, request_id, request_delta, response_delta, outbound_body
		FROM public.session_bodies_unified
		WHERE tenant_id = $1 AND session_id = $2
		ORDER BY turn_no ASC
		LIMIT $3`, tenantID, sessionID, limit+1)
	if err != nil {
		slog.ErrorContext(r.Context(), "serveSessionTurnsBodies query failed",
			"session_id", sessionID, "tenant_id", tenantID, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "query turn bodies failed: "+err.Error())
		return
	}
	defer rows.Close()

	items := make([]turnBodyItem, 0, limit)
	for rows.Next() {
		var (
			turnNo                                             int
			requestID                                          string
			requestDeltaRaw, responseDeltaRaw, outboundBodyRaw []byte
		)
		if err := rows.Scan(&turnNo, &requestID, &requestDeltaRaw, &responseDeltaRaw, &outboundBodyRaw); err != nil {
			writeError(w, http.StatusInternalServerError, "scan turn body failed")
			return
		}
		items = append(items, turnBodyItem{
			TurnNo:        turnNo,
			RequestID:     requestID,
			RequestDelta:  decodeStoredJSON("request_delta", requestID, requestDeltaRaw),
			ResponseDelta: decodeStoredJSON("response_delta", requestID, responseDeltaRaw),
			OutboundBody:  decodeStoredJSON("outbound_body", requestID, outboundBodyRaw),
		})
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "iterate turn bodies failed")
		return
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session_id": sessionID,
		"turns":      items,
		"has_more":   hasMore,
	})
}

func intPtrValue(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func boolPtrValue(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}

// sessionSnapshotV2 是会话快照的响应形状，前端 SessionSummaryBar 依赖这些字段。
// 数据源为 public.sessions（V2 会话快照，migration 456 添加了
// title/summary/summary_generated_at 列，migration 430 有 last_model/last_provider）。
type sessionSnapshotV2 struct {
	SessionID          string     `json:"session_id"`
	TenantID           string     `json:"tenant_id"`
	Title              string     `json:"title"`
	Summary            string     `json:"summary"`
	SummaryGeneratedAt *time.Time `json:"summary_generated_at,omitempty"`
	TotalTurns         int        `json:"total_turns"`
	TotalTokens        int        `json:"total_tokens"`
	TotalCostUSD       float64    `json:"total_cost_usd"`
	LastTurnNo         int        `json:"last_turn_no"`
	LastModel          *string    `json:"last_model,omitempty"`
	LastProvider       *string    `json:"last_provider,omitempty"`
	LastRequestSummary string     `json:"last_request_summary,omitempty"`
	LastResponseSummary string    `json:"last_response_summary,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ClosedAt           *time.Time `json:"closed_at,omitempty"`
	Status             string     `json:"status"`
	TaskType           string     `json:"task_type,omitempty"`
	ClientType         string     `json:"client_type,omitempty"`
	Topic              string     `json:"topic,omitempty"`
	Intent             string     `json:"intent,omitempty"`
	UserTags           []string   `json:"user_tags,omitempty"`
	// SessionAnalysis 是 migration 567 的 session_analysis_metadata 读侧投影
	// （LEFT JOIN LATERAL 命中时非空）。与 turns_sessions / session_detail_v2
	// 的 SessionAnalysis 字段保持同一形状，前端 SessionSummaryBar 可直接复用。
	SessionAnalysis *SessionAnalysisView `json:"session_analysis,omitempty"`
}

// serveSessionSnapshot 返回会话快照（取自 public.sessions）。
//
// 标题/摘要采用与 GET /api/admin/turns/sessions 一致的级联回退：
// public.sessions → session_titles（auto_title_generator）→
// session_summaries（auto_summary_generator），否则会话详情页顶部的摘要栏
// 在 instant-summary 端点被手动触发前会一直显示空白。
func (h *Handler) serveSessionSnapshot(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	var snap sessionSnapshotV2
	// 2026-08-26: 增加 LEFT JOIN LATERAL session_analysis_metadata。快照是
	// SessionSummaryBar 的拉取入口，把 SessionAnalysis 一起带回避免额外的
	// /api/admin/session-analytics/... 往返。
	query := `
			SELECT s.session_id, s.tenant_id,
			       CASE WHEN tstate.tenant_id IS NOT NULL THEN COALESCE(tstate.title, '')
			            ELSE COALESCE(NULLIF(s.title, ''), st.title, ss.title, '') END AS title,
			       COALESCE(NULLIF(s.summary, ''), ss.summary, '') AS summary,
			       s.summary_generated_at, s.total_turns, s.total_tokens, s.total_cost_usd,
			       s.last_turn_no, s.last_model, s.last_provider,
			       COALESCE(s.last_request_summary, '') AS last_request_summary,
			       COALESCE(s.last_response_summary, '') AS last_response_summary,
			       s.created_at, s.updated_at, s.closed_at, COALESCE(s.status, 'active') AS status,
			       COALESCE(s.task_type, '') AS task_type,
			       COALESCE(s.client_type, '') AS client_type,
			       COALESCE(s.topic, '') AS topic,
			       COALESCE(s.intent, '') AS intent,
			       COALESCE(s.user_tags, ARRAY[]::text[]) AS user_tags,
			       sam.status, sam.schema_version, sam.input_hash,
			       sam.source_task_id, sam.updated_at, sam.payload
			FROM public.sessions s
			LEFT JOIN session_dim sd
				ON sd.gw_session_id = s.session_id AND sd.tenant_id = s.tenant_id
			LEFT JOIN session_summaries ss
				ON ss.session_key = s.session_id AND ss.tenant_id = s.tenant_id
			LEFT JOIN public.session_title_states tstate
				ON tstate.tenant_id = s.tenant_id AND tstate.scoped_session_id = s.session_id
			` + sessionAnalysisJoinSQL() + `
			` + sessionTitleFallbackJoinSQL("s.session_id", "sd.task_id") + `
			WHERE s.session_id=$1 AND s.tenant_id=$2
			ORDER BY s.partition_date DESC LIMIT 1`
	var (
		// 2026-08-28: 这三个列来自 LEFT JOIN LATERAL public.session_analysis_metadata,
		// 在 JOIN miss (会话从未被分析) 时为 NULL。用 *string 接收 NULL,
		// 否则 pgx 报错 "cannot scan NULL into *string" → 快照接口 500。
		saStatus, saSchemaVersion, saInputHash *string
		saSourceTaskID                         *string
		saUpdatedAt                            *time.Time
		saPayloadRaw                           []byte
		userTags                               []string
	)
	err := h.db.QueryRow(r.Context(), query,
		sessionID, tenantID).Scan(
		&snap.SessionID, &snap.TenantID, &snap.Title, &snap.Summary,
		&snap.SummaryGeneratedAt, &snap.TotalTurns, &snap.TotalTokens, &snap.TotalCostUSD,
		&snap.LastTurnNo, &snap.LastModel, &snap.LastProvider,
		&snap.LastRequestSummary, &snap.LastResponseSummary,
		&snap.CreatedAt, &snap.UpdatedAt, &snap.ClosedAt, &snap.Status,
		&snap.TaskType, &snap.ClientType, &snap.Topic, &snap.Intent, &userTags,
		&saStatus, &saSchemaVersion, &saInputHash, &saSourceTaskID, &saUpdatedAt, &saPayloadRaw)
	if err != nil {
		if err.Error() == "no rows in result set" {
			writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "tenant_id": tenantID})
			return
		}
		slog.ErrorContext(r.Context(), "serveSessionSnapshot query failed",
			"session_id", sessionID, "tenant_id", tenantID, "error", err.Error())
		writeError(w, http.StatusInternalServerError, "query snapshot failed: "+err.Error())
		return
	}
	// LEFT JOIN miss → 所有分析列为 NULL, 用空串作为 "未分析" 哨兵。
	saStatusVal := ""
	if saStatus != nil {
		saStatusVal = *saStatus
	}
	saSchemaVal := ""
	if saSchemaVersion != nil {
		saSchemaVal = *saSchemaVersion
	}
	saHashVal := ""
	if saInputHash != nil {
		saHashVal = *saInputHash
	}
	if saStatusVal != "" {
		var view SessionAnalysisView
		scanSessionAnalysis(&view, saStatusVal, saSchemaVal, saHashVal, saSourceTaskID, saUpdatedAt, saPayloadRaw)
		snap.SessionAnalysis = &view
	}
	snap.UserTags = userTags
	writeJSON(w, http.StatusOK, snap)
}

// serveSessionInstantSummary 触发会话即时总结，返回生成的标题/总结/生成时间。
// 复用 SessionSummaryV2API 的 LLM 总结路径（session_summary_v2.go），并把
// title/summary/summary_generated_at 回写 public.sessions，供前端
// 轮询感知新鲜度（public.sessions 是分区表，UPDATE 需 ORDER BY LIMIT 1）。
func (h *Handler) serveSessionInstantSummary(w http.ResponseWriter, r *http.Request, sessionID string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	api := &SessionSummaryV2API{pool: h.db}
	summary, err := api.generateSummary(r.Context(), &SessionSummaryRequest{
		SessionID: sessionID,
		Tenant:    tenantID,
	}, tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "summary failed: "+err.Error())
		return
	}

	now := time.Now()
	// Partitioned sessions table: UPDATE ... ORDER BY/LIMIT is invalid in PostgreSQL.
	// Pin the newest partition via MAX(partition_date), same pattern as titlestore.
	result, uerr := h.db.Exec(r.Context(), `
		UPDATE public.sessions
		SET title=$3, summary=$4, summary_generated_at=$5, updated_at=$5
		WHERE session_id=$1 AND tenant_id=$2
		  AND partition_date = (
			SELECT MAX(partition_date) FROM public.sessions
			WHERE session_id=$1 AND tenant_id=$2
		  )`,
		sessionID, tenantID, summary.Title, summary.Summary, now)
	if uerr != nil {
		writeError(w, http.StatusInternalServerError, "update snapshot failed: "+uerr.Error())
		return
	}
	if result.RowsAffected() == 0 {
		// 会话记录不存在（V2 影子写可能未写入），但总结结果仍返回给前端。
		now = time.Time{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"title":                summary.Title,
		"summary":              summary.Summary,
		"turns_analyzed":       summary.TurnsAnalyzed,
		"summary_generated_at": timePtrOrNil(now),
	})
}

// tenantFromQueryOrContext resolves a session tenant from authenticated scope.
// Only platform-wide administrators may select another tenant explicitly; every
// other authenticated role is pinned to its own tenant and cannot override it
// through a query parameter or request header.
func tenantFromQueryOrContext(r *http.Request) string {
	if !IsSuperAdminOrLegacy(r) {
		return GetTenantID(r)
	}
	if t := r.URL.Query().Get("tenant"); t != "" {
		return t
	}
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return GetTenantID(r)
}

func timePtrOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
