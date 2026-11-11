package ir

// Gemini 专有语义参数在非 Gemini 目标上的丢失上报（2026-11-11 审计轮）。
//
// ── 缺口（本轮实测撞出）────────────────────────────────────────────
// parse_gemini 把 `safetySettings` / `cachedContent` **消费进 IR 的结构化
// 字段**（SafetySettings / CachedContent）⇒ 它们**不在 Extensions 里** ⇒
// 非 Gemini 的序列化器根本"看不见"它们 ⇒ 跨协议（Gemini → OpenAI/Anthropic/
// Responses/Ollama）时被**静默丢弃，且无任何 loss 事件**。
//
// 后果分级：safetySettings 是**安全语义参数**（内容安全阈值）。丢掉它 =
// 网关替客户端放宽了内容安全边界，且无人知晓。internal/ir/types.go 的
// P3 修复注释原话：「安全语义参数被静默丢失属于合规风险」。
//
// ── 为什么是「上报丢失」而不是「还原到目标 body」─────────────────────
//  ① paramreg.Decide 对 KindIRHandled + Dialects=[gemini] 的字段，在非
//     Gemini 目标上返回 **ActionDrop**（internal/paramreg/decide.go:140）——
//     仓库既有裁决就是「不许把 Gemini 专有字段塞进 OpenAI body」。
//  ② 盲还原会让上游对未知顶层键 400（OpenAI 严格校验 body 参数）。
//  ⇒ 正确处置 = **诚实披露丢失**，与 cache_control / documents / thinking
//    的既有处理同款（见 reportSerializeOpenAILosses 的同类上报）。
//
// ── 设计纪律 ────────────────────────────────────────────────────────
//  · 单一实现：四个非 Gemini 序列化器共用本函数 ⇒ 不复制粘贴 ⇒ 不会漂移。
//  · 同协议不报：src == Gemini 时由 serialize_gemini 自己输出，不算丢失。
//  · 参数用「具体字段名 + 目标协议」钉死，不落在标签词上（避免兜底分支
//    命中同一关键词而打不中）。
//  · SourceProtocol 为空（程序化构造的 IR）⇒ 不报：无法证明来源是 Gemini，
//    报了就是假阳性。

// reportGeminiOnlyLosses 上报「Gemini 专有语义参数在目标协议上无法表达」。
//
// targetProtocol 传目标协议的常量（openai-chat / anthropic-messages /
// openai-responses / ollama-chat）。
func reportGeminiOnlyLosses(req *InternalRequest, targetProtocol string) {
	if req == nil {
		return
	}
	src := req.SourceProtocol
	// 同协议：serialize_gemini 自己输出这两个字段 ⇒ 不是丢失。
	if targetProtocol == ProtocolGeminiGenerate {
		return
	}
	// 来源不可证（SourceProtocol 为空或非 Gemini）⇒ 不报：无法证明这批
	// 参数来自 Gemini 客户端，报了就是假阳性。
	if src != ProtocolGeminiGenerate {
		return
	}

	reqID := irRequestID(req)

	if len(req.SafetySettings) > 0 {
		ReportProtocolLoss(
			reqID,
			"safety_settings",
			ProtocolGeminiGenerate,
			targetProtocol,
			"loss",
			"Gemini safetySettings cannot be expressed on "+targetProtocol,
			map[string]any{"safety_settings_count": len(req.SafetySettings)},
		)
	}
	if req.CachedContent != "" {
		ReportProtocolLoss(
			reqID,
			"cached_content",
			ProtocolGeminiGenerate,
			targetProtocol,
			"loss",
			"Gemini cachedContent cannot be expressed on "+targetProtocol,
			nil,
		)
	}
}
