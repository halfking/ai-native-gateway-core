package ir

// reasoning_projection.go — Slice 2（2026-11-12，多厂商适应变化点 B）的
// ir 侧接线层：serializer 不再内联 reasoning 投影语义，改为按目标厂商族
// 取方言（internal/reasoningdialect，ForVendorFamily 注册表）投影，并把
// 按设计丢弃经 Loss 载体转发为 ir_protocol_loss（字段/原因/消息逐字保持，
// metadata 富化 family/disposition——anomaly 去重键不含 metadata，富化
// 不改变去重行为）。
//
// 各 serializer 的调用形态：
//   - serialize_anthropic  → FamilyAnthropic（Native+Derived）
//   - serialize_gemini     → FamilyGemini25（Native+Derived）
//   - serialize_ollama     → FamilyOllama（Derived 丢弃面）
//   - serialize_openai     → FamilyOpenAI（effort 原生发射）
//     / openai-wire thinking 家族（IntentProjector，按 TargetProvider 取）

import (
	"github.com/kaixuan/llm-gateway-go/internal/paramreg"
	"github.com/kaixuan/llm-gateway-go/internal/reasoningdialect"
)

// reportProjectionLosses 把方言投影的按设计丢弃逐条上报。Loss 四元组与
// ReportProtocolLoss 形参一一对应（fieldPath/reason/message 与历史内联
// 调用逐字一致）。
func reportProjectionLosses(req *InternalRequest, p reasoningdialect.Projection) {
	for _, l := range p.Losses {
		ReportProtocolLoss(irRequestID(req), l.FieldPath, l.SourceProtocol, l.TargetProtocol, l.Reason, l.Message, l.Metadata)
	}
}

// projectReasoningNative 把原生 thinking 配置交给指定家族方言投影
// （serialize_anthropic → FamilyAnthropic；serialize_gemini → FamilyGemini25）。
func projectReasoningNative(req *InternalRequest, family, typ string, budget int) reasoningdialect.Projection {
	np, ok := reasoningdialect.ForVendorFamily(family).(reasoningdialect.NativeProjector)
	if !ok {
		return reasoningdialect.Projection{}
	}
	return np.ProjectNative(reasoningdialect.NativeRequest{
		Type:           typ,
		Budget:         budget,
		Model:          req.Model,
		SourceProtocol: req.SourceProtocol,
	})
}

// applyReasoningWireToGC 把方言投影的 Wire 并进 generationConfig（键值
// 原样并入；发射即置 hasAny——与历史内联块「disabled-unsupported 记 loss
// 后省略、hasAny 不变」逐字一致）。
func applyReasoningWireToGC(gc map[string]any, proj reasoningdialect.Projection, hasAny *bool) {
	for k, v := range proj.Wire {
		gc[k] = v
		*hasAny = true
	}
}

// projectReasoningDerived 把跨协议 ReasoningConfig 交给指定家族方言投影。
// finalMaxTokens/clientSetMaxTokens 只被 anthropic 家族的 2026-10-01 审计
// 钳制消费；gemini 家族忽略。
func projectReasoningDerived(req *InternalRequest, r *ReasoningConfig, family string, finalMaxTokens int, clientSetMaxTokens bool) reasoningdialect.Projection {
	dp, ok := reasoningdialect.ForVendorFamily(family).(reasoningdialect.DerivedProjector)
	if !ok {
		return reasoningdialect.Projection{}
	}
	return dp.ProjectDerived(reasoningdialect.DerivedRequest{
		Type:               r.Type,
		BudgetTokens:       r.BudgetTokens,
		Effort:             r.Effort,
		MaxReasoningTokens: r.MaxReasoningTokens,
		Model:              req.Model,
		SourceProtocol:     req.SourceProtocol,
		FinalMaxTokens:     finalMaxTokens,
		ClientSetMaxTokens: clientSetMaxTokens,
	})
}

// openAIWireFamilyKey 把 paramreg 目标方言映射为 openai-wire thinking 家族
// 的注册表键。ok=false = 该方言无已验证的 openai-wire reasoning 投影
// （kimi / grok / mistral / vllm / ollama / unknown 保持 loss 上报路径——
// 2026-09-18 事故教训：方言来自 TargetProvider，绝不来自模型名）。
// 取代原 syntheticReasonCaps 的 switch（caps 构造随方言对象内聚）。
func openAIWireFamilyKey(dst paramreg.Dialect) (string, bool) {
	switch dst {
	case paramreg.DialectMiniMax:
		return reasoningdialect.FamilyMiniMax, true
	case paramreg.DialectDeepSeek:
		return reasoningdialect.FamilyDeepSeek, true
	case paramreg.DialectGLM:
		return reasoningdialect.FamilyGLM, true
	case paramreg.DialectArk:
		return reasoningdialect.FamilyArk, true
	case paramreg.DialectQwen:
		return reasoningdialect.FamilyQwen, true
	}
	return "", false
}
