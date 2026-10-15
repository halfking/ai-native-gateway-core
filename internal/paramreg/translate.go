package paramreg

import (
	"encoding/json"

	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

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
// 覆盖范围（R43 2026-09-18 更新）：请求级 thinking 的跨协议表达已由同日 P5
// 的 ir.applyThinkingToOpenAIChat 接管——Anthropic 入向（ir.Thinking）与
// Gemini 入向（ir.Reasoning budget 形）在 OpenAI 形态上游按 TargetProvider
// 方言出向；本函数保留为 OpenAI 协议入向（parse_openai 把 thinking 放进
// Extensions 的路径）出向前的语义改写层，两者通过 extensions_restore 的
// "已有键不覆盖"守卫衔接，不重复表达。
//
// 后续如果某个方言改 contract 再加白名单，在此函数追加分支即可。
func translateThinking(value json.RawMessage, src, dst Dialect) (string, json.RawMessage, bool) {
	// 2026-10-13（thinking 翻译轮，数据矩阵见 docs/audit/2026-10-13-
	// Vapeur全量测试轮.md §七）：responses 与 grok 方言不收 thinking
	// 对象但收 effort 形态——Vapeur 全模型实测：responses 腿
	// reasoning{effort} 除 claude/gemini 后端（本就不走该腿）外全 200；
	// grok chat 腿 reasoning_effort 200 且 reasoning_tokens=672 证明思考
	// 真生效。r1021 的「裁剪」升级为「翻译」：思考意图不再静默丢失。
	if dst == DialectResponses {
		if effort, ok := thinkingToEffort(value); ok {
			out, err := json.Marshal(map[string]any{"effort": effort})
			if err == nil {
				return "reasoning", out, true
			}
		}
		return "", nil, false
	}
	if dst == DialectGrok {
		if effort, ok := thinkingToEffort(value); ok {
			return "reasoning_effort", json.RawMessage(`"` + effort + `"`), true
		}
		return "", nil, false
	}
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

// thinkingToEffort 把 anthropic 形态的 thinking 值换算成 effort 档位。
// 明确 disabled 返回 ok=false（不渲染——思考关）；budget→档位复用
// reasonnorm.BudgetToEffort 的 LiteLLM 反向映射口径，"max" 就近降
// "xhigh"（gpt 家族 effort 集无 max，r1014）。
func thinkingToEffort(value json.RawMessage) (string, bool) {
	var obj struct {
		Type         string `json:"type"`
		BudgetTokens int    `json:"budget_tokens"`
	}
	if err := json.Unmarshal(value, &obj); err != nil {
		// 非对象形态：保守按默认档（上游侧 paramguard 仍会钳制）。
		return "medium", true
	}
	switch obj.Type {
	case "disabled":
		return "", false
	case "enabled", "adaptive", "":
		// 继续换算档位。
	default:
		// 未知类型词：保守按默认档。
	}
	if obj.BudgetTokens <= 0 {
		return "medium", true
	}
	effort := reasonnorm.BudgetToEffort(obj.BudgetTokens)
	if effort == "none" || effort == "max" {
		return "xhigh", true
	}
	return effort, true
}

// TranslateThinkingForProtocol 是 extensions_restore 对 DialectUnknown
// 目标的协议感知入口（Decide 只有方言粒度，协议信息在 ir 层调用点）。
// protocol 取 "responses" / "openai-chat"；非思考意图（disabled 等）
// 返回 ok=false 由调用方走原有裁剪路径。
func TranslateThinkingForProtocol(value json.RawMessage, protocol string) (string, json.RawMessage, bool) {
	switch protocol {
	case "responses":
		if effort, ok := thinkingToEffort(value); ok {
			out, err := json.Marshal(map[string]any{"effort": effort})
			if err == nil {
				return "reasoning", out, true
			}
		}
		return "", nil, false
	case "openai-chat":
		if effort, ok := thinkingToEffort(value); ok {
			return "reasoning_effort", json.RawMessage(`"` + effort + `"`), true
		}
		return "", nil, false
	}
	return "", nil, false
}
