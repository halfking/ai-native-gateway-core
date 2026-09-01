package streaming

// 2026-09-01 (audit AUDIT_CONTEXT_COMPRESSION_AND_STREAMING_20260901 §三 3.3):
// 用户需求：会话超 1M token → 主动压缩到 60%（不依赖供应商 4xx 触发）。
// 实现：promptBudgetExceeded 之前插入一层 preflight。
//
//   - ≤ softCompressThreshold (1M):       原行为不变
//   - (softCompressThreshold, hardCompressCeiling]: 尝试 60% 预压缩
//     - 成功（输出 ≤ budget）：继续走原路径
//     - 失败：交回 promptBudgetExceeded 拒绝（413）
//   - > hardCompressCeiling (2M):          直接交给 promptBudgetExceeded 拒绝（413）
//
// 关键不变量：
//   - 1M 默认行为不变（仍是 413 兜底）
//   - 复用 transformation.CompressMessagesAggressively / CompressAnthropicMessagesAggressively
//     与 4xx recovery 共用同一压缩器，避免重复实现
//   - 压缩前后日志保持与现有 promptBudgetExceeded 同级（logCtx.SetPreflightCompress + EmitFailure）
//   - 失败仍 fail-open：压缩异常视为 noop，由 promptBudgetExceeded 兜底

import (
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/transformation"
)

// softCompressThreshold 与 promptBudgetDefaultTokens 共用 1M（避免两处配置漂移）。
// hardCompressCeiling 定义 2M 软上限：超过即交回 413 prompt_too_large，不主动压缩。
const (
	softCompressThreshold = promptBudgetDefaultTokens       // 1048576
	hardCompressCeiling   = softCompressThreshold * 2       // 2097152（2M）
)

// preflightCompress 检查 body 是否落在 1M–2M 软触发区间，若是则尝试 60% 激进压缩。
//
// 入参 protocol 是 ClientProtocol 字符串："openai" 或 "anthropic-messages"。
// 返回：
//   - newBody: 压缩后的 body（未触发时返回原 body）
//   - applied: 是否真的触发了压缩（true = 实际生效，bodyBytes 应被替换）
//   - estTokens: 进入 preflight 时的估算 token 数（用于日志）
func preflightCompress(body []byte, protocol string) (newBody []byte, applied bool, estTokens int) {
	if len(body) == 0 {
		return body, false, 0
	}
	estTokens = estimateTokens(body)
	if estTokens <= softCompressThreshold {
		return body, false, estTokens
	}
	if estTokens > hardCompressCeiling {
		// > 2M：交给 promptBudgetExceeded 拒绝。不在 preflight 层处理。
		return body, false, estTokens
	}
	// 1M < estTokens ≤ 2M：尝试 60% 压缩。
	// 目标 ctxWindow 取 hardCompressCeiling (2M)，保证压缩后输出严格 ≤ 1M budget。
	// 这样语义自洽：60% × 2M = 1.2M ≤ budget（1M）边界有 ~17% 富余。
	ctxWindow := hardCompressCeiling
	var compressed []byte
	switch protocol {
	case "anthropic-messages":
		compressed = transformation.CompressAnthropicMessagesAggressively(body, ctxWindow)
	default:
		// openai / openai-responses / 其他：均按 OpenAI chat-style messages 数组处理。
		// CompressMessagesAggressively 对结构不匹配时返回原 body（never-worse 不变量）。
		compressed = transformation.CompressMessagesAggressively(body, ctxWindow)
	}
	// never-worse 守卫：若压缩器未缩（结构不匹配 / 已达下限），不要触发后续路径，
	// 直接交回 promptBudgetExceeded 拒绝。
	if len(compressed) >= len(body) {
		slog.Warn("preflight_compress: aggressive compress produced no shrinkage; deferring to 413",
			"protocol", protocol, "est_tokens", estTokens, "body_bytes", len(body))
		return body, false, estTokens
	}
	// 压缩后再次校验：估算 token 是否已经落到 ≤ budget 区间。
	// 若仍超 budget（极端情况），仍交回 413。
	postTokens := estimateTokens(compressed)
	if postTokens > softCompressThreshold {
		slog.Warn("preflight_compress: post-compress still over budget; deferring to 413",
			"protocol", protocol, "pre_tokens", estTokens, "post_tokens", postTokens,
			"pre_bytes", len(body), "post_bytes", len(compressed))
		return body, false, estTokens
	}
	slog.Info("preflight_compress: 1M-2M band compressed to <=60%",
		"protocol", protocol, "pre_tokens", estTokens, "post_tokens", postTokens,
		"pre_bytes", len(body), "post_bytes", len(compressed), "ctx_window", ctxWindow)
	return compressed, true, estTokens
}