package executors

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// applyInlineValidation applies format validation to OpenAI request JSON.
// Used in legacy path where IR converter may not be initialized.
//
// 2026-07-12: Created to ensure tool_call_id_mismatch prevention works
// even when e.IR is nil (which is the default in production).
//
// 2026-07-18: take requestID so each removal logs are correlated to
// the gateway request. Caller must pass the actual value; no caller
// is currently wired (see executor_chat.go:legacy_no_ir note).
//
// 2026-09-18 (MiniMax thinking 事故): take targetCatalogCode so the
// serializer's paramreg dialect decision (KindTranslatable / KindDialectOnly,
// e.g. thinking enabled→adaptive for MiniMax) resolves to the real provider
// dialect instead of falling back to generic openai_chat.
//
// R45 (2026-09-19, Gemini thinking 专项): take ctx so the executor can restore
// a context-carried inbound reasoning intent (ir.SourceReasoning) — the
// Gemini handler's step-6 pre-routing serialization loses it from the body.
//
// 2026-10-28 (原厂直通轮): take nativePair so the legacy path (e.IR == nil)
// 也能保住原厂对（internal/nativepair）的客户端私有参数 —— 本函数把 body
// 整体经 IR 往返一次，Extensions 若不盖 NativePassthrough 会被 paramreg
// 方言裁剪（与 IR 路径同样的漏法）。生产默认启用 IR，但本函数是紧急回退
// 形态（LLM_GATEWAY_IR_CONVERTER=false）的必经之路 ⇒ 原生对的保证必须
// 在两条路径上都成立。
func applyInlineValidation(bodyBytes []byte, requestID string, targetCatalogCode string, ctx context.Context, nativePair bool) []byte {
	// Debug level: this runs on every request, so Info-level would flood
	// production logs. Operators can opt-in via LLM_GATEWAY_DEBUG_INLINE_VALIDATION=1
	// by switching back to slog.Info in a per-deploy override if needed.
	log := slog.With("request_id", requestID, "body_size", len(bodyBytes))
	log.Debug("applyInlineValidation: called")

	// Use IR converter to parse (handles string/array content correctly)
	irReq, err := ir.ParseOpenAI(bodyBytes)
	if err != nil {
		log.Warn("applyInlineValidation: ParseOpenAI failed, skipping validation",
			"error", err.Error(),
		)
		return bodyBytes
	}

	// Validate and fix
	irReq = ir.ValidateAndFixRequest(irReq, requestID)
	irReq.TargetProvider = targetCatalogCode
	// 原厂直通（2026-10-28）：Extensions 全量原样还原，不做方言裁剪。
	irReq.NativePassthrough = nativePair
	// R45: 恢复 context 携带的入向推理意图（Gemini handler step-6 丢失的
	// budget 形 intent）。无携带值时为无操作。
	irReq = ir.RestoreSourceReasoning(irReq, ctx)

	// Step 4 audit fix (2026-07-28): per-request scope so anomaly dedup is
	// bounded to this request rather than the process-global map.
	scope, cleanup := ir.WithIRScope(nil)
	defer cleanup()
	_ = scope

	// Serialize back to OpenAI format
	fixedBytes, err := ir.SerializeOpenAI(irReq)
	if err != nil {
		log.Warn("applyInlineValidation: SerializeOpenAI failed, using original",
			"error", err.Error(),
		)
		return bodyBytes
	}

	log.Debug("applyInlineValidation: validation applied",
		"original_size", len(bodyBytes),
		"fixed_size", len(fixedBytes),
	)
	return fixedBytes
}
