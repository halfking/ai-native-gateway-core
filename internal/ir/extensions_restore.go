package ir

import (
	"bytes"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
)

// paramregEnabled 报告是否启用注册表驱动的 Extensions 还原。
//
// PARAMREG_ENABLED=false 退回旧行为（仅同协议还原），用于紧急回退。
// 默认启用。
var paramregEnabled = sync.OnceValue(func() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv("PARAMREG_ENABLED")), "false")
})

// restoreExtensions 把 req.Extensions 按参数注册表策略还原进 out。
//
// 这个函数替换了此前散落在 serialize_openai.go:179 / serialize_anthropic.go:196 /
// serialize_responses.go:166 的三段重复代码，并补上了 serialize_gemini.go 完全
// 缺失的还原逻辑。
//
// 旧实现的问题（docs/参数全量兼容/01-审计基线与研究结论.md 1.1-1.3）：
//
//	if req.SourceProtocol == "" || req.SourceProtocol == ProtocolOpenAIChat {
//	    // 还原
//	}
//
// 该门禁把"来源协议 == 目标协议"当成"字段安全"的充分条件。但网关的核心场景
// 恰恰是跨协议转发（Claude Code anthropic → DeepSeek openai-chat），此时门禁
// 永假，客户端的厂商私有参数 100% 丢失。
//
// 新实现按**字段**判定而非按整包判定：
//   - 未登记字段（未知/前向兼容）→ 无条件还原，含跨协议
//   - 方言私有字段 → 仅目标方言认识时还原，否则裁剪并上报 loss
//   - 目标方言硬拒绝的字段 → 裁剪（优先级最高，防 400）
//
// 这样既让 Claude Code 的 CLAUDE_CODE_EXTRA_BODY 任意字段能穿过网关，
// 又不会把 Anthropic 私有的 context_management 泄漏进 Mistral 的
// additionalProperties:false body。
//
// targetProtocol 是目标线格式协议常量（Protocol* 之一）。
// 已存在于 out 的键不会被覆盖 —— IR 序列化器的输出优先于 Extensions。
func restoreExtensions(out map[string]any, req *InternalRequest, targetProtocol string) {
	if req == nil || len(req.Extensions) == 0 {
		return
	}
	src := paramreg.DialectForProtocol(req.SourceProtocol)
	dst := resolveTargetDialect(req, targetProtocol)
	RestoreExtensionsWithDialects(out, req, src, dst, targetProtocol)
}

// RestoreExtensionsWithDialects 是 restoreExtensions 的显式方言变体（导出）。
//
// 2026-11-11 审计第二轮（批 3 · 双实现统一）：transport 层
// （domains/transformation/ir_converter.go）的第二遍还原曾有一份**独立的
// 简化拷贝**——缺 NativePassthrough、reasoning/thinking 特例、确定性快照
// 三阶段，靠注释约定「与 ir 层同形」维持一致；r1021（thinking 400 复发）
// 正是这种双实现漂移的既遂案例。修法 = 阶段逻辑单点化到本函数，transport
// 层委托（方言解析仍由调用方承担：transport 有 ctx.UpstreamCatalogCode
// 兜底这一 ir 层没有的输入）。
//
// src/dst 语义与 restoreExtensions 的自行解析完全一致；其余门禁
// （NativePassthrough / PARAMREG_ENABLED 回退）与阶段 1/2a/2b/3 逐字同源。
func RestoreExtensionsWithDialects(out map[string]any, req *InternalRequest, src, dst paramreg.Dialect, targetProtocol string) {
	if req == nil || len(req.Extensions) == 0 {
		return
	}

	// 原厂直通（2026-10-28）：NativePassthrough 置位时 Extensions 全量
	// 原样还原，仅让位于 IR 序列化器已输出的键（结构性字段）。既不走
	// paramreg 的方言裁剪，也不做 unknown-relay 的 thinking 翻译 ——
	// 原厂对原厂，客户端自己的参数表达即事实源；上游拒绝的回退在执行器
	// （reqprobe 4xx 剥参重试），不在序列化层。
	if req.NativePassthrough {
		for key, val := range req.Extensions {
			if _, exists := out[key]; exists {
				continue
			}
			out[key] = preservedRawValue(val)
		}
		return
	}

	if !paramregEnabled() {
		// 回退路径：旧的同协议门禁行为。
		if req.SourceProtocol != "" && req.SourceProtocol != targetProtocol {
			return
		}
		for key, val := range req.Extensions {
			if _, exists := out[key]; exists {
				continue
			}
			out[key] = preservedRawValue(val)
		}
		return
	}

	restoreExtensionsStages(out, req, src, dst, targetProtocol)
}

// restoreExtensionsStages 承载 paramreg 启用路径的全部阶段
// （确定性快照 / reasoning·thinking 特例 / 排序通用还原）。
// 由 restoreExtensions（自行解析方言）与 RestoreExtensionsWithDialects
// （显式方言）共用 —— 单一语义源。
func restoreExtensionsStages(out map[string]any, req *InternalRequest, src, dst paramreg.Dialect, targetProtocol string) {

	// ══ 阶段 1：确定性快照（2026-11-01 12h 审计 P1-1）══════════════════════
	//
	// Extensions 是 Go map ⇒ 迭代顺序随机（Go 规范）。而下面这几个键会互相
	// 影响「出站 body 是否已带 reasoning_effort / reasoning」：
	//
	//   · Extensions["reasoning_effort"] → 通用还原会写 out["reasoning_effort"]
	//   · Extensions["thinking"]         → Unknown 特例 / Grok·Responses 通用
	//                                      翻译会写 out["reasoning_effort"]
	//                                      或 out["reasoning"]
	//   · Extensions["reasoning"]        → 下面的 reasoning 特例会写
	//                                      out["reasoning_effort"] 或 out["reasoning"]
	//
	// 旧实现把「出站是否已有下划线」这个探测放在 map 循环体内 ⇒ 同一请求
	// 出站 body 随迭代序变化：HEAD 实测 300 次出站出现 245/55 混合（基线
	// 300/300 恒定 ⇒ 本窗口引入的确定性回归），且 OpenRouter「对象内 effort
	// 与 reasoning_effort 同发」的冲突 400 只被消掉 78~80%（实测 3 次
	// 复跑 80.2% / 79.3% / 78.5%）。
	//
	// 修法（三处，缺一不可）：
	//   ① 探测全部提到循环前 —— 读快照，不再读可变的 out；
	//   ② 特例键固定排序（reasoning 在 thinking 之前）；
	//   ③ 通用路径按键名排序迭代 —— 两个来源写同一个出站键时也有确定胜者。
	_, outHasUnderscore := out["reasoning_effort"]
	_, outHasReasoning := out["reasoning"]
	_, extHasUnderscore := req.Extensions["reasoning_effort"]
	_, extHasReasoning := req.Extensions["reasoning"]
	// 对象内 effort（客户端显式表达的思考强度；对象是唯一载体时由它承载）。
	objectEffort := reasoningObjectEffort(req.Extensions["reasoning"])
	// 客户端**显式**表达的下划线 effort：IR 渲染的 或 Extensions 自带的。
	// （Extensions 自带的那份由通用路径写回 out，故也算「出站已带」。）
	explicitUnderscore := outHasUnderscore || extHasUnderscore

	// thinking 翻译轮（2026-10-13）：unknown-relay（DialectUnknown）上
	// thinking 的 RejectedByUnknown 裁剪改为**按目标协议翻译**——这里独有
	// 协议信息（Decide/Translate 只有方言粒度）。数据矩阵（Vapeur 全模型
	// 实测，docs/audit/2026-10-13-Vapeur全量测试轮.md §七）：
	//   · responses 协议 → reasoning{effort}（除 claude/gemini 后端外全 200）
	//   · openai-chat 协议 → reasoning_effort（全模型 200）
	// gpt/grok 严格后端不再吃「Unknown parameter: 'thinking'」的 400 往返
	// （reqprobe 剥参兜底从「每次两跳」变「零跳」），宽容后端思考意图保留。
	// IR 已渲染 effort（reasoning/reasoning_effort 在 out）时不覆盖——
	// 客户端显式 effort 优先于 thinking 值换算。
	//
	// ★ 2026-11-01（12h 审计 P1-1）：守卫从「探测可变的 out」改为读阶段 1
	//   快照，并把「Extensions 自带下划线」与「对象内 effort」两个显式来源
	//   纳入 —— 客户端显式 effort 优先于 thinking 换算（上一段的既有语义），
	//   且判定不再随 map 迭代序变化。
	translateUnknownThinking := func(val json.RawMessage) (string, json.RawMessage, bool) {
		if dst != paramreg.DialectUnknown {
			return "", nil, false
		}
		switch targetProtocol {
		case ProtocolOpenAIResponses:
			if outHasReasoning || extHasReasoning {
				return "", nil, false
			}
			return paramreg.TranslateThinkingForProtocol(val, "responses")
		case ProtocolOpenAIChat:
			if explicitUnderscore || objectEffort != "" {
				return "", nil, false
			}
			return paramreg.TranslateThinkingForProtocol(val, "openai-chat")
		}
		return "", nil, false
	}

	// ══ 阶段 2a：reasoning 特例（固定排在 thinking 特例之前）════════════════
	//
	// 参数兼容静态化轮（2026-11-01）：chat 线出站的顶层 reasoning 对象
	// 裁决。四腿实机矩阵（docs/audit/2026-11-01-参数兼容静态化轮-
	// 跨端点reasoning实证.md）：nvidia 硬 400（registry RejectedBy）/
	// zhipu·Ark 静默忽略（意图会无声丢失，翻译成 reasoning_effort）/
	// OpenRouter 原生生效（保留对象；双表达同发时对象内 effort 让位，
	// 旋钮保留——冲突值 400 类被静态消除）。Decide 看不到出站 body
	// 状态（out 是否已有 reasoning_effort）也没有方言翻译粒度，
	// 与 thinking/stream_options 特例同款模式收在这里。
	//
	// 排在 thinking 特例之前的理由：客户端显式 effort（对象内 effort）
	// 优先于 thinking 换算（上一段的既有语义）——否则 thinking 换算出的
	// 值会顶掉客户端显式表达的强度。
	reasoningHandled := false
	if extHasReasoning && targetProtocol == ProtocolOpenAIChat {
		if _, exists := out["reasoning"]; exists {
			// IR 序列化器已经输出该键 ⇒ 不覆盖（与旧循环首行同语义）。
			reasoningHandled = true
		} else {
			outKey, outVal, action, lossReason := paramreg.ReasoningObjectChatDecision(
				req.Extensions["reasoning"], explicitUnderscore, dst)
			switch action {
			case paramreg.ActionSkip:
				reasoningHandled = true
			case paramreg.ActionDrop:
				ReportProtocolLoss("", "reasoning", req.SourceProtocol, targetProtocol, lossReason,
					"top-level reasoning object dropped by target-dialect policy on the openai-chat wire",
					map[string]any{
						"source_dialect": string(src),
						"target_dialect": string(dst),
					})
				reasoningHandled = true
			case paramreg.ActionRestore:
				if _, exists := out[outKey]; !exists {
					out[outKey] = preservedRawValue(outVal)
				}
				reasoningHandled = true
			}
		}
	}

	// ══ 阶段 2b：thinking 特例 ═══════════════════════════════════════════════
	thinkingHandled := false
	if rawThinking, ok := req.Extensions["thinking"]; ok {
		if _, exists := out["thinking"]; exists {
			// IR 序列化器已经输出该键 ⇒ 不覆盖（与旧循环首行同语义）。
			thinkingHandled = true
		} else if outKey, outVal, ok := translateUnknownThinking(rawThinking); ok {
			out[outKey] = preservedRawValue(outVal)
			thinkingHandled = true
		}
	}

	// ══ 阶段 3：通用还原 ═══════════════════════════════════════════════════
	//
	// ★ 键名排序迭代：两个不同的 Extensions 键可能映射到同一个出站键
	//   （例：thinking→reasoning_effort 与 reasoning_effort→reasoning_effort）
	//   ⇒ 旧实现「先到先得」由随机迭代序决定 ⇒ 同样不可复现。排序后胜者
	//   确定（键名字典序），且与「显式 > 换算」的语义一致
	//   （reasoning_effort 先于 thinking）。
	extKeys := make([]string, 0, len(req.Extensions))
	for key := range req.Extensions {
		extKeys = append(extKeys, key)
	}
	sort.Strings(extKeys)

	for _, key := range extKeys {
		val := req.Extensions[key]
		if _, exists := out[key]; exists {
			// IR 序列化器已经输出了这个键，不覆盖。
			continue
		}
		if (key == "reasoning" && reasoningHandled) || (key == "thinking" && thinkingHandled) {
			// 阶段 2 已按固定顺序裁决过 ⇒ 不再走通用路径。
			continue
		}

		// r1026（2026-10-13）：stream_options 是 openai-chat 线格式专属参数，
		// Responses / Anthropic / Gemini 线格式均无此字段。chat 入向的
		// stream_options.include_usage（AI-SDK 流式恒发）经 KindPortable 透传
		// 进 openai-responses 出站体时，严格后端整流 400（vapeur 中转
		// grok-4.6 实测："Unknown parameter: 'stream_options.include_usage'"，
		// 单候选即整请求失败）。按**目标线格式**裁剪而非给注册表挂
		// RejectedByUnknown：后者会连带 chat 形态的 unknown 中转（联界/
		// 智码等本地主力）在 executor 再注入前丢掉客户端显式
		// include_usage=false 的语义。Responses 流的 usage 恒随
		// response.completed 返回，Anthropic/Gemini 各有原生 usage 事件，
		// 裁剪零语义损失。
		if key == "stream_options" {
			switch targetProtocol {
			case ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerate:
				ReportProtocolLoss("", key, req.SourceProtocol, targetProtocol,
					"wire_format_scoped_field",
					"stream_options dropped: parameter exists only on the openai-chat wire format",
					map[string]any{
						"source_dialect": string(src),
						"target_dialect": string(dst),
					})
				continue
			}
		}

		outKey, outVal, action, spec := paramreg.Apply(key, val, src, dst)

		switch action {
		case paramreg.ActionRestore, paramreg.ActionTranslate:
			if outKey == "" {
				continue
			}
			if _, exists := out[outKey]; exists {
				continue
			}
			out[outKey] = preservedRawValue(outVal)

		case paramreg.ActionDrop:
			reason := "dialect_scoped_field"
			note := ""
			if spec != nil {
				note = spec.Note
				if spec.RejectedBy != nil {
					for _, bad := range spec.RejectedBy {
						if bad == dst {
							reason = "rejected_by_target_dialect"
							break
						}
					}
				}
			}
			ReportProtocolLoss("", key, req.SourceProtocol, targetProtocol, reason,
				"extension field dropped by param registry policy",
				map[string]any{
					"source_dialect": string(src),
					"target_dialect": string(dst),
					"note":           note,
					// Slice 2（2026-11-12）语义化投影：paramreg 策略丢弃是
					// **按设计的方言管辖**（厂商分歧 ≠ 语义损失），处置标记
					// 让 census 能把它与真丢失分离。去重键不含 metadata
					// ⇒ 富化不改变去重行为。
					"disposition": "by_design_dialect_scope",
				})

		case paramreg.ActionSkip:
			// IR 已处理该字段，序列化器负责输出。不是丢失，不上报。
		}
	}
}

// reasoningObjectEffort 取出顶层 reasoning 对象里的 effort 档位。
//
// 用途（2026-11-01 12h 审计 P1-1）：判定「客户端是否显式表达了思考强度」。
// 非对象 / 无 effort / effort 非字符串 / 空串 ⇒ 返回 ""（视作「对象不携带
// effort」）——调用方据此决定「对象内 effort 是否优先于 thinking 换算」。
func reasoningObjectEffort(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	effortRaw, ok := obj["effort"]
	if !ok {
		return ""
	}
	var effort string
	if err := json.Unmarshal(effortRaw, &effort); err != nil {
		return ""
	}
	return strings.TrimSpace(effort)
}

// resolveTargetDialect 推导目标方言。
//
// TargetProvider（来自 provider.Candidate.CatalogCode）比协议粒度更细 ——
// DeepSeek 和 GLM 都说 openai-chat，但私有参数互不相认。优先用它；
// TargetProvider 为空时（如 apiclaude 等未挂 catalog 的中转）回退到目标协议。
//
// 2026-10-13（交叉格式矩阵 A1.6）：catalog 非空但未登记的供应商
// （suyun/apigpt/apinext 等）此前同样走协议回退，DialectOpenAIChat 成为
// 目标方言后，别家厂商的方言私有字段（grok 的 search_parameters、
// DeepSeek 的 prefix）按「目标不认识」被裁剪 —— 这违背了
// DialectForCatalogCode 对未登记厂商「不该因为没登记就丢参数」的
// fail-open 承诺。未登记就是没有立场替对方做兼容判断：维持
// DialectUnknown（knownBy=true / rejects=false，最宽松），错误面交给
// reqprobe 的 4xx 学习兜底。
//
// 注意 TargetProvider == ""（空串）**不走**上面的 fail-open：它同时承担
// 「路由前序列化点」语义（会话恢复/预渲染，见
// serialize_openai_minimax_dialect_test.go 的 pre-routing 契约），那里
// fail-closed 是对的。路由已定但 catalog_code 为空串的中转（联界/智码
// 等本地主力，2026-10-13 R2.D1 实测盲区）由 executor 盖
// paramreg.CatalogCodeUnknownRelay 哨兵区分——非空且必然未登记，
// 自然落到 DialectUnknown fail-open。
func resolveTargetDialect(req *InternalRequest, targetProtocol string) paramreg.Dialect {
	if req.TargetProvider != "" {
		if d := paramreg.DialectForCatalogCode(req.TargetProvider); d != paramreg.DialectUnknown {
			return d
		}
		return paramreg.DialectUnknown
	}
	return paramreg.DialectForProtocol(targetProtocol)
}

// preservedRawValue keeps an extension's JSON bytes intact.
//
// It used to be `json.Unmarshal(val, &v)` into `any`, which routes every JSON
// number through float64: an integer one above 2^53 came back rewritten
// (9007199254740993 → 9007199254740992) and was emitted to the wire that way.
// The document codec already preserved the same bytes via json.RawMessage, so
// the loss was introduced purely by this convenience decode.
//
// json.RawMessage is itself a []byte alias, so assigning it satisfies the
// `any` element type and encoding/json emits the original bytes verbatim.
func preservedRawValue(val json.RawMessage) any {
	trimmed := bytes.TrimSpace(val)
	if len(trimmed) == 0 {
		return nil
	}
	return json.RawMessage(trimmed)
}
