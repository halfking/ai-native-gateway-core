package streaming

import (
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/kaixuan/llm-gateway-go/domains/authentication" //nolint:depguard // same historical exemption as messages.go
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors"
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

	if h.chatHandler != nil && h.chatHandler.keyVerifier != nil && h.chatHandler.keyVerifier.Enabled() {
		rawKey := extractBearerToken(r)
		if rawKey == "" {
			writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Missing API key")
			return
		}
		if _, verifyErr := h.chatHandler.keyVerifier.Verify(r.Context(), rawKey); verifyErr != nil {
			if _, ok := verifyErr.(*authentication.InvalidKeyError); ok {
				writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid or expired API key")
				return
			}
			slog.Warn("count_tokens: key verification RPC failed", "error", verifyErr)
			writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "Authentication service temporarily unavailable")
			return
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
