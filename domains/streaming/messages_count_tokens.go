package streaming

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // same historical exemption as messages.go
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
	"github.com/kaixuan/llm-gateway-go/ratelimit"
)

// messages_count_tokens.go (2026-09-21)
//
// POST /v1/messages/count_tokens — Anthropic Messages API 的 token 计数端点。
// Claude Code / claude-cli 在每个上下文管理周期都会探测该端点;此前网关无此
// 路由, mux 直接回 404 "404 page not found"(非 Anthropic 错误形状),严格的
// 客户端会当作硬错误中断会话, 宽容的客户端也丢失压缩(compaction)精度。
//
// 网关侧没有供应商 tokenizer, 这里返回与流式桥 message_start.usage.input_tokens
// 完全相同的启发式估算(executors.EstimateAnthropicInputTokens): 两个表面给出
// 一致的数字, 客户端的上下文管理才不会在流式/非流式之间漂移。估算偏高是安全
// 方向(提前压缩优于上下文溢出)。
//
// 认证与 /v1/messages 同语义: Bearer 优先、x-api-key 兜底(extractBearerToken
// 自 2026-06-26 起支持), keyVerifier 的静态精确匹配与 sk-* 透传验证同样生效。
//
// ★★ 2026-10-30（ingressguard P2 缺口收口轮）：补 RPM 限流。
// 218 号查出本端点「既无配额闸也无 RPM 限流」，并登记成 P2 缺口
// （internal/ingressguard/ingress_surface_test.go 的 registry：RateLimit=gateNone，
//  理由「单请求可读至 128MB 且无频率上限 ⇒ P2 缺口，待裁决是否补」）。
// ⇒ 本轮**补** RPM 闸，理由与 2026-10-29 的 Gemini :countTokens 轮同源：
//
//  1. ★ **本面消耗网关自身算力**：它要读整个 body（最多 128MB，handler.go:72 的
//     maxBodySize）再做一次 Anthropic 形状解析与 token 估算 ⇒ 网关的 CPU/内存
//     开销由调用方触发 ⇒ **有频率上限才是对的**。
//  2. ★ **不存在「已计一次」的那一跳**：与 summarize/transform 的「刻意不消耗
//     RPM」不同——那三个面的 LLM 步骤经环回 chat 面携带同一把 key，那一跳已计
//     一次 ⇒ 再计即双计；而本面**不产生任何上游调用** ⇒ 没有那一跳 ⇒ 按同路由
//     族其他动作的口径计一次，是唯一自洽的选择。
//  3. ★ **同级对照**：/v1/messages（messages.go:444）有 RPM 闸；本端点是它的
//     同族面 ⇒ 「同级有、自己没有」不是偶然。
//
// ★ 为什么用 529 而不是 429：本端点属 Anthropic 面，**同路由族的 /v1/messages
// 在被限流时回 529**（messages.go:462）⇒ 沿用同族口径，避免同一把 key 在
// /v1/messages 与 /v1/messages/count_tokens 上收到两种限流状态码。
//
// ★ 为什么 429 之外还要写限流响应头（writeRateLimitHeaders）：与 /v1/messages
// 同款 ⇒ 客户端能读到 Retry-After / 剩余额度，而不是只看到一个错误信封。
type CountTokensHandler struct {
	chatHandler *ChatHandler
}

func NewCountTokensHandler(ch *ChatHandler) *CountTokensHandler {
	return &CountTokensHandler{chatHandler: ch}
}

func (h *CountTokensHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	//nolint:errcheck // best-effort close
	defer r.Body.Close()

	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request", "Method not allowed")
		return
	}

	// keyInfo 供 RPM 闸使用（checkGatewayRateLimit 需要它算配额）。
	// ★ lite 模式（DB 校验器不可用）下它为 nil，而该闸对 nil keyInfo 是 Skipped
	//   （与 ChatHandler、ChatHandler 侧的同名闸同口径）。
	var keyInfo *authentication.KeyInfo
	if h.chatHandler != nil && h.chatHandler.keyVerifier != nil && h.chatHandler.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Missing API key")
			return
		}
		verified, verifyErr := h.chatHandler.keyVerifier.Verify(r.Context(), rawKey)
		if verifyErr != nil {
			if _, ok := verifyErr.(*authentication.InvalidKeyError); ok {
				writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or expired API key")
				return
			}
			slog.Warn("count_tokens: key verification RPC failed", "error", verifyErr)
			writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "Authentication service temporarily unavailable")
			return
		}
		keyInfo = verified
	}

	// ★ RPM 闸（2026-10-30 补上，见本文件头 §「补 RPM 限流」的完整理由）：
	// 与同路由族的 /v1/messages 同一把尺（checkGatewayRateLimit）。
	//
	// ★ 闸位放在【鉴权之后、读 body 之前】——这是它真正的意义所在：
	//   被限流的请求**不会**再去读那最多 128MB 的 body ⇒ 限流 thereby 限制了
	//   网关自己的算力与内存开销 ⇒ 若把闸放在读 body 之后，被拒的请求已经
	//   付掉了全部读取成本，限流就只剩了个「拒绝」语义、挡不住消耗。
	if h.chatHandler != nil {
		rlOutcome := checkGatewayRateLimit(r.Context(), keyInfo, h.chatHandler.rateLimiter, notifyRateLimitWait(w, false))
		if !rlOutcome.Skipped {
			writeRateLimitHeaders(w, rlOutcome)
			if rlOutcome.Blocked {
				recordGatewayRateLimitRejection(rlOutcome)
				ratelimit.MarkGatewaySharedKeyRateLimit(w)
				// 529 与同路由族的 /v1/messages 同口径（messages.go:462）。
				writeAnthropicError(w, 529, "rate_limit_error", "Rate limit exceeded. Please wait and retry.")
				return
			}
		}
	}

	// Claude Code 会带 anthropic-beta 头与 context_management 等未知字段;
	// 估算器对原始 body 做 Anthropic 形状解析, 解析失败时退回整体 len/3.5,
	// 因此这里不做任何字段校验, 任何 body 都能给出非零估计。
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBodySize)+1))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request", "Failed to read request body")
		return
	}
	if len(body) > maxBodySize {
		writeAnthropicError(w, http.StatusRequestEntityTooLarge, "invalid_request", "Request body too large")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck // HTTP write error non-recoverable
	w.Write([]byte(`{"input_tokens":` + strconv.Itoa(executors.EstimateAnthropicInputTokens(body)) + `}`))
}
