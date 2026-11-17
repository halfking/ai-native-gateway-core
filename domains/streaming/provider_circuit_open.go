package streaming

// provider_circuit_open.go —— W1-A（2026-11-18）：「上游熔断把候选打空」
// 的**客户端契约**。三协议面（chat / anthropic-messages / responses）共用
// 这一个判据，避免三个面各写各的、彼此漂移。
//
// ── 为什么需要它 ──────────────────────────────────────────────
//
// 熔断准入在 domains/credential/breaker.go（per-(provider, credential)）。
// 全部候选都被熔断拒掉时，请求落到 handler 的 Exhausted 分支。修前该分支
// **没有** KindCircuitOpen 的专属出口 ⇒ 熔断耗尽与「这个模型真没节点」
// 一起落到通用兜底 errCode="model_not_found"（503）⇒ 客户端分不清
// 「退避后重试即可」与「换模型/上报配置」——这两者的处置是相反的。
//
// ★ 缺口形态与 2026-11-11「请求形态拒绝」那一轮**完全同型**：那次是
// 「协议面没接」——只给 chat 面加了分支，/v1/messages 与 /v1/responses 仍
// 落通用兜底 503。因此本轮的契约必须**三面同时接线**，并有面级端到端钉测。
//
// ── 契约 ──────────────────────────────────────────────────────
//
//	HTTP 503（熔断是临时状态，不是调用方的错）
//	error.code = provider_circuit_open（专属码，客户端可按码分流）
//	error.kind = circuit_open（errorsx 分类，便于与运维侧对齐）
//	Retry-After: <秒>（来自熔断器自己的冷却截止）
//	gateway_debug.retryable = true（客户端应当退避后重试）
//
// ★ 为什么用 error.code 而不是 error.kind 承载客户端契约：kind 只在 OpenAI
// 信封上出现（writeErrorJSONWithKind），Anthropic 信封（writeAnthropicError）
// 没有这个字段 ⇒ 用 kind 做客户端契约会在协议切换时静默失效。code 两个协议
// 都在（Anthropic 走 extras 注入）。

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

const (
	// ProviderCircuitOpenCode 是熔断耗尽时回给客户端的 error.code。
	//
	// ★ 为什么必须是一个**专属码**而不是复用 model_not_found：「所有候选都
	// 被熔断」与「这个模型真的没有节点」对调用方是相反的处置 —— 前者退避
	// 后重试即可，后者要换模型/换供应商/上报配置。两者共用一个码 ⇒ 调用方
	// 只能把临时熔断当永久缺货处理。
	ProviderCircuitOpenCode = "provider_circuit_open"

	// providerCircuitOpenDefaultRetryAfter 是算不出具体冷却时长时的缺省
	// 退避秒数。与仓内既有的 quotaHeld 缺省（30s）保持一致。
	providerCircuitOpenDefaultRetryAfter = 30
)

// providerCircuitOpenSurface 是熔断耗尽时**客户端**该看到的契约。
//
// 用结构体而不是一串返回值：三个协议面各自要取其中不同的字段（状态码 /
// 文案 / 错误码 / 退避秒数），裸元组在调用点会退化成 `_ = x` 之类的噪音。
type providerCircuitOpenSurface struct {
	// Status 恒为 503（熔断是临时状态，不是调用方的错）。
	Status int
	// Message 是面向调用方的可读说明。
	Message string
	// Code 是专属错误码（provider_circuit_open）。
	Code string
	// RetryAfter 是客户端该等多久再重试（秒）。
	RetryAfter int
}

// providerCircuitOpenRejection 判定「这次失败是熔断把候选打空」，并给出
// 客户端该看到的契约。
//
// 判据：ExecuteError.LastKind == circuit_open。该 kind 由执行器层从**尝试
// 摘要**判定（见 executors/executor_circuit_open.go）⇒ 混合场景（部分候选
// 被熔断、部分上游 500）不会误命中。
func providerCircuitOpenRejection(ee *executors.ExecuteError, clientModel string) (providerCircuitOpenSurface, bool) {
	if ee == nil || ee.LastKind != errorsx.KindCircuitOpen {
		return providerCircuitOpenSurface{}, false
	}
	retryAfter := ee.RetryAfterSeconds
	if retryAfter <= 0 {
		// 算不出具体冷却时长（熔断器里没有这个 key / 冷却截止是零值）
		// ⇒ 给一个保守的缺省值，而不是让客户端立刻重试再撞一次。
		retryAfter = providerCircuitOpenDefaultRetryAfter
	}
	return providerCircuitOpenSurface{
		Status:  http.StatusServiceUnavailable,
		Message: fmt.Sprintf("All %d candidate(s) for model '%s' are temporarily circuit-open; retry after backoff.", ee.Tried, clientModel),
		Code:    ProviderCircuitOpenCode,
		// 至少 1 秒：Retry-After: 0 会让客户端立即重试再撞一次熔断。
		RetryAfter: max(retryAfter, 1),
	}, true
}

// setCircuitOpenHeaders 写出熔断耗尽的两个响应头。
//
// ★ Retry-After 是**契约的一部分**，不是装饰：调用方拿到它才知道该等多久
// 再重试；没有它就只能盲退避（要么太早重试再撞一次熔断，要么等太久）。
func setCircuitOpenHeaders(w http.ResponseWriter, retryAfter int) {
	w.Header().Set("X-Gateway-Last-Kind", string(errorsx.KindCircuitOpen))
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
}
