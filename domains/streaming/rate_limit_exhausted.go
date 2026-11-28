package streaming

// rate_limit_exhausted.go —— 2026-11-28：「全部候选都被上游限流」的客户端契约。
//
// ── 为什么需要它 ──────────────────────────────────────────────
//
// chat 面（handler.go）自 2026-09-07 起就有专属出口：HTTP 429 + Retry-After +
// retryable=true。/v1/messages 与 /v1/responses 一直没有 ⇒ 同一个「候选耗尽且
// 最后错误是 429」落到通用兜底：客户端收到 overloaded_error "All providers
// unavailable"，落库 error_kind=model_not_found。
//
// 211 实测（glm-5.3-flash，2026-11-28 14:47Z）：ZCode 把它显示成
// provider_code=PROVIDER_BUSINESS_ERROR retryable=false ⇒ 整轮直接失败；运维在
// 库里按 model_not_found 检索，与「这个模型真没节点」分不开——两者的处置相反。
//
// ★ 缺口形态与 2026-11-18「熔断打空」（provider_circuit_open.go）完全同型：
// kind 没错，是协议面没接。因此本契约必须三面同时接线，并有面级端到端钉测。
//
// ── 契约 ──────────────────────────────────────────────────────
//
//	HTTP 429（限流是临时状态，不是调用方的错）
//	error.code = rate_limit
//	Retry-After: <秒>（上游提示优先，封顶；见 overloadRetryAfterSeconds）
//	retryable = true（客户端应当退避后重试）
//
// chat 面的内联实现保持不动（既有判据钉着它的信封）；本文件是另外两面的
// 判定来源，三面共用同一份 kind 判据与文案。

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// RateLimitExhaustedCode 是「全部候选被上游限流」时回给客户端的 error.code。
const RateLimitExhaustedCode = "rate_limit"

// rateLimitExhaustedSurface 是限流耗尽时**客户端**该看到的契约。
type rateLimitExhaustedSurface struct {
	// Status 恒为 429。
	Status int
	// Message 是面向调用方的可读说明。
	Message string
	// Code 恒为 rate_limit。
	Code string
	// RetryAfter 是客户端该等多久再重试（秒）。
	RetryAfter int
}

// rateLimitExhaustedRejection 判定「这次失败是所有候选都被上游限流」。
//
// 判据：ExecuteError.Exhausted 且 LastKind == rate_limit。未耗尽的限流错误走
// 各面既有的 "Upstream request failed" 路径，不在本契约内。
func rateLimitExhaustedRejection(ee *executors.ExecuteError, clientModel string) (rateLimitExhaustedSurface, bool) {
	if ee == nil || !ee.Exhausted || ee.LastKind != errorsx.KindRateLimit {
		return rateLimitExhaustedSurface{}, false
	}
	return rateLimitExhaustedSurface{
		Status:     http.StatusTooManyRequests,
		Message:    fmt.Sprintf("Rate limited by all providers for model '%s'. All %d candidates failed.", clientModel, ee.Tried),
		Code:       RateLimitExhaustedCode,
		RetryAfter: overloadRetryAfterSeconds(ee),
	}, true
}

// setRateLimitHeaders 写出限流耗尽的两个响应头。
//
// ★ 流已预热（HTTP 200 已发出）时响应头到不了线上——那种场合 retryable 与
// retry_after 由调用点放进流内错误帧的 extras；本函数只负责非流式分支。
func setRateLimitHeaders(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("X-Gateway-Last-Kind", string(errorsx.KindRateLimit))
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
}

// rateLimitExtras 是流内错误帧与非流式信封共用的附加字段。
func rateLimitExtras(surf rateLimitExhaustedSurface) map[string]any {
	return map[string]any{"retryable": true, "retry_after": surf.RetryAfter}
}
