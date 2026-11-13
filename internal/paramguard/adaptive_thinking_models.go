package paramguard

import (
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// ─── 强制 adaptive thinking 的模型族（2026-11-11 claude-fable-5-2 事故轮）───
//
// 背景（211 生产实证，apiclaude 中继，直连实测）：
//
//	claude-fable-5-2 + thinking.type=enabled(+budget_tokens) ⇒ 400
//	  "claude-fable-5-2 requires adaptive thinking; omit thinking or use
//	   thinking.type=adaptive and output_config.effort"
//	claude-fable-5-2 + thinking.type=disabled              ⇒ 400（同文案）
//	claude-fable-5-2 + thinking.type=adaptive               ⇒ 200
//	claude-fable-5-2 + 无 thinking                            ⇒ 200
//
// 也就是说该模型**只接受 adaptive**（或干脆不带 thinking）。网关若把客户端的
// budget 形态（enabled）或关闭形态（disabled）原样发出，上游 100% 拒收 ⇒
// 全候选失败 ⇒ 客户端看到 503「有供应商却访问失败」。
//
// ★ 为什么这条规则必须落在 paramguard（而不是只改 IR 序列化）：
// 出站有两条腿都会带 thinking ——
//	① legacy 直传腿（客户端协议==anthropic-messages，即 claude-code /
//	   Anthropic SDK 客户端）：请求体不经 IR 序列化，直接透传；
//	② IR 序列化腿（跨协议 openai-chat/responses → anthropic）。
// paramguard 是两条腿**唯一共用的出站改写点**（executor_anthropic.go 的
// finalizeAnthropicRequestBody：IR 分支与 legacy 分支都调它）⇒ 在这里改
// 一处即覆盖两腿，且不碰任何字节契约（直传腿原本就允许语义级钳制）。
//
// ★ 为什么按**模型**门控而不是按方言：
// 约束是**模型版本**的属性，不是 anthropic-messages 方言的属性 ——
// 同一条方言下的 claude-fable-5 / claude-fable-5-1 对 enabled/disabled
// 全兼容（211 直连实测五种形态全 200）。按方言改会误伤旧模型 ⇒
// 门控判据必须是「出站 body 的 model 字段」，与既有先例
// isGrokReasoningModel（grok-3-mini / grok-4 同款模型门控）同源。
//
// ★ 改写语义选择：
//	enabled  → adaptive（保留客户端「想要推理」的意图，改成上游唯一接受的形态）
//	disabled → adaptive（该模型不支持关闭思考；adaptive 是最接近的合法值，
//	             且实测 200。省略整个 thinking 块会让客户端「关闭思考」的
//	             意图变成「开思考」，语义反转更糟）
//	adaptive → 不动（已是合法形态）
//	无 thinking → 不动（上游接受省略）
//
// 伴随的 budget_tokens 一并丢弃：adaptive 是 budgetless 形态（与
// reasonnorm.renderAnthropic 的 adaptive 分支同款），留着 budget_tokens
// 可能触发上游严格校验的二次 400（同 MiniMax enabled→adaptive 改写的处置）。

// adaptiveOnlyModels 是「只接受 thinking.type=adaptive」的模型名集合。
//
// ★ 判据是**精确名**而非前缀：`claude-fable-5` 与 `claude-fable-5-1`
// 对 enabled/disabled 全兼容（211 直连实测），只有 `claude-fable-5-2`
// 强制 adaptive ⇒ 用前缀 "claude-fable-5" 会把这两个旧模型一起误伤
// （它们此刻正在生产承载流量）。
//
// 2026-11-13（opus-5-5 事故轮）：apiclaude 把 fable-5-2 的同款语义
// 推广到了 `claude-opus-5-5` —— 同文案 "claude-opus-5-5 requires adaptive
// thinking; omit thinking or use thinking.type=adaptive and
// output_config.effort"（211 网关日志 02:23~02:34 +08 三起活体，ZCode chat
// 形态 thinking:{type:enabled,budget_tokens} ⇒ 400 ⇒ 全候选失败 ⇒ 客户端
// 「No available provider for model 'claude-opus-5-5'」）。注意翻转证据：
// 2026-11-11 时同中继同模型的 enabled 形态实测全 200（当时把 opus-5-5 钉在
// 阴性对照组）—— 这是**上游语义升级**，不是网关回归。精确名判据继续防误伤：
// `claude-opus-5` / `claude-opus-4-8` 等旧 opus 仍对 enabled 全兼容。
var adaptiveOnlyModels = map[string]bool{
	"claude-fable-5-2": true,
	"claude-opus-5-5":  true,
}

// adaptiveOnlyModelNames 供测试与文档引用（保序，便于人工核对）。
var adaptiveOnlyModelNames = []string{
	"claude-fable-5-2",
	"claude-opus-5-5",
}

// requiresAdaptiveThinking 报告该出站模型是否强制 adaptive thinking。
// model 取自出站 body 的 model 字段（已含 vendor/宿主前缀可能）。
func requiresAdaptiveThinking(model string) bool {
	if model == "" {
		return false
	}
	model = strings.ToLower(strings.TrimSpace(model))
	// 容忍 vendor/宿主前缀形态（如 "anthropic/claude-fable-5-2"）。
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	return adaptiveOnlyModels[model]
}

// fixAdaptiveOnlyThinking 把「强制 adaptive」的模型上的 thinking 形态改写为
// adaptive。返回 true 表示 body 被修改。
//
// 只在 Anthropic 方言下生效（thinking 是 Anthropic 的形态词汇）；其它方言
// 的 thinking 语义不同，不受此规则约束。
func fixAdaptiveOnlyThinking(obj map[string]json.RawMessage, dialect paramreg.Dialect, rep reporter) bool {
	if dialect != paramreg.DialectAnthropic {
		return false
	}
	rawModel, ok := stringField(obj, "model")
	if !ok || !requiresAdaptiveThinking(rawModel) {
		return false
	}
	thinkingRaw, ok := obj["thinking"]
	if !ok {
		return false
	}
	var th map[string]json.RawMessage
	if err := json.Unmarshal(thinkingRaw, &th); err != nil {
		return false
	}
	typ := ""
	if raw, ok := th["type"]; ok {
		_ = json.Unmarshal(raw, &typ)
	}
	// adaptive 已是合法形态；空/未知形态不动（未知形态原样交给上游判定，
	// 网关不猜 —— 猜错会把「上游自己会拒的形态」变成「网关改出来的形态」）。
	if typ == "adaptive" || (typ != "enabled" && typ != "disabled") {
		return false
	}
	out, err := json.Marshal(map[string]any{"type": "adaptive"})
	if err != nil {
		return false
	}
	slog.Warn("paramguard: rewrote thinking to adaptive for model that requires it",
		"model", rawModel, "original_type", typ)
	obj["thinking"] = out
	repField(rep, Report{
		Field: "thinking.type", Original: typ, Sent: "adaptive",
		Action: "normalize", Reason: "paramguard: model requires adaptive thinking",
	})
	return true
}
