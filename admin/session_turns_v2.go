package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
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
)

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
	beforeTurnNo := int(^uint(0) >> 1)
	if encoded := r.URL.Query().Get("cursor"); encoded != "" {
		decoded, err := validateCursor(encoded, []byte(h.secret), tenantID, sessionID)
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

	rows, err := h.db.Query(r.Context(), `
		SELECT turn_no, ts, COALESCE(title,''), COALESCE(summary,''),
		       COALESCE(prompt_tokens,0), COALESCE(completion_tokens,0), COALESCE(cost_usd,0),
		       COALESCE(model,''), COALESCE(provider,''), COALESCE(status_code,0),
		       COALESCE(submit_mode,''), COALESCE(injection_verdict,''), COALESCE(output_verdict,''),
		       COALESCE(attachment_count,0)
		FROM gateway.session_turns
		WHERE tenant_id=$1 AND session_id=$2 AND turn_no < $3
		ORDER BY turn_no DESC LIMIT $4`, tenantID, sessionID, beforeTurnNo, limit+1)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query turns failed")
		return
	}
	defer rows.Close()

	items := make([]TurnListItem, 0, limit)
	for rows.Next() {
		var it TurnListItem
		if err := rows.Scan(&it.TurnNo, &it.Ts, &it.Title, &it.Summary, &it.RequestTokens,
			&it.ResponseTokens, &it.CostUSD, &it.Model, &it.Provider, &it.StatusCode,
			&it.SubmitMode, &it.InjectionVerdict, &it.OutputVerdict, &it.AttachmentCount); err != nil {
			writeError(w, http.StatusInternalServerError, "scan turn failed")
			return
		}
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
		}, []byte(h.secret))
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
	Meta        map[string]any `json:"meta"`
	Governance  map[string]any `json:"governance"`
	Attachments []attachmentV2 `json:"attachments"`
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
	turnNo, err := strconv.Atoi(turnNoStr)
	if err != nil || turnNo <= 0 {
		writeError(w, http.StatusBadRequest, "invalid turn_no")
		return
	}
	if h.db == nil {
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
			t.source_kind, t.quality,
			b.request_delta, b.response_delta, b.outbound_body,
			b.request_attachments, b.response_attachments
		FROM gateway.session_turns t
		LEFT JOIN gateway.session_bodies b
			ON t.session_id = b.session_id AND t.turn_no = b.turn_no AND t.partition_date = b.partition_date
		WHERE t.session_id = $1 AND t.tenant_id = $2 AND t.turn_no = $3
		LIMIT 1`
	var (
		turnNoOut                                                                   int
		requestID, submitMode, injectionVerdict, outputVerdict, sourceKind, quality string
		ts                                                                          time.Time
		compressionApplied                                                          bool
		compressionStrategy                                                         *string
		compressionMetaRaw, requestDeltaRaw, responseDeltaRaw, outboundBodyRaw      []byte
		compressionTokensSaved                                                      *int
		model, provider, errorKind                                                  *string
		promptTokens, completionTokens, cacheReadTokens, cacheWriteTokens           *int
		costUSD                                                                     *float64
		latencyMs, statusCode                                                       *int
		success                                                                     *bool
		requestAttachmentsRaw, responseAttachmentsRaw                               []byte
	)
	err = h.db.QueryRow(r.Context(), query, sessionID, tenantID, turnNo).Scan(
		&turnNoOut, &requestID, &ts,
		&submitMode, &compressionApplied, &compressionStrategy,
		&compressionMetaRaw, &compressionTokensSaved,
		&injectionVerdict, &outputVerdict,
		&model, &provider,
		&promptTokens, &completionTokens, &cacheReadTokens, &cacheWriteTokens,
		&costUSD, &latencyMs, &statusCode, &success, &errorKind,
		&sourceKind, &quality,
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

	resp := turnDetailV2Response{
		Request:  decodeStoredJSON("request_delta", requestID, requestDeltaRaw),
		Response: decodeStoredJSON("response_delta", requestID, responseDeltaRaw),
		Meta: map[string]any{
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
		},
		Governance: map[string]any{
			"submit_mode":              submitMode,
			"compression_applied":      compressionApplied,
			"compression_strategy":     stringPtrValue(compressionStrategy),
			"compression_tokens_saved": intPtrValue(compressionTokensSaved),
			"injection_verdict":        injectionVerdict,
			"output_verdict":           outputVerdict,
		},
		Attachments: buildTurnAttachments(requestID, requestAttachmentsRaw, responseAttachmentsRaw),
	}
	if model != nil {
		resp.Model = *model
	}
	if costUSD != nil {
		resp.CostUSD = *costUSD
	}
	if len(outboundBodyRaw) > 0 {
		// 把 outbound_body 合并进 request 视图（前端如有需要可读到）。
		if existing, ok := resp.Request.(map[string]any); ok {
			existing["_outbound_body"] = decodeStoredJSON("outbound_body", requestID, outboundBodyRaw)
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
// 数据源为 gateway.sessions（V2 会话快照，migration 456 添加了
// title/summary/summary_generated_at 列，migration 430 有 last_model/last_provider）。
type sessionSnapshotV2 struct {
	SessionID          string     `json:"session_id"`
	TenantID           string     `json:"tenant_id"`
	Title              string     `json:"title"`
	Summary            string     `json:"summary"`
	SummaryGeneratedAt *time.Time `json:"summary_generated_at,omitempty"`
	TotalTurns         int        `json:"total_turns"`
	TotalCostUSD       float64    `json:"total_cost_usd"`
	LastModel          *string    `json:"last_model,omitempty"`
	LastProvider       *string    `json:"last_provider,omitempty"`
}

// serveSessionSnapshot 返回会话快照（取自 gateway.sessions）。
func (h *Handler) serveSessionSnapshot(w http.ResponseWriter, r *http.Request, sessionID string) {
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}
	tenantID := tenantFromQueryOrContext(r)
	var snap sessionSnapshotV2
	err := h.db.QueryRow(r.Context(), `
		SELECT session_id, tenant_id, COALESCE(title,''), COALESCE(summary,''),
		       summary_generated_at, total_turns, total_cost_usd,
		       last_model, last_provider
		FROM gateway.sessions
		WHERE session_id=$1 AND tenant_id=$2
		ORDER BY partition_date DESC LIMIT 1`,
		sessionID, tenantID).Scan(
		&snap.SessionID, &snap.TenantID, &snap.Title, &snap.Summary,
		&snap.SummaryGeneratedAt, &snap.TotalTurns, &snap.TotalCostUSD,
		&snap.LastModel, &snap.LastProvider)
	if err != nil {
		if err.Error() == "no rows in result set" {
			writeJSON(w, http.StatusOK, map[string]any{"session_id": sessionID, "tenant_id": tenantID})
			return
		}
		writeError(w, http.StatusInternalServerError, "query snapshot failed")
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// serveSessionInstantSummary 触发会话即时总结，返回生成的标题/总结/生成时间。
// 复用 SessionSummaryV2API 的 LLM 总结路径（session_summary_v2.go），并把
// title/summary/summary_generated_at 回写 gateway.sessions，供前端
// 轮询感知新鲜度（gateway.sessions 是分区表，UPDATE 需 ORDER BY LIMIT 1）。
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
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "summary failed: "+err.Error())
		return
	}

	now := time.Now()
	result, uerr := h.db.Exec(r.Context(), `
		UPDATE gateway.sessions
		SET title=$3, summary=$4, summary_generated_at=$5, updated_at=$5
		WHERE session_id=$1 AND tenant_id=$2
		ORDER BY partition_date DESC LIMIT 1`,
		sessionID, tenantID, summary.Title, summary.Summary, now)
	if uerr != nil {
		writeError(w, http.StatusInternalServerError, "update snapshot failed")
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

// tenantFromQueryOrContext 从查询参数或请求上下文解析租户（缺省 "default"）。
func tenantFromQueryOrContext(r *http.Request) string {
	if t := r.URL.Query().Get("tenant"); t != "" {
		return t
	}
	if t := r.Header.Get("X-Tenant-ID"); t != "" {
		return t
	}
	return "default"
}

func timePtrOrNil(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}
