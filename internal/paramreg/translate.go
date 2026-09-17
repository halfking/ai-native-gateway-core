package paramreg

import "encoding/json"

// translateRandomSeed 在 Mistral 的 random_seed 与通用 seed 之间转换。
//
// Mistral 用 random_seed 表达其它厂商的 seed。值语义一致（整数），只是键名不同。
func translateRandomSeed(value json.RawMessage, src, dst Dialect) (string, json.RawMessage, bool) {
	if dst == DialectMistral {
		return "random_seed", value, true
	}
	// 出向非 Mistral：改回通用 seed 键名。
	//
	// 注意 seed 本身是 KindIRHandled（IR 有 Seed 字段），所以目标 body 里
	// 很可能已经有 seed 了。还原器只写目标不存在的键，不会覆盖 IR 的输出。
	return "seed", value, true
}

// translateThinking 在目标方言不允许的 thinking.type 值时改写为最近合法值。
//
// 2026-09-18 事故根因（MiniMax hzx-2 / minimax-prod-v2 强启即降级）：
//
//	MiniMax 上游自 2026-09 起将 thinking.type 收窄为 adaptive | disabled，
//	并对 enabled 硬报 400："invalid params, invalid thinking.type: \"enabled\"
//	(allowed: adaptive, disabled) (2013)"。
//
// 网关 IR（parse_openai）不识别 thinking，因此客户端的 Anthropic 风格
// thinking{type:enabled} 落到 req.Extensions。restoreExtensions 按
// KindIRHandled 决策返回 ActionRestore，把客户端值原样塞进 MiniMax-bound
// body → upstream 400 → credstate 开 breaker → 每次强制启用后立刻又被
// 同一个坏 body 打回。
//
// 修复：dst==MiniMax 时把 enabled → adaptive（最接近的合法值，
// 保留客户端的"想要推理"语义），并输出最小对象 {"type":"adaptive"}
// （budget_tokens 等伴生字段一并丢弃 —— MiniMax M3 无 budget 概念）；
// 其它方言原样透传（DeepSeek/GLM/Kimi 仍允许 enabled；Anthropic 仍接受
// enabled|adaptive|disabled）。
//
// 覆盖范围（2026-09-18 审计）：仅 OpenAI 协议入向（parse_openai 把
// thinking 放进 Extensions 的路径）。Anthropic 协议入向的 thinking 由
// parse_anthropic 消费进 ir.Thinking，serialize_openai 对其只上报 loss
// 不输出 —— 该路径在所有 OpenAI 形态上游都会静默丢 thinking，属 P5
// reasonnorm 统一处理范畴，不在本修复内。
//
// 后续如果某个方言改 contract 再加白名单，在此函数追加分支即可。
func translateThinking(value json.RawMessage, src, dst Dialect) (string, json.RawMessage, bool) {
	if dst != DialectMiniMax {
		return "thinking", value, true
	}
	var obj map[string]any
	if err := json.Unmarshal(value, &obj); err != nil {
		// 非对象（罕见；某些客户端会传字符串或其它标量）。
		// 原样写回 — 上游再校验，比网关主动猜更安全。
		return "thinking", value, true
	}
	if t, _ := obj["type"].(string); t == "enabled" {
		// 输出最小合法对象 {"type":"adaptive"}，丢弃 budget_tokens 等伴生字段：
		// MiniMax M3 的 thinking 无 budget/深度控制（官方 Responses API 的
		// effort 档位也只是 adaptive 的别名），改写后的对象里留下 MiniMax
		// 不认识的字段可能在严格校验下再次 400。
		out, err := json.Marshal(map[string]any{"type": "adaptive"})
		if err != nil {
			return "thinking", value, true
		}
		return "thinking", out, true
	}
	return "thinking", value, true
}
