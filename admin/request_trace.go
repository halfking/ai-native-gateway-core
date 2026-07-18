// Package admin — request_trace.go
//
// 2026-07-17: 请求链路追踪查看 API。
//
//	GET  /api/admin/requests/{request_id}/trace       — 拉取整次请求的链路事件
//	POST /api/admin/requests/{request_id}/ai-prompt   — 生成标准 AI 分析提示词
//
// 数据来源:
//   - 优先: Redis (request:trace:{request_id}, 进行中或刚完成的请求)
//   - 兜底: PostgreSQL (request_logs.trace_events, 已 flush 的历史)
//
// 设计参考 docs/design/request-trace-system.md, 复用 route_incidents.go
// 的"store + handler"分层与注册风格(参见 admin/route_incidents.go)。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	gwtrace "github.com/kaixuan/llm-gateway-go/internal/trace"
)

type traceLoadState string

const (
	traceStateReady       traceLoadState = "ready"
	traceStateNotReady    traceLoadState = "not_ready"
	traceStateUnavailable traceLoadState = "unavailable"
	traceStateMissing     traceLoadState = "missing"
)

var errTraceNotReady = errors.New("trace is not ready yet")
var errTraceUnavailable = errors.New("trace is unavailable")

// RequestTraceHandler 是只读 API 的处理器, 不修改任何状态。
type RequestTraceHandler struct {
	rdb *redis.Client
	db  *pgxpool.Pool
}

// NewRequestTraceHandler 构造 handler。rdb/db 任一为 nil 时该端点退化为 503。
func NewRequestTraceHandler(rdb *redis.Client, db *pgxpool.Pool) *RequestTraceHandler {
	return &RequestTraceHandler{rdb: rdb, db: db}
}

// RegisterRoutes 把路由注册到 admin mux。两者皆 super_admin only。
func (h *RequestTraceHandler) RegisterRoutes(mux *http.ServeMux, superAdmin func(http.HandlerFunc) http.HandlerFunc) {
	if h == nil {
		return
	}
	if superAdmin == nil {
		superAdmin = func(fn http.HandlerFunc) http.HandlerFunc { return fn }
	}
	mux.HandleFunc("/api/admin/requests/", superAdmin(h.handleSubrouter))
}

// handleSubrouter 分发 /api/admin/requests/{rid}/... 子路由。
func (h *RequestTraceHandler) handleSubrouter(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "method_not_allowed", "only GET/POST supported")
		return
	}
	// URL 形如 /api/admin/requests/{rid}/trace 或 /api/admin/requests/{rid}/ai-prompt
	const prefix = "/api/admin/requests/"
	remainder := strings.TrimPrefix(r.URL.Path, prefix)
	parts := strings.SplitN(remainder, "/", 2)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_path",
			"expected /api/admin/requests/{request_id}/trace or /ai-prompt")
		return
	}
	requestID := parts[0]
	switch parts[1] {
	case "trace":
		h.handleTrace(w, r, requestID)
	case "ai-prompt":
		h.handleAIPrompt(w, r, requestID)
	default:
		writeAdminError(w, http.StatusNotFound, "not_found",
			fmt.Sprintf("unknown subroute: %s", parts[1]))
	}
}

// handleTrace 拉取链路事件。
//
// Response 200:
//
//	{
//	  "request_id": "...",
//	  "events": [...],
//	  "final_status": "failed",
//	  "failed_at_stage": "upstream_request",
//	  "total_duration_ms": 1250,
//	  "source": "redis" | "postgres"
//	}
//
// 401/403 由 superAdmin wrapper 处理; 404 表示 redis miss + postgres miss。
func (h *RequestTraceHandler) handleTrace(w http.ResponseWriter, r *http.Request, requestID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	trace, source, err := h.loadTrace(ctx, requestID)
	if err != nil {
		if errors.Is(err, errTraceNotReady) {
			writeAdminError(w, http.StatusAccepted, "not_ready", "trace is still being persisted; retry shortly")
			return
		}
		if errors.Is(err, errTraceUnavailable) {
			writeAdminError(w, http.StatusConflict, "trace_unavailable", "request exists but trace data is unavailable")
			return
		}
		writeAdminError(w, http.StatusInternalServerError, "load_failed", "unable to load request trace")
		return
	}
	if trace == nil {
		writeAdminError(w, http.StatusNotFound, "not_found", "request or trace not found")
		return
	}

	writeAdminJSON(w, http.StatusOK, map[string]any{
		"request_id":        trace.RequestID,
		"events":            trace.Events,
		"final_status":      trace.FinalStatus,
		"failed_at_stage":   trace.FailedAtStage,
		"total_duration_ms": trace.TotalDurationMs,
		"source":            source,
	})
}

// loadTrace 先查 Redis, 失败再查 PG。
func (h *RequestTraceHandler) loadTrace(ctx context.Context, requestID string) (*gwtrace.RequestTrace, string, error) {
	if h.rdb != nil {
		rec := gwtrace.NewRedisRecorder(h.rdb)
		if trace, _, err := rec.Load(ctx, requestID); err == nil && trace != nil {
			return trace, "redis", nil
		}
	}
		if h.db != nil {
			trace, err := gwtrace.LoadFromPG(ctx, h.db, requestID)
			if err != nil {
				return nil, "", errTraceUnavailable
			}
			if trace != nil {
				return trace, "postgres", nil
			}
			if state, err := h.requestTraceState(ctx, requestID); err != nil {
				return nil, "", errTraceUnavailable
			} else if state == traceStateNotReady {
				return nil, "", errTraceNotReady
			} else if state == traceStateUnavailable {
				return nil, "", errTraceUnavailable
			}

		// Probe fallback: synthesize a trace from node_probe_runs so the
		// /trace + /ai-prompt endpoints work for probe rows.
		if isProbeRequestID(requestID) {
			if pt := h.buildProbeTrace(ctx, requestID); pt != nil {
				return pt, "probe_synthesized", nil
			}
		}
	}
	return nil, "", nil
}

func (h *RequestTraceHandler) requestTraceState(ctx context.Context, requestID string) (traceLoadState, error) {
	var status string
	var traceEvents []byte
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(request_status, ''), trace_events
		FROM request_logs
		WHERE request_id = $1
		LIMIT 1
	`, requestID).Scan(&status, &traceEvents)
	if errors.Is(err, pgx.ErrNoRows) {
		return traceStateMissing, nil
	}
	if err != nil {
		return traceStateUnavailable, err
	}
	if len(traceEvents) > 0 && string(traceEvents) != "null" {
		return traceStateReady, nil
	}
	if status == "in_progress" || status == "pending" || status == "" {
		return traceStateNotReady, nil
	}
	return traceStateUnavailable, nil
}


// aiPromptRequest 是 POST /ai-prompt 的 body。
type aiPromptRequest struct {
	UserQuestion string `json:"user_question"`
	// LangOverride: zh / en / ja, 留空跟随默认中文。
	LangOverride string `json:"lang_override,omitempty"`
}

// aiPromptResponse 是响应体。
type aiPromptResponse struct {
	Prompt         string `json:"prompt"`
	TokensEstimate int    `json:"tokens_estimate"`
	EventCount     int    `json:"event_count"`
}

// handleAIPrompt 生成可直接粘贴给 LLM 的故障分析提示词。
//
// 模板内容:
//   - 请求基本信息(请求 ID、时间、模型、状态)
//   - 失败阶段 + 最终错误信息
//   - 链路事件(每条带序号 + 阶段名 + 状态图标 + 耗时 + 详情)
//   - 失败事件的上下文快照(候选列表 / 节点探测状态 / 并发槽位)
//   - 占位的"我的问题"段
//
// 目的是减少运维写 prompt 的心智负担, 也让 LLM 拿到结构化的诊断上下文。
func (h *RequestTraceHandler) handleAIPrompt(w http.ResponseWriter, r *http.Request, requestID string) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	var req aiPromptRequest
	// 允许 body 为空 → 默认中文、默认问题占位
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.UserQuestion == "" {
		req.UserQuestion = "(请在此处粘贴你的具体问题, 例如：为什么这次 gpt-5.6-luna 请求超时了？)"
	}

	trace, _, err := h.loadTrace(ctx, requestID)
	if err != nil || trace == nil {
		writeAdminError(w, http.StatusNotFound, "not_found",
			"trace not found, cannot generate prompt")
		return
	}

	// 拉取 request_logs 中的基本信息(模型 / 状态 / 错误)
	reqSummary := h.fetchRequestSummary(ctx, requestID)

	prompt := buildAIPrompt(reqSummary, trace, req.UserQuestion)
	tokens := estimateTokensAIPrompt(prompt)

	writeAdminJSON(w, http.StatusOK, aiPromptResponse{
		Prompt:         prompt,
		TokensEstimate: tokens,
		EventCount:     len(trace.Events),
	})
}

// requestSummary 是 AI 提示词所需的最小请求元信息。
type requestSummary struct {
	RequestID     string `json:"request_id"`
	ClientModel   string `json:"client_model"`
	OutboundModel string `json:"outbound_model"`
	ProviderID    *int   `json:"provider_id,omitempty"`
	CredentialID  *int   `json:"credential_id,omitempty"`
	LatencyMs     *int   `json:"latency_ms,omitempty"`
	Success       *bool  `json:"success,omitempty"`
	ErrorKind     string `json:"error_kind,omitempty"`
	FailureStage  string `json:"failure_stage,omitempty"`
	Ts            string `json:"ts"`

	// 2026-07-17: probe 行的真实诊断元信息。request_logs 对 probe 行
	// 的 failure_stage / token 是占位值, 真正的失败原因在 auto_decision
	// JSONB 里。这里解析出来供 buildAIPrompt 的"探测详情"段展示。
	IsProbe       bool   `json:"-"`
	ProbeOrigin   string `json:"-"` // direct | gateway
	ProbeAttempt  int    `json:"-"`
	ProbeErrCode  string `json:"-"` // 如 http_429 / endpoint_build / network_error
	ProbeHTTP     int    `json:"-"` // 上游 HTTP 状态码
	ProbeStatus   string `json:"-"` // success/timeout/network/...
	ProbeViaProxy bool   `json:"-"`
}

func (h *RequestTraceHandler) fetchRequestSummary(ctx context.Context, requestID string) requestSummary {
	if h.db == nil {
		return requestSummary{RequestID: requestID}
	}
	row := h.db.QueryRow(ctx, `
		SELECT ts, client_model, outbound_model, provider_id, credential_id,
		       latency_ms, success, COALESCE(error_kind, ''), COALESCE(failure_stage, ''),
		       COALESCE(auto_decision::text, ''),
		       COALESCE(task_type, ''), COALESCE(task_type_chosen, '')
		FROM request_logs
		WHERE request_id = $1
		LIMIT 1
	`, requestID)
	var s requestSummary
	s.RequestID = requestID
	var ts time.Time
	var autoDecision, taskType, taskTypeChosen string
	if err := row.Scan(&ts, &s.ClientModel, &s.OutboundModel, &s.ProviderID,
		&s.CredentialID, &s.LatencyMs, &s.Success, &s.ErrorKind, &s.FailureStage,
		&autoDecision, &taskType, &taskTypeChosen); err != nil {
		// 没找到就返回半填充, prompt 中标注
		return requestSummary{RequestID: requestID}
	}
	if !ts.IsZero() {
		s.Ts = ts.UTC().Format(time.RFC3339)
	}
	if taskType == "probe_triggered" {
		s.IsProbe = true
		s.ProbeOrigin = strings.TrimPrefix(taskTypeChosen, "probe_")
		if s.ProbeOrigin == "" {
			s.ProbeOrigin = "direct"
		}
		// Parse the auto_decision JSONB for the real probe diagnostics.
		if autoDecision != "" {
			var ad map[string]any
			if json.Unmarshal([]byte(autoDecision), &ad) == nil {
				s.ProbeAttempt = atoiSafe(ad["probe_attempt"])
				s.ProbeErrCode = strval(ad["probe_err_code"])
				s.ProbeHTTP = atoiSafe(ad["probe_http_status"])
				s.ProbeStatus = strval(ad["probe_status"])
				if v, ok := ad["probe_via_proxy"].(bool); ok {
					s.ProbeViaProxy = v
				}
			}
		}
	}
	return s
}

func atoiSafe(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func strval(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return strings.Trim(string(b), "\"")
}

// buildAIPrompt 生成 Markdown 提示词。
//
// 设计原则:
//   - 不冗余展示完整 JSON, 因为 LLM 需要的是"可读性"而非原始字节
//   - 用 emoji 给视觉锚点(✅❌⏱️⏭️), 让 LLM 快速定位失败阶段
//   - 失败事件的 Snapshot 折叠为短字段列表, 完整 trace JSON 放最后供深度排查
func buildAIPrompt(s requestSummary, t *gwtrace.RequestTrace, userQuestion string) string {
	var sb strings.Builder

	sb.WriteString("# 请求故障分析请求\n\n")

	// 基本信息
	sb.WriteString("## 基本信息\n\n")
	fmt.Fprintf(&sb, "- 请求 ID: `%s`\n", s.RequestID)
	if s.Ts != "" {
		fmt.Fprintf(&sb, "- 时间: %s\n", s.Ts)
	}
	if s.ClientModel != "" {
		fmt.Fprintf(&sb, "- 客户端模型: `%s`\n", s.ClientModel)
	}
	if s.OutboundModel != "" && s.OutboundModel != s.ClientModel {
		fmt.Fprintf(&sb, "- 出站模型: `%s`\n", s.OutboundModel)
	}
	if s.ProviderID != nil {
		fmt.Fprintf(&sb, "- Provider ID: %d\n", *s.ProviderID)
	}
	if s.CredentialID != nil {
		fmt.Fprintf(&sb, "- Credential ID: %d\n", *s.CredentialID)
	}
	if s.LatencyMs != nil {
		fmt.Fprintf(&sb, "- 总耗时: %dms\n", *s.LatencyMs)
	}
	if s.Success != nil {
		statusIcon := "❌"
		statusText := "失败"
		if *s.Success {
			statusIcon = "✅"
			statusText = "成功"
		}
		fmt.Fprintf(&sb, "- 状态: %s %s\n", statusIcon, statusText)
	}
	if s.ErrorKind != "" {
		fmt.Fprintf(&sb, "- 错误类型: `%s`\n", s.ErrorKind)
	}
	if s.FailureStage != "" {
		fmt.Fprintf(&sb, "- 失败阶段: `%s`\n", s.FailureStage)
	}
	sb.WriteString("\n")

	// 探测详情段(仅 probe 行): request_logs 对 probe 行的 token/failure_stage
	// 是占位值, 这里把 auto_decision JSONB 里的真实诊断(err_code/http_status/
	// via_proxy/origin)显式列出, 让 LLM 拿到真实根因而非误导性的"probe failed"。
	if s.IsProbe {
		sb.WriteString("## 探测详情\n\n")
		fmt.Fprintf(&sb, "- 探测类型: `%s` (第 %d 轮)\n", s.ProbeOrigin, s.ProbeAttempt)
		if s.ProbeErrCode != "" {
			fmt.Fprintf(&sb, "- 错误码: `%s`\n", s.ProbeErrCode)
		}
		if s.ProbeHTTP > 0 {
			fmt.Fprintf(&sb, "- 上游 HTTP 状态: %d\n", s.ProbeHTTP)
		}
		if s.ProbeStatus != "" {
			fmt.Fprintf(&sb, "- 探测状态: `%s`\n", s.ProbeStatus)
		}
		fmt.Fprintf(&sb, "- 是否走代理: %v\n", s.ProbeViaProxy)
		sb.WriteString("\n")
		// 合成 trace 的事件 Details 里带 request_url/request_body/response_body,
		// 这里从第一条事件(直连轮)提取出来, 避免让 LLM 翻原始 JSON。
		if len(t.Events) > 0 {
			ev := t.Events[0]
			if v, ok := ev.Details["request_url"]; ok {
				fmt.Fprintf(&sb, "- 请求 URL: `%v`\n", v)
			}
			if v, ok := ev.Details["request_body"]; ok {
				fmt.Fprintf(&sb, "- 请求体: `%s`\n", traceTruncate(fmt.Sprintf("%v", v), 200))
			}
			if v, ok := ev.Details["response_body"]; ok {
				fmt.Fprintf(&sb, "- 响应体: `%s`\n", traceTruncate(fmt.Sprintf("%v", v), 300))
			}
			if v, ok := ev.Details["err_detail"]; ok {
				fmt.Fprintf(&sb, "- 错误详情: `%s`\n", traceTruncate(fmt.Sprintf("%v", v), 300))
			}
		}
		sb.WriteString("\n")
	}

	// 链路事件
	sb.WriteString(fmt.Sprintf("## 链路事件 (共 %d 步)\n\n", len(t.Events)))
	for _, ev := range t.Events {
		icon := "✅"
		switch ev.Status {
		case gwtrace.StatusFailed:
			icon = "❌"
		case gwtrace.StatusTimeout:
			icon = "⏱️"
		case gwtrace.StatusSkipped:
			icon = "⏭️"
		}
		durText := ""
		if ev.DurationMs > 0 {
			durText = fmt.Sprintf(" %dms", ev.DurationMs)
		}
		fmt.Fprintf(&sb, "%d. %s `%s`%s", ev.Seq, icon, string(ev.Stage), durText)
		if ev.Error != "" {
			fmt.Fprintf(&sb, " — %s", traceTruncate(ev.Error, 200))
		}
		sb.WriteString("\n")

		// 详情
		if len(ev.Details) > 0 {
			for k, v := range ev.Details {
				fmt.Fprintf(&sb, "   - %s: %v\n", k, traceTruncate(fmt.Sprintf("%v", v), 120))
			}
		}

		// 快照(仅失败事件)
		if ev.Snapshot != nil && len(ev.Snapshot.Candidates) > 0 {
			fmt.Fprintf(&sb, "   - 📦 候选列表: %d 个\n", len(ev.Snapshot.Candidates))
		}
		if ev.Snapshot != nil && len(ev.Snapshot.NodeProbeState) > 0 {
			sb.WriteString("   - 📦 节点探测状态:\n")
			for k, v := range ev.Snapshot.NodeProbeState {
				fmt.Fprintf(&sb, "     - %s: %v\n", k, traceTruncate(fmt.Sprintf("%v", v), 80))
			}
		}
		if ev.Snapshot != nil && ev.Snapshot.ConcurrencySlot != nil {
			fmt.Fprintf(&sb, "   - 📦 并发槽位: %d/%d (blocked=%v)\n",
				ev.Snapshot.ConcurrencySlot.InUse,
				ev.Snapshot.ConcurrencySlot.MaxSlots,
				ev.Snapshot.ConcurrencySlot.Blocked)
		}
		if ev.Snapshot != nil && ev.Snapshot.FailureHint != "" {
			fmt.Fprintf(&sb, "   - 💡 失败归类: %s\n", ev.Snapshot.FailureHint)
		}
	}

	// 失败阶段标注
	if t.FailedAtStage != "" {
		sb.WriteString("\n")
		fmt.Fprintf(&sb, "**失败停在: `%s`**\n\n", string(t.FailedAtStage))
	}

	// 原始 trace JSON
	sb.WriteString("\n## 原始 Trace JSON (供深度参考)\n\n```json\n")
	rawJSON, _ := json.MarshalIndent(t, "", "  ")
	sb.Write(rawJSON)
	sb.WriteString("\n```\n\n")

	// 用户问题
	sb.WriteString("## 我的问题\n\n")
	sb.WriteString(userQuestion)
	sb.WriteString("\n\n")

	// 提示 LLM 输出格式
	sb.WriteString("---\n\n")
	sb.WriteString("请按以下结构回答:\n")
	sb.WriteString("1. **直接结论**: 用 1-2 句话回答上面的问题\n")
	sb.WriteString("2. **根因分析**: 引用链路事件中具体步骤 + 相关字段\n")
	sb.WriteString("3. **修复建议**: 提供可执行的修改点(代码 / 配置 / 上游)\n")
	sb.WriteString("4. **预防措施**: 如何在 metric / 日志 / 告警中避免下次重蹈覆辙\n")

	return sb.String()
}

// estimateTokensAIPrompt 用粗略公式估算 token 数 (≈ len/3.5, 中文稍多)。
func estimateTokensAIPrompt(s string) int {
	return int(float64(len(s)) / 3.5)
}

func traceTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// ─── probe 行 trace 合成 (2026-07-17) ─────────────────────────────────────────
//
// probe 行(emitter 写入)在 request_logs.trace_events 永远是 NULL, 导致
// /trace 与 /ai-prompt 对 probe 请求 404。真实诊断数据其实已记录在
// node_probe_runs 表(request_url / request_body / response_body /
// direct_err_code / via_proxy), 这里从该表合成一条 RequestTrace, 让运维
// 不用手写 SQL 就能在 dashboard / AI prompt 里看到 probe 的请求与响应。

// probeRequestIDRe 解析 probe request_id: probe-direct-c{cred}-m{model}-a{att}-{ok|fail}-{ns}
// model 段经过 sanitizeModelForID 处理(非字母数字→_, 截断40), 故仅用于
// 定位 cred + 时间窗口, 不用于精确匹配 raw_model_name。
var probeRequestIDRe = regexp.MustCompile(`^probe-direct-c(\d+)-m(.+)-a(\d+)-(ok|fail)-(\d+)$`)

func isProbeRequestID(requestID string) bool {
	return probeRequestIDRe.MatchString(requestID)
}

// buildProbeTrace 从 node_probe_runs 合成一条单事件 RequestTrace。
// 返回 nil 表示没找到匹配的 audit 行(老数据 / 旧路径未写表)。
//
// 2026-07-17 (audit fix):
//   - 匹配 sanitized model 段(m[2]), 防止同一 credential 在 ±2 分钟内对多个
//     model 各跑 probe 时, 最近邻查询返回无关 model 的行(张冠李戴)。
//   - 读取 request_logs.auto_decision.probe_origin, 据此渲染 direct 或
//     gateway 轮的列。request_id 前缀不区分 origin(都拼成 probe-direct-...),
//     所以必须从该行自身的元信息判断它属于哪一轮。
func (h *RequestTraceHandler) buildProbeTrace(ctx context.Context, requestID string) *gwtrace.RequestTrace {
	m := probeRequestIDRe.FindStringSubmatch(requestID)
	if m == nil {
		return nil
	}
	credID, _ := strconv.Atoi(m[1])
	sanitizedModel := m[2] // sanitized by sanitizeModelForID on the write side
	ns, _ := strconv.ParseInt(m[5], 10, 64)
	ts := time.Unix(0, ns)
	if ns <= 0 {
		ts = time.Now()
	}

	// Determine which round this request_id represents. request_logs writes
	// task_type_chosen='probe_direct'|'probe_gateway' into auto_decision; the
	// request_id prefix is identical for both rounds, so the id alone can't
	// tell us. Default to "direct" when the lookup fails (legacy rows).
	origin := "direct"
	if h.db != nil {
		var chosen string
		_ = h.db.QueryRow(ctx, `
			SELECT COALESCE(task_type_chosen, '')
			FROM request_logs
			WHERE request_id = $1
			LIMIT 1
		`, requestID).Scan(&chosen)
		if o := strings.TrimPrefix(chosen, "probe_"); o == "gateway" || o == "direct" {
			origin = o
		}
	}

	row := h.db.QueryRow(ctx, `
		SELECT raw_model_name, attempt,
		       direct_ok, direct_http_status, direct_err_code, direct_latency_ms, direct_err_detail,
		       gateway_ok, gateway_http_status, gateway_err_code, gateway_latency_ms, gateway_err_detail,
		       outbound_model, provider_id,
		       request_url, request_body, response_body, via_proxy,
		       duration_ms
		FROM node_probe_runs
		WHERE credential_id = $1
		  AND LEFT(regexp_replace(raw_model_name, '[^a-zA-Z0-9-_.]', '_', 'g'), 40) = $2
		  AND started_at BETWEEN $3 - interval '2 minutes' AND $3 + interval '2 minutes'
		ORDER BY ABS(EXTRACT(EPOCH FROM (started_at - $3))) ASC, started_at DESC
		LIMIT 1
	`, credID, sanitizedModel, ts)

	var (
		rawModel                              string
		attempt                               int
		directOK                              bool
		directHTTP                            *int
		directErrCode, directErrDetail        *string
		directLatency                         *int
		gwOK                                  bool
		gwHTTP                                *int
		gwErrCode                             *string
		gwLatency                             *int
		gwErrDetail                           *string
		outboundModel                         *string
		providerID                            *int
		requestURL, requestBody, responseBody *string
		viaProxy                              *bool
		durationMs                            int
	)
	if err := row.Scan(&rawModel, &attempt,
		&directOK, &directHTTP, &directErrCode, &directLatency, &directErrDetail,
		&gwOK, &gwHTTP, &gwErrCode, &gwLatency, &gwErrDetail,
		&outboundModel, &providerID,
		&requestURL, &requestBody, &responseBody, &viaProxy,
		&durationMs); err != nil {
		return nil
	}

	// Select the round matching this request_id's origin. The request_url /
	// request_body / response_body / via_proxy columns are populated by
	// probeDirect (direct round); the gateway round reuses the same request
	// context columns in node_probe_runs (probeGateway also records them).
	roundOK := directOK
	roundHTTP := directHTTP
	roundErrCode := directErrCode
	roundErrDetail := directErrDetail
	if origin == "gateway" {
		roundOK = gwOK
		roundHTTP = gwHTTP
		roundErrCode = gwErrCode
		roundErrDetail = gwErrDetail
	}

	details := map[string]any{
		"raw_model":    rawModel,
		"attempt":      attempt,
		"probe_origin": origin,
	}
	if requestURL != nil {
		details["request_url"] = *requestURL
	}
	if requestBody != nil {
		details["request_body"] = traceTruncate(*requestBody, 300)
	}
	if roundHTTP != nil {
		details["http_status"] = *roundHTTP
	}
	if roundErrCode != nil {
		details["err_code"] = *roundErrCode
	}
	if roundErrDetail != nil {
		details["err_detail"] = traceTruncate(*roundErrDetail, 300)
	}
	if responseBody != nil {
		details["response_body"] = traceTruncate(*responseBody, 300)
	}
	if viaProxy != nil {
		details["via_proxy"] = *viaProxy
	}
	// Always include both rounds' summary so the operator can compare.
	gwSummary := map[string]any{"ok": gwOK}
	if gwHTTP != nil {
		gwSummary["http_status"] = *gwHTTP
	}
	if gwErrCode != nil {
		gwSummary["err_code"] = *gwErrCode
	}
	directSummary := map[string]any{"ok": directOK}
	if directHTTP != nil {
		directSummary["http_status"] = *directHTTP
	}
	if directErrCode != nil {
		directSummary["err_code"] = *directErrCode
	}
	details["gateway_round"] = gwSummary
	details["direct_round"] = directSummary

	evStatus := gwtrace.StatusSuccess
	var errMsg string
	failureHint := ""
	if !roundOK {
		evStatus = gwtrace.StatusFailed
		if roundErrCode != nil {
			errMsg = *roundErrCode
			failureHint = "probe_" + *roundErrCode
		} else {
			errMsg = "probe failed (no err_code)"
			failureHint = "probe_failed"
		}
		if roundErrDetail != nil {
			errMsg = errMsg + ": " + traceTruncate(*roundErrDetail, 200)
		}
	}

	ev := gwtrace.TraceEvent{
		Seq:        1,
		Stage:      gwtrace.StageUpstreamRequest,
		StageName:  "探测" + map[string]string{"direct": "直连上游", "gateway": "经网关"}[origin],
		Module:     gwtrace.ModuleUpstream,
		Timestamp:  ts,
		DurationMs: durationMs,
		Status:     evStatus,
		Details:    details,
		Error:      errMsg,
	}
	if failureHint != "" {
		ev.Snapshot = &gwtrace.Snapshot{
			CapturedAt:  ts,
			FailureHint: failureHint,
			Extra:       details,
		}
	}

	finalStatus := gwtrace.FinalSuccess
	failedStage := gwtrace.Stage("")
	if !roundOK {
		finalStatus = gwtrace.FinalFailed
		failedStage = gwtrace.StageUpstreamRequest
	}
	return &gwtrace.RequestTrace{
		RequestID:       requestID,
		Events:          []gwtrace.TraceEvent{ev},
		FinalStatus:     finalStatus,
		FailedAtStage:   failedStage,
		TotalDurationMs: durationMs,
	}
}

// writeAdminJSON 是统一的 JSON 输出。
func writeAdminJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeAdminError 写统一错误结构。
func writeAdminError(w http.ResponseWriter, status int, code, msg string) {
	writeAdminJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
