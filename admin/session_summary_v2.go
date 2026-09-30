// Package admin - session_summary_v2.go
//
// 会话总结 API v2 — 调用LLM对会话内容进行即时总结
//
//   POST /api/admin/sessions/summary
//   Body: {
//     "session_id": "xxx",
//     "tenant": "default",
//     "up_to_turn": 3  // optional, summarize up to this turn
//   }
//
// 返回格式：
//   {
//     "title": "...",
//     "summary": "...",
//     "turns_analyzed": 3
//   }
//
// 仅 super 用户可用。

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/db"
	"github.com/kaixuan/llm-gateway-go/internal/jsonbody"
	"github.com/kaixuan/llm-gateway-go/internal/observability"
)

// SessionSummaryV2API 提供会话总结端点
type SessionSummaryV2API struct {
	pool *pgxpool.Pool
	// llmCall（2026-09-29 审计二十一轮）由装配方注入：走网关自身 admin LLM
	// 任务管线生成 title/summary（Handler.SessionSummaryLLMCaller）。此前
	// 端点内是写死 http://localhost:8080 + gpt-4o-mini 的占位桩（无鉴权、
	// 生产不可达），失败静默落入字节截断的伪摘要。nil 时 generateSummary
	// 退化为 rune 安全截断摘要并 Warn——桩已删除，不再有假的 "llm 生成"。
	llmCall func(ctx context.Context, r *http.Request, conversationText string) (title, summary string, err error)
}

// NewSessionSummaryV2API 构造函数
func NewSessionSummaryV2API(pool *pgxpool.Pool) *SessionSummaryV2API {
	return &SessionSummaryV2API{pool: pool}
}

// SetLLMCaller 注入真实 LLM 生成闭包（main.go / serveSessionInstantSummary 装配）。
func (api *SessionSummaryV2API) SetLLMCaller(fn func(ctx context.Context, r *http.Request, conversationText string) (string, string, error)) {
	api.llmCall = fn
}

// SessionSummaryRequest 是总结请求的结构
type SessionSummaryRequest struct {
	SessionID string `json:"session_id"`
	Tenant    string `json:"tenant"`
	UpToTurn  *int   `json:"up_to_turn,omitempty"`
}

// SessionSummaryResponse 是总结响应的结构
type SessionSummaryResponse struct {
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	TurnsAnalyzed int    `json:"turns_analyzed"`
	// summary_source（2026-09-29 审计二十一轮）: "llm"=真实模型生成；
	// "fallback"=LLM 不可用时的 rune 安全截断摘要。让前端/运维能区分
	// 真总结与降级产物（反馈闭环）。
	SummarySource string `json:"summary_source,omitempty"`
}

func (api *SessionSummaryV2API) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if api.pool == nil {
		writeExportJSONError(w, http.StatusServiceUnavailable, "session summary v2 API requires database")
		return
	}
	if r.URL.Path != "/api/admin/sessions/summary" {
		writeExportJSONError(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodPost {
		writeExportJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req SessionSummaryRequest
	if err := jsonbody.DecodeRequest(r, &req, jsonbody.MaxRequiredBody, true); err != nil {
		writeExportJSONError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.SessionID == "" {
		writeExportJSONError(w, http.StatusBadRequest, "session_id is required")
		return
	}
	if req.Tenant == "" {
		req.Tenant = "default"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 2026-08-30: enforce tenant isolation. The standalone summary endpoint
	// is restricted to super_admin + tenant_admin. For tenant_admin, the
	// tenant must be their own — any caller-supplied "tenant" field is
	// ignored. For super_admin, the tenant is taken from the request body.
	authTenant := GetTenantID(r)
	isSuper := IsSuperAdminOrLegacy(r)
	if !isSuper && !IsTenantAdmin(r) {
		writeExportJSONError(w, http.StatusForbidden, "session summary requires super_admin or tenant_admin")
		return
	}

	if !isSuper {
		// tenant_admin: tenant MUST be their own. Ignore any caller-supplied tenant.
		tenantID := authTenant
		summary, err := api.generateSummary(ctx, &SessionSummaryRequest{
			SessionID: req.SessionID,
			Tenant:    tenantID,
			UpToTurn:  req.UpToTurn,
		}, tenantID, r)
		if err != nil {
			api.writeSummaryError(w, err)
			return
		}
		writeExportJSON(w, http.StatusOK, summary)
		return
	}

	// super_admin: caller may specify tenant in body.
	tenantID := req.Tenant
	if tenantID == "" {
		tenantID = "default"
	}
	// Pass an empty authTenant for unrestricted callers so the explicit
	// super-admin tenant selector is not overwritten by GetTenantID's
	// legacy/default fallback value.
	summary, err := api.generateSummary(ctx, &SessionSummaryRequest{
		SessionID: req.SessionID,
		Tenant:    tenantID,
		UpToTurn:  req.UpToTurn,
	}, "", r)
	if err != nil {
		api.writeSummaryError(w, err)
		return
	}

	writeExportJSON(w, http.StatusOK, summary)
}

// writeSummaryError 统一 summary 端点的错误出口：存储层不可用 → 503 降级
// 契约（storage_degraded.go，2026-09-29 审计二十一轮接齐）；其余维持
// writeInternalErrStr 的 500（不回显内部错误串）。
func (api *SessionSummaryV2API) writeSummaryError(w http.ResponseWriter, err error) {
	if IsStorageUnavailable(err) {
		WriteStorageDegraded(w, observability.StorageComponentSummary, err)
		return
	}
	// 正文取数并发已满 → 503「现在忙」，不是 500「坏了」。调用方对这两者的
	// 重试含义不同（503 可退避重试，500 不该重试）。见
	// session_bodies_batch.go 的并发闸注释。
	if errors.Is(err, ErrBodyFetchSaturated) {
		slog.Warn("session summary rejected: body fetch saturated", "err", err, "limit", maxConcurrentBodyFetches)
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"status":  "error",
			"message": "Session body data is temporarily unavailable — too many concurrent scans. Retry shortly.",
			"code":    "session_body_fetch_saturated",
		})
		return
	}
	writeInternalErrStr(w, "summary failed", err)
}

func (api *SessionSummaryV2API) generateSummary(
	ctx context.Context,
	req *SessionSummaryRequest,
	authTenant string,
	r *http.Request,
) (*SessionSummaryResponse, error) {
	tenantID := req.Tenant
	if authTenant != "" {
		// The authenticated tenant is authoritative. Never let a caller
		// supplied JSON tenant override it; doing so would turn this
		// summary endpoint into a cross-tenant IDOR for tenant_admins.
		tenantID = authTenant
	}

	// 1. Query turns from session_turns + session_bodies_unified.
	turns, err := api.queryTurnsForSummary(ctx, req.SessionID, tenantID, req.UpToTurn)
	if err != nil {
		return nil, fmt.Errorf("query turns: %w", err)
	}

	if len(turns) == 0 {
		// 2026-08-30: many sessions have V2 shadow-write disabled
		// (sessions_v2.enabled=false) so session_turns is empty even though
		// request_logs (the V1 store) has the full conversation. Fall back
		// to request_logs so the operator gets a real summary instead of
		// "no turns found".
		fallback, ferr := api.queryRequestLogsFallback(ctx, req.SessionID, tenantID, req.UpToTurn)
		if ferr != nil {
			return nil, fmt.Errorf("query turns (request_logs fallback): %w", ferr)
		}
		if len(fallback) == 0 {
			return nil, fmt.Errorf("no turns found for session %s", req.SessionID)
		}
		turns = fallback
	}

	// 2. Build conversation text for LLM
	conversationText := buildConversationText(turns)

	// 3. 生成 title/summary（2026-09-29 审计二十一轮起走装配方注入的真实
	// 链路——网关自身 admin LLM 任务管线；此前是写死 localhost:8080/
	// gpt-4o-mini 的占位桩，生产必败静默落入伪摘要）。
	title, summary, source := api.summarize(ctx, r, conversationText)

	return &SessionSummaryResponse{
		Title:         title,
		Summary:       summary,
		TurnsAnalyzed: len(turns),
		SummarySource: source,
	}, nil
}

// summarize 生成 title/summary：llmCall 可用走真实 LLM；不可用或失败时
// 退化为 rune 安全截断摘要（LLM 故障不打挂整个端点），并在响应体用
// summary_source 诚实标注来源；失败原因进服务端日志。
func (api *SessionSummaryV2API) summarize(ctx context.Context, r *http.Request, conversationText string) (title, summary, source string) {
	if api.llmCall != nil && r != nil {
		t, s, err := api.llmCall(ctx, r, conversationText)
		if err == nil && strings.TrimSpace(s) != "" {
			return t, s, "llm"
		}
		// 2026-09-29 (审计二十二轮): err==nil 但摘要为空时打「空摘要」
		// 事实日志——此前统一打「生成失败」+err=nil，误导排障方向。
		if err != nil {
			slog.WarnContext(ctx, "session summary: LLM 生成失败，退化为截断摘要", "err", err)
		} else {
			slog.WarnContext(ctx, "session summary: LLM 返回空摘要，退化为截断摘要")
		}
	} else {
		slog.WarnContext(ctx, "session summary: LLM caller 未接线，退化为截断摘要")
	}
	fTitle, fSummary, _ := generateFallbackSummary(conversationText)
	return fTitle, fSummary, "fallback"
}

// fallbackTurnKey identifies one fallback turn for body lookup. Bodies are
// paired by request identity **and** timestamp to avoid attaching a reused
// request ID to the wrong turn.
//
// ts is normalized to Unix microseconds rather than kept as time.Time: Go's
// time.Time `==` compares the location pointer, so a key built from a scan
// (which carries the connection's *time.Location) would not match the same
// instant scanned back from a different session-timezone setting, silently
// turning every body hit into a miss. PostgreSQL timestamptz has microsecond
// resolution, which is exactly what UnixMicro preserves.
type fallbackTurnKey struct {
	requestID  string
	tsUnixMicr int64
}

// newFallbackTurnKey builds the phase-1/phase-2 join key. Both phases call it,
// which is the only reason the normalization actually holds.
func newFallbackTurnKey(requestID string, ts time.Time) fallbackTurnKey {
	return fallbackTurnKey{requestID: requestID, tsUnixMicr: ts.UnixMicro()}
}

// The per-turn body value now lives in sessionBody (session_bodies_batch.go)
// rather than a summary-local type, so the summary and compare paths cannot
// drift on how a "no body stored" turn is represented. A missing map entry and a
// map entry holding nils mean the same thing here: what the old LEFT JOIN
// delivered as NULL.

// queryRequestLogsFallback derives turn-shaped conversation text from the
// V1 request_logs store. request_logs_with_current_month already includes the
// hot write window, so querying request_logs_hot separately would duplicate
// every recent request.
//
// It runs as **two queries**, not one join (measured on the live database,
// 2026-10-01). The single-query form was a 115-column nested loop against
// request_logs_bodies_with_current_month, and migration 765 turned that
// month's partition into a Citus columnar table:
//
//	phase 1 (request_id + ts only)          41 ms, 1,029 buffers
//	phase 2 (batched body fetch)          7–501 ms, ~120 buffers
//	old single query (same session)   40,483 ms, 8,513,122 buffers
//
// The cost was never the JSONB payloads being sorted — it was the per-row
// ColumnarScan that the LEFT JOIN drove (each turn re-scanned a 2.2M-row
// chunked partition). Phase 1 touches only the session turns; phase 2 hands
// the planner a set it can answer from the (request_id, ts) primary key.
//
// Ordering and row set are unchanged: phase 1 owns the ORDER BY and the
// LIMIT, and phase 2 only supplies bodies. The join is 1:1 — every partition
// of request_logs_bodies carries a UNIQUE (request_id, ts) primary key and
// request_logs_bodies_hot is disjoint from the monthly parent (verified: 0
// overlapping rows) — so limiting before the body fetch drops nothing.
func (api *SessionSummaryV2API) queryRequestLogsFallback(
	ctx context.Context,
	sessionID, tenantID string,
	upToTurn *int,
) ([]turnForSummary, error) {
	keys, err := api.queryFallbackTurnKeys(ctx, sessionID, tenantID, upToTurn)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	requestIDs := make([]string, len(keys))
	for i, key := range keys {
		requestIDs[i] = key.requestID
	}
	// 2026-10-01：配对键从 (request_id, ts) 切到 request_id 单键。
	// 依据是实测，不是推断：
	//
	//	turns 腿 = v1 视图  ×  bodies 配 (request_id, ts)  → 11.207%（1839/16409）
	//	turns 腿 = v1 视图  ×  bodies 配 request_id         → 100.000%（16409/16409）
	//	turns 腿 = 原生源    ×  bodies 配 (request_id, ts)  → 0.007%（1/14582）
	//
	// 根因见 §5.9：request_logs_bodies.ts 是**正文写入时间**，不是轮次时间
	// （通常差 8~16 秒）。单键安全性已实测：request_logs_bodies_with_current_month
	// 2,220,507 行 = 2,220,507 个不同 request_id，无重键，所以 request_id
	// 单键是**无歧义**的，不需要 ts 来消歧。
	//
	bodies, err := querySessionBodiesByRequestID(ctx, api.pool, requestIDs)
	if err != nil {
		return nil, err
	}
	return mergeFallbackTurns(keys, bodies), nil
}

// mergeFallbackTurns pairs each turn identity with its body, preserving the
// order phase 1 produced. A turn with no stored body keeps a nil delta, which
// is byte-for-byte what the old LEFT JOIN emitted as NULL.
//
// Bodies are keyed by request_id alone (see queryRequestLogsFallback); a
// missing map entry and a map entry holding nils mean the same thing here.
func mergeFallbackTurns(keys []fallbackTurnKey, bodies map[string]sessionBody) []turnForSummary {
	if len(keys) == 0 {
		return nil
	}
	turns := make([]turnForSummary, 0, len(keys))
	for i, key := range keys {
		body := bodies[key.requestID]
		turns = append(turns, turnForSummary{
			TurnNo:        i + 1,
			RequestDelta:  decodeStoredJSON("request_body", key.requestID, strPtrBytes(body.requestBody)),
			ResponseDelta: decodeStoredJSON("response_body", key.requestID, strPtrBytes(body.responseBody)),
		})
	}
	return turns
}

// queryFallbackTurnKeys runs phase 1: session turns in chronological order,
// bounded by upToTurn, with no body columns involved.
func (api *SessionSummaryV2API) queryFallbackTurnKeys(
	ctx context.Context,
	sessionID, tenantID string,
	upToTurn *int,
) ([]fallbackTurnKey, error) {
	query, args := buildRequestLogsFallbackQuery(sessionID, tenantID, upToTurn)
	rows, err := api.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// ts 只进 key、不再单独返回：phase 1 仍按 rl.ts ASC 排序（轮次时间，语义
	// 正确），但正文按 request_id 单键配对，不再需要把 ts 传给 phase 2
	// （见 queryRequestLogsFallback 的命中率对比）。
	var keys []fallbackTurnKey
	for rows.Next() {
		var requestID string
		var ts time.Time
		if err := rows.Scan(&requestID, &ts); err != nil {
			return nil, err
		}
		keys = append(keys, newFallbackTurnKey(requestID, ts))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

func buildRequestLogsFallbackQuery(sessionID, tenantID string, upToTurn *int) (string, []any) {
	// 会话存储解耦 v3 审计（2026-09-30）：本查询曾迁到 session 族原生源
	// （实测 14000ms → 5308ms）又因「镜像漏写 20,660 会话」被回退。§5.3.1
	// 复核证明真正的 genuine_loss（1,459 行）已全量补写，35 天窗口复测为 0，
	// 该否决理由不再成立，故于同日重新启用。
	//
	// 口径差（全量实测）：视图比原生源多 38,229 行 / 2.30% 会话，全部是
	// hook 按设计不镜像的 internal_loopback（36,693，标题/摘要生成器自己的
	// LLM 调用）与 non_terminal（1,541，in_progress 占位），unexplained = 0。
	// 对本端点而言这是**净收益**：总结正文此前会把网关自己生成的标题/摘要
	// 调用当成用户发言喂进对话文本。
	//
	// 2026-10-01 缺陷 7（审计报告新增，优先级高于 §8 第 7 项配对键）：
	// 上一版把本查询的 FROM 换成了 db.SessionFamilyTurnsForSessionSQL()。
	// 那展开是 `session_turns_hot UNION ALL session_turns`，**与主路径
	// session_turns_with_current_month 同源**（该视图 = hot 去重 ∪ parent）。
	// 而这条路径存在的唯一理由就是主路径读不到轮次时才被调用 —— 改接之后
	// 它去读同一批表，必然同样返回 0 行，generateSummary 落到
	// `no turns found for session %s` → HTTP 500。
	//
	// 实测（真库，近 3 天窗口）：触发 fallback 的会话 1,700 个，它们在 v1 里
	// 本该有 1,802 轮，fallback 自己的 turns 腿返回 **0 行（1700/1700 全 0）**；
	// 全体 v1 会话中触发比例 10.87%。改接发生在 02c93d04e（S4 读路径迁移批次），
	// 而同批次把等价性门里的 legacy 查询也一起改了 ⇒ 门对换源完全不可见。
	//
	// 现在改回 v1 视图，**并**把内部调用排除掉，因为读 v1 视图会带回
	// internal_loopback / non_terminal（见上面那段口径差说明：那是净收益，
	// 不能丢）。排除谓词用 db.MirrorDriftClassSQL，与 dual-read-drift 判定
	// genuine_loss 的口径同源，不另起一份定义。
	//
	// 2026-10-01：正文腿从本查询里摘出，改由 querySessionBodies 单独取。
	// 本查询现在只投影 request_id + ts —— 见 queryRequestLogsFallback 的
	// 计时对比（40,483ms → 41ms）。正文取 v1 bodies 视图（rb 腿未换源）：
	// session_bodies 只有增量、无 final_full 全量，正文存储决策未落地前
	// 两腿口径必须一致。
	//
	// WHERE 用 `rl.gw_session_id = $1` 而不是 `= $1::text`：gw_session_id 是
	// CASE 投影的表达式，裸 `$1` 推不出类型（42P18 "could not determine data
	// type of parameter $1"，实测）。删掉这个过滤会同时丢掉参数绑定。
	//
	// LIMIT 的序号用 strconv 拼，不用 fmt.Sprintf —— 原生源 SQL 里含
	// LIKE 'sys:%'，把它当格式串会吃掉参数（见 turns_sessions 同族事故）。
	query := `
		SELECT rl.request_id,
		       rl.ts
		FROM request_logs_with_current_month rl
		WHERE rl.gw_session_id = $1
		  -- 注意方向：这里要**保留** 'genuine_loss'，不是排除它。
		  -- 这个标签来自 dual-read-drift 的诊断口径，读起来像「坏行」，
		  -- 实际含义恰好相反 —— 它是「镜像钩子本来会写、却没找到对应
		  -- session_turns 的 v1 行」，也就是本端点**要服务**的那些业务轮次。
		  -- 被排除的 internal_loopback / non_terminal 才是钩子按设计不镜像的。
		  -- 写反的后果不报错、也不空：近 3 天窗口下用「不等于」会留下
		  -- 1,829 行（= loopback 1,721 + non_terminal 108，恰好等于被丢掉
		  -- 的那批），把 14,546 行业务轮次全部扔掉。
		  AND (` + db.MirrorDriftClassSQL + `) = 'genuine_loss'`
	args := []any{sessionID}
	if tenantID != "" {
		query += " AND rl.tenant_id = $2"
		args = append(args, tenantID)
	}
	query += " ORDER BY rl.ts ASC"
	if upToTurn != nil {
		query += " LIMIT $" + strconv.Itoa(len(args)+1)
		args = append(args, *upToTurn)
	}
	return query, args
}

// turnForSummary 是用于总结的简化turn结构
type turnForSummary struct {
	TurnNo        int
	RequestDelta  any
	ResponseDelta any
}

func (api *SessionSummaryV2API) queryTurnsForSummary(
	ctx context.Context,
	sessionID, tenantID string,
	upToTurn *int,
) ([]turnForSummary, error) {
	// 2026-08-30: read from public.session_bodies_unified so that turns whose
	// body row is still in session_bodies_hot (the recent-write window after
	// migration 614) are visible. Falling back to public.session_bodies
	// directly would silently drop hot rows and surface a misleading
	// "no turns found" error to the caller even when metadata exists.
	query := `
		SELECT
			t.turn_no,
			b.request_delta,
			b.response_delta
		FROM public.session_turns_with_current_month t
		LEFT JOIN public.session_bodies_unified b
			ON t.tenant_id = b.tenant_id
			AND t.session_id = b.session_id
			AND t.turn_no = b.turn_no
			AND t.request_id = b.request_id
		WHERE t.session_id = $1 AND t.tenant_id = $2
	`
	args := []any{sessionID, tenantID}

	if upToTurn != nil {
		query += " AND t.turn_no <= $3"
		args = append(args, *upToTurn)
	}

	query += " ORDER BY t.turn_no ASC" // Chronological order for summary

	rows, err := api.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var turns []turnForSummary
	for rows.Next() {
		var t turnForSummary
		var requestDeltaRaw, responseDeltaRaw []byte

		err := rows.Scan(&t.TurnNo, &requestDeltaRaw, &responseDeltaRaw)
		if err != nil {
			return nil, err
		}

		// Decode failures are logged: a null delta is indistinguishable from a
		// turn that stored no delta at all.
		t.RequestDelta = decodeStoredJSON("request_delta", sessionID, requestDeltaRaw)
		t.ResponseDelta = decodeStoredJSON("response_delta", sessionID, responseDeltaRaw)

		turns = append(turns, t)
	}

	return turns, rows.Err()
}

// buildConversationText 将turns转换为适合LLM分析的文本格式。
// 只纳入 user/assistant 内容，排除 system/developer 样板提示。
// Restored 2026-08-27 after d2cbaf88b stripped the system-skipping form,
// which leaked system prompts back into the summary corpus.
func buildConversationText(turns []turnForSummary) string {
	var buf bytes.Buffer

	for _, t := range turns {
		buf.WriteString(fmt.Sprintf("=== Turn %d ===\n", t.TurnNo))

		userText := extractDialogueContent(t.RequestDelta, "user")
		assistantText := extractDialogueContent(t.ResponseDelta, "assistant")
		if userText == "" {
			userText = extractDialogueContent(t.RequestDelta, "")
		}
		if assistantText == "" {
			assistantText = extractDialogueContent(t.ResponseDelta, "")
		}

		buf.WriteString("User: ")
		buf.WriteString(userText)
		buf.WriteString("\n\n")

		buf.WriteString("Assistant: ")
		buf.WriteString(assistantText)
		buf.WriteString("\n\n")
	}

	return buf.String()
}

// extractDialogueContent extracts plain text for summary corpora.
// Prefer roleFilter when set; always skip system/developer/tool messages.
func extractDialogueContent(delta any, roleFilter string) string {
	if delta == nil {
		return ""
	}
	want := strings.ToLower(strings.TrimSpace(roleFilter))

	appendContent := func(parts *[]string, role string, content any) {
		role = strings.ToLower(strings.TrimSpace(role))
		if role == "system" || role == "developer" || role == "tool" || role == "function" {
			return
		}
		if want != "" && role != "" && role != want {
			return
		}
		text := strings.TrimSpace(contentToPlainText(content))
		if text != "" {
			*parts = append(*parts, text)
		}
	}

	var parts []string
	switch v := delta.(type) {
	case map[string]any:
		if msgs, ok := v["messages"].([]any); ok {
			for _, raw := range msgs {
				msg, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				appendContent(&parts, fmt.Sprint(msg["role"]), msg["content"])
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
		if choices, ok := v["choices"].([]any); ok && len(choices) > 0 {
			if last, ok := choices[len(choices)-1].(map[string]any); ok {
				if msg, ok := last["message"].(map[string]any); ok {
					appendContent(&parts, fmt.Sprint(msg["role"]), msg["content"])
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
		appendContent(&parts, fmt.Sprint(v["role"]), v["content"])
	case []any:
		for _, raw := range v {
			msg, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			appendContent(&parts, fmt.Sprint(msg["role"]), msg["content"])
		}
	case string:
		return strings.TrimSpace(v)
	}
	return strings.Join(parts, "\n")
}

func contentToPlainText(content any) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		var texts []string
		for _, part := range c {
			switch p := part.(type) {
			case string:
				if strings.TrimSpace(p) != "" {
					texts = append(texts, p)
				}
			case map[string]any:
				if t, ok := p["text"].(string); ok && strings.TrimSpace(t) != "" {
					texts = append(texts, t)
				} else if t, ok := p["content"].(string); ok && strings.TrimSpace(t) != "" {
					texts = append(texts, t)
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}

// extractMessageContent keeps the legacy helper name for call sites that need
// "any readable text" while extractDialogueContent remains the shared parser.
// It is intentionally retained as a compatibility shim: deleting it would
// make otherwise-unrelated admin packages silently miss user/assistant content
// during incremental compilation or downstream embedding.
func extractMessageContent(delta any) (string, bool) {
	text := extractDialogueContent(delta, "")
	if text == "" {
		return "", false
	}
	return text, true
}

// SessionSummaryLLMCaller 返回走网关自身 admin LLM 任务管线生成会话摘要的
// 闭包（session_summary work_type + auto 模型 + 显式 fallback 重试，见
// admin_llm_task.go），与 session title（session_title.go 同款）共用同一
// 真实链路：端点取本网关地址、密钥走 pickFirstAvailableAPIKey、提示词经
// <session_transcript> 包裹防注入。
//
// 2026-09-29 (审计二十一轮)：此前该端点是写死 http://localhost:8080 +
// gpt-4o-mini 的占位桩（无 Authorization、生产不可达），失败静默落入
// summary[:200] 字节截断的伪摘要——同仓 title 早已是真链路，一真一假。
func (h *Handler) SessionSummaryLLMCaller() func(ctx context.Context, r *http.Request, conversationText string) (string, string, error) {
	return func(ctx context.Context, r *http.Request, conversationText string) (string, string, error) {
		_, apiKey, err := h.pickFirstAvailableAPIKey(ctx, r)
		if err != nil {
			return "", "", fmt.Errorf("pick api key: %w", err)
		}
		res, err := h.callAdminLLMChat(ctx, r, apiKey, adminLLMTaskSessionSummary, "", conversationText)
		if err != nil {
			return "", "", err
		}
		return parseSummaryLLMContent(res.Content)
	}
}

// parseSummaryLLMContent 解析摘要模型输出：优先 JSON {"title","summary"}
// （剥代码围栏），否则退回「首行=标题」文本启发式。
func parseSummaryLLMContent(content string) (string, string, error) {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var parsed struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(content), &parsed); err == nil && strings.TrimSpace(parsed.Summary) != "" {
		title := normalizeSessionTitle(parsed.Title)
		if title == "" {
			title = "会话总结"
		}
		return title, strings.TrimSpace(parsed.Summary), nil
	}
	return extractTitleAndSummaryFromText(content)
}

// generateFallbackSummary 生成一个简单的回退总结
func generateFallbackSummary(conversationText string) (string, string, error) {
	turnCount := strings.Count(conversationText, "=== Turn ")
	title := fmt.Sprintf("会话总结 (%d轮)", turnCount)

	// 2026-09-29 (审计二十一轮): 按 rune 截断。此前 summary[:200] 按字节切，
	// CJK 会截成非法 UTF-8 前缀直接进响应体与 sessions.summary 列。
	summary := conversationText
	if runes := []rune(summary); len(runes) > 200 {
		summary = string(runes[:200]) + "…"
	}

	return title, summary, nil
}

// extractTitleAndSummaryFromText 从纯文本中提取标题和总结
func extractTitleAndSummaryFromText(text string) (string, string, error) {
	// Simple heuristic: first line is title, rest is summary
	lines := bytes.Split([]byte(text), []byte("\n"))

	if len(lines) == 0 {
		return "会话总结", text, nil
	}

	title := string(bytes.TrimSpace(lines[0]))
	if title == "" {
		title = "会话总结"
	}

	summary := ""
	if len(lines) > 1 {
		summary = string(bytes.TrimSpace(bytes.Join(lines[1:], []byte("\n"))))
	}

	if summary == "" {
		summary = text
	}

	return title, summary, nil
}
