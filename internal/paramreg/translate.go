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

// reasoningEffortTranslateDialects 是「reasoning_effort 下划线形态实证生效」
// 的 openai-chat 方言集合（2026-11-01 参数兼容静态化轮四腿实机矩阵，
// docs/audit/2026-11-01-参数兼容静态化轮-跨端点reasoning实证.md §二）：
//   - GLM（zhipu glm-5.3 coding 端点）：low/high 档带 3 倍可分辨，
//     minimal 200 生效（rt 201 vs low 带 132~489 vs high 带 814~2365）；
//   - Ark（volcengine glm-5-2）：minimal 确定性归零（rt=0 三连），
//     low/high 带 1071~1692；
//   - DeepSeek / Grok：registry reasoning_effort 行登记的枚举厂商
//     （值域由 paramguard fixReasoningEffort 按能力表出站钳制）；
//   - OpenAIChat（openai/azure 原厂）：reasoning_effort 是 o 系列的
//     规范拼写。
//
// 不在集合内的方言（MiniMax/Kimi/Qwen/Mistral/VLLM/Ollama）维持对象
// 原样透传——它们对对象的静默忽略是既有语义，翻译面若枚举未证实反而
// 可能引入新 400（今天 200 的请求不能变红）。
var reasoningEffortTranslateDialects = map[Dialect]bool{
	DialectGLM:        true,
	DialectArk:        true,
	DialectDeepSeek:   true,
	DialectGrok:       true,
	DialectOpenAIChat: true,
}

// ReasoningObjectChatDecision 裁决 openai-chat 出站 body 上的顶层
// reasoning 对象（extensions_restore 特例，2026-11-01 参数兼容静态化轮）。
//
// 背景：parse_openai 不消费对象形态（只认 reasoning_effort），chat 入向
// 的对象落 Extensions 后按 Decide（KindIRHandled、无 Dialects）原样还原
// ——于是 nvidia 吃 400、zhipu/Ark 把思考意图无声吞掉、OpenRouter 上
// 双表达冲突值直接 400。四腿实机矩阵给出三条静态事实：
//
//  1. reasoning_effort 下划线在全部四腿都生效（nvidia 集成层面接受、
//     zhipu/Ark 档带可分辨、OpenRouter 与对象形态等效）——「双表达同发
//     只保留下划线」四腿皆安全（任务问题 2 的结论，无供应商只认对象）；
//  2. 顶层对象在 chat 线**不是普遍被拒**：nvidia 硬 400（registry
//     RejectedBy 第一优先级裁决）、zhipu/Ark 宽容解析但静默忽略、
//     OpenRouter 原生接受且真生效（任务问题 1 的结论）；
//  3. 对象可以携带下划线表达不了的旋钮（OpenRouter 的 exclude /
//     max_tokens / enabled）⇒ 双表达同发时只剥对象内 effort、保留旋钮，
//     既消除 OpenRouter 冲突 400 又不丢语义。
//
// outHasUnderscoreEffort 报告出站 body 是否已有 IR 序列化器渲染的
// reasoning_effort（客户端下划线经 parse_openai 进 IR.Reasoning 后由
// serialize_openai 发射）。返回 (outKey, outVal, action, lossReason)：
//   - ActionSkip：静默跳过（双表达且对象 effort-only——语义已由下划线承载）
//   - ActionRestore：outKey/outVal 写入出站 body
//   - ActionDrop：上报 loss（lossReason 进 ReportProtocolLoss 的 reason）
func ReasoningObjectChatDecision(value json.RawMessage, outHasUnderscoreEffort bool, dst Dialect) (string, json.RawMessage, RestoreAction, string) {
	// 硬拒第一优先级：registry reasoning 行的 RejectedBy（nvidia NIM
	// 「Unsupported parameter(s): `reasoning`」生产台账 2026-10-28~31）。
	if spec := Lookup("reasoning"); spec != nil && spec.rejects(dst) {
		return "", nil, ActionDrop, "rejected_by_target_dialect"
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(value, &obj); err != nil {
		obj = nil
	}
	effortRaw, hasEffort := obj["effort"]

	// 双表达同发：对象内 effort 恒让位（事实 1）。剥掉 effort 后剩旋钮
	// 则保留旋钮对象（事实 3），剩空则整对象静态收口。
	if outHasUnderscoreEffort && hasEffort {
		delete(obj, "effort")
		if len(obj) == 0 {
			return "", nil, ActionSkip, ""
		}
		out, err := json.Marshal(obj)
		if err != nil {
			return "", nil, ActionSkip, ""
		}
		return "reasoning", out, ActionRestore, ""
	}

	// 无下划线：对象是 effort 意图的唯一载体。翻译方言集把 effort 提升为
	// reasoning_effort（zhipu/Ark 否则静默吞意图；值域由 paramguard 按目标
	// 能力钳制）；effort 表达不了的（旋钮-only / 非对象标量）按 loss 上报
	// ——这些方言集成员对对象形态没有可用语义，静默透传只是「假装成功」。
	// 保留方言集（OpenRouter 原生 / Unknown fail-open / 其余未证实方言）
	// 原样透传，维持今日语义零回归。
	if reasoningEffortTranslateDialects[dst] {
		if hasEffort {
			var effort string
			if err := json.Unmarshal(effortRaw, &effort); err == nil && effort != "" {
				if enc, err := json.Marshal(effort); err == nil {
					return "reasoning_effort", enc, ActionRestore, ""
				}
			}
			return "", nil, ActionDrop, "reasoning_object_effort_not_expressible"
		}
		return "", nil, ActionDrop, "reasoning_object_not_expressible"
	}
	return "reasoning", value, ActionRestore, ""
}
