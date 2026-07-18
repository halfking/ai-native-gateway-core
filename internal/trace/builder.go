package trace

import (
	"context"
	"fmt"
	"time"

	i18n "github.com/kaixuan/llm-gateway-go/i18n"
)

// StageDisplayName 把 Stage 映射到 i18n key (前端 i18n 表用,后端只填占位)。
//
// 不在后端做实际翻译,避免后端耦合前端 i18n 资源。
// 后端只填 "trace.stage.receive_request" 这种 key,前端查表替换。
func StageDisplayNameKey(s Stage) string {
	return "trace.stage." + string(s)
}

// EventBuilder 是给调用方用的"事件构造器",按阶段封装常用字段。
//
// 用法示例:
//
//	trace.ReceiveRequest("POST", "/v1/chat/completions", "1.2.3.4").Append(ctx, recorder, requestID)
//
// 所有方法都返回 EventBuilder,可链式调用 .WithDuration / .WithDetails。
type EventBuilder struct {
	stage    Stage
	module   Module
	status   Status
	duration time.Duration
	details  map[string]any
	errMsg   string
	snap     *Snapshot
	ts       time.Time
}

// ReceiveRequest 构造 receive_request 事件。
func ReceiveRequest(method, path, clientIP string) EventBuilder {
	return EventBuilder{
		stage:  StageReceiveRequest,
		module: ModuleMiddleware,
		status: StatusSuccess,
		details: map[string]any{
			"method":    method,
			"path":      path,
			"client_ip": clientIP,
		},
	}
}

// Authenticate 构造 authenticate 事件。
func Authenticate(apiKeyID int64, tenantID string, ok bool) EventBuilder {
	st := StatusSuccess
	errMsg := ""
	if !ok {
		st = StatusFailed
		errMsg = "auth_failed"
	}
	return EventBuilder{
		stage:  StageAuthenticate,
		module: ModuleAuth,
		status: st,
		errMsg: errMsg,
		details: map[string]any{
			"api_key_id": apiKeyID,
			"tenant_id":  tenantID,
		},
	}
}

// RateLimitCheck 构造 rate_limit_check 事件。
func RateLimitCheck(passed bool, kind string, remaining int) EventBuilder {
	st := StatusSuccess
	if !passed {
		st = StatusFailed
	}
	return EventBuilder{
		stage:  StageRateLimit,
		module: ModuleRatelimit,
		status: st,
		details: map[string]any{
			"passed":    passed,
			"kind":      kind,
			"remaining": remaining,
		},
	}
}

// BodyParse 构造 body_parse 事件。
func BodyParse(bodySize int, err error) EventBuilder {
	st := StatusSuccess
	errMsg := ""
	if err != nil {
		st = StatusFailed
		errMsg = err.Error()
	}
	return EventBuilder{
		stage:  StageBodyParse,
		module: ModuleHandler,
		status: st,
		errMsg: errMsg,
		details: map[string]any{
			"body_size": bodySize,
		},
	}
}

// SessionLookup 构造 session_lookup 事件。
func SessionLookup(sessionID string, isNew bool, err error) EventBuilder {
	st := StatusSuccess
	errMsg := ""
	if err != nil {
		st = StatusFailed
		errMsg = err.Error()
	}
	return EventBuilder{
		stage:  StageSessionLookup,
		module: ModuleSession,
		status: st,
		errMsg: errMsg,
		details: map[string]any{
			"session_id": sessionID,
			"is_new":     isNew,
		},
	}
}

// RouteResolve 构造 route_resolve 事件。
func RouteResolve(model string, candidateCount int) EventBuilder {
	return EventBuilder{
		stage:  StageRouteResolve,
		module: ModuleHandler,
		status: StatusSuccess,
		details: map[string]any{
			"model":           model,
			"candidate_count": candidateCount,
		},
	}
}

// RouteCredential 构造 route_credential 事件。
func RouteCredential(providerID, credentialID int, model, rawModel, tier, billingMode string) EventBuilder {
	return EventBuilder{
		stage:  StageRouteCredential,
		module: ModuleExecutor,
		status: StatusSuccess,
		details: map[string]any{
			"provider_id":   providerID,
			"credential_id": credentialID,
			"model":         model,
			"raw_model":     rawModel,
			"tier":          tier,
			"billing_mode":  billingMode,
		},
	}
}

// UpstreamRequest 构造 upstream_request 事件。
func UpstreamRequest(url string, timeoutMs int, viaProxy bool) EventBuilder {
	return EventBuilder{
		stage:  StageUpstreamRequest,
		module: ModuleUpstream,
		status: StatusSuccess,
		details: map[string]any{
			"url":        url,
			"timeout_ms": timeoutMs,
			"via_proxy":  viaProxy,
		},
	}
}

// UpstreamFailure 是 UpstreamRequest 的失败版本 (带 Snapshot)。
func UpstreamFailure(url string, statusCode int, err error, model string, credentialID int) EventBuilder {
	eb := EventBuilder{
		stage:  StageUpstreamRequest,
		module: ModuleUpstream,
		status: StatusFailed,
		errMsg: fmt.Sprintf("http_status=%d err=%v", statusCode, err),
		details: map[string]any{
			"url":         url,
			"http_status": statusCode,
		},
	}
	if err != nil {
		eb.details["error"] = err.Error()
	}
	if statusCode > 0 {
		eb.details["http_status"] = statusCode
	}
	if hint := classifyUpstreamError(err, statusCode); hint != "" {
		eb.details["failure_hint"] = hint
	}
	return eb
}

// UpstreamFailureWithBody 增强版 UpstreamFailure，携带 upstream.Error 的完整上下文
// (StatusCode, Body, Headers)。用于 5xx 错误详细诊断（满足 rule 需求：不只是 5xx 标签）。
func UpstreamFailureWithBody(url string, uErr error) EventBuilder {
	eb := EventBuilder{
		stage:  StageUpstreamRequest,
		module: ModuleUpstream,
		status: StatusFailed,
		errMsg: fmt.Sprintf("upstream_error: %v", uErr),
		details: map[string]any{
			"url": url,
		},
	}
	if uErr == nil {
		return eb
	}

	// 类型断言提取 upstream.Error
	type upstreamError interface {
		error
		StatusCode() int
		Body() []byte
	}

	// 尝试断言为 *upstream.Error（通过接口模式避免循环导入）
	var statusCode int
	var body []byte
	if ue, ok := uErr.(upstreamError); ok {
		statusCode = ue.StatusCode()
		body = ue.Body()
	}

	if statusCode > 0 {
		eb.details["http_status"] = statusCode
	}
	if len(body) > 0 {
		bodyStr := string(body)
		if len(bodyStr) > 512 {
			bodyStr = bodyStr[:512] + "..." // 限制 trace_events 大小
		}
		eb.details["response_body"] = bodyStr
		eb.details["response_body_len"] = len(body)
	}

	eb.details["error"] = uErr.Error()
	if hint := classifyUpstreamError(uErr, statusCode); hint != "" {
		eb.details["failure_hint"] = hint
	}

	return eb
}

// StreamStart 构造 stream_start 事件。
func StreamStart(ttfbMs int) EventBuilder {
	return EventBuilder{
		stage:  StageStreamStart,
		module: ModuleUpstream,
		status: StatusSuccess,
		details: map[string]any{
			"ttfb_ms": ttfbMs,
		},
	}
}

// StreamChunk 构造 stream_chunk 事件（按一次请求汇总写入，而非每个 chunk 写 Redis）。
func StreamChunk(chunkCount int, totalBytes int) EventBuilder {
	return EventBuilder{
		stage:  StageStreamChunk,
		module: ModuleUpstream,
		status: StatusSuccess,
		details: map[string]any{
			"chunk_count": chunkCount,
			"total_bytes": totalBytes,
		},
	}
}

// StreamComplete 构造 stream_complete 事件 (流式响应结束)。
func StreamComplete(chunkCount int, totalBytes int, finishReason string) EventBuilder {
	return EventBuilder{
		stage:  StageStreamComplete,
		module: ModuleUpstream,
		status: StatusSuccess,
		details: map[string]any{
			"chunk_count":   chunkCount,
			"total_bytes":   totalBytes,
			"finish_reason": finishReason,
		},
	}
}

// RequestComplete 构造 request_complete 事件 (整次请求退出)。
func RequestComplete(finalStatus FinalStatus, failedStage Stage, errMsg string, snapshot *Snapshot) EventBuilder {
	eb := EventBuilder{
		stage:  StageRequestComplete,
		module: ModuleHandler,
		status: Status(finalStatus), // 复用 Status 枚举(final_status 也是 success/failed/timeout)
		errMsg: errMsg,
		details: map[string]any{
			"failed_stage": string(failedStage),
		},
		snap: snapshot,
	}
	return eb
}

// ─── 链式方法 ────────────────────────────────────────────────────────────────

// WithDuration 显式指定 duration (覆盖 time.Since)。
func (b EventBuilder) WithDuration(d time.Duration) EventBuilder {
	b.duration = d
	return b
}

// WithDetails 追加 details 字段 (后写覆盖先写)。
func (b EventBuilder) WithDetails(kv ...any) EventBuilder {
	if b.details == nil {
		b.details = map[string]any{}
	}
	for i := 0; i+1 < len(kv); i += 2 {
		k, ok := kv[i].(string)
		if !ok {
			continue
		}
		b.details[k] = kv[i+1]
	}
	return b
}

// WithError 设置错误信息。
func (b EventBuilder) WithError(err error) EventBuilder {
	if err != nil {
		b.status = StatusFailed
		b.errMsg = err.Error()
	}
	return b
}

// WithSnapshot 在失败事件上挂载快照。
func (b EventBuilder) WithSnapshot(snap *Snapshot) EventBuilder {
	b.snap = snap
	return b
}

// WithTimestamp 显式指定时间 (用于"事件发生 → 注入 trace" 中间有时延的情况)。
func (b EventBuilder) WithTimestamp(ts time.Time) EventBuilder {
	b.ts = ts
	return b
}

// Build 渲染为 TraceEvent。供 Append 时使用。
func (b EventBuilder) Build() TraceEvent {
	ts := b.ts
	if ts.IsZero() {
		ts = time.Now()
	}
	return TraceEvent{
		Stage:      b.stage,
		StageName:  StageDisplayNameKey(b.stage),
		Module:     b.module,
		Timestamp:  ts,
		DurationMs: int(b.duration.Milliseconds()),
		Status:     b.status,
		Details:    b.details,
		Error:      b.errMsg,
		Snapshot:   b.snap,
	}
}

// Append 一站式调用: Build + recorder.Append。
func (b EventBuilder) Append(ctx context.Context, rec Recorder, requestID string) {
	if rec == nil || requestID == "" {
		return
	}
	if err := rec.Append(ctx, requestID, b.Build()); err != nil {
		// recorder 内部已经 slog.Warn,这里 swallow。
		_ = err
	}
}

// ─── 辅助 ────────────────────────────────────────────────────────────────────

// classifyUpstreamError 把上游错误归类为人可读提示。
// 与 docs/design/request-trace-system.md §4 的 ai-prompt 维度对应。
func classifyUpstreamError(err error, statusCode int) string {
	if err == nil && statusCode == 0 {
		return ""
	}
	if err != nil {
		msg := err.Error()
		switch {
		case contains(msg, "timeout"):
			return "upstream_timeout"
		case contains(msg, "connection refused"):
			return "upstream_unreachable"
		case contains(msg, "reset"):
			return "upstream_reset"
		case contains(msg, "i/o"):
			return "upstream_io_error"
		}
		return "upstream_network_error"
	}
	switch {
	case statusCode >= 500:
		return "upstream_5xx"
	case statusCode == 429:
		return "upstream_rate_limit"
	case statusCode == 401, statusCode == 403:
		return "upstream_auth_error"
	case statusCode == 404:
		return "upstream_not_found"
	case statusCode >= 400:
		return "upstream_4xx"
	}
	return ""
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Avoid unused import: i18n reserved for future backend-side message catalog.
var _ = i18n.T
