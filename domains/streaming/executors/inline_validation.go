package executors

import (
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/internal/ir"
)

// applyInlineValidation applies format validation to OpenAI request JSON.
// Used in legacy path where IR converter may not be initialized.
//
// 2026-07-12: Created to ensure tool_call_id_mismatch prevention works
// even when e.IR is nil (which is the default in production).
func applyInlineValidation(bodyBytes []byte) []byte {
	// Debug level: this runs on every request, so Info-level would flood
	// production logs. Operators can opt-in via LLM_GATEWAY_DEBUG_INLINE_VALIDATION=1
	// by switching back to slog.Info in a per-deploy override if needed.
	slog.Debug("applyInlineValidation: called", "body_size", len(bodyBytes))

	// Use IR converter to parse (handles string/array content correctly)
	irReq, err := ir.ParseOpenAI(bodyBytes)
	if err != nil {
		slog.Warn("applyInlineValidation: ParseOpenAI failed, skipping validation",
			"error", err.Error(),
		)
		return bodyBytes
	}

	// Validate and fix
	irReq = ir.ValidateAndFixRequest(irReq)

	// Serialize back to OpenAI format
	fixedBytes, err := ir.SerializeOpenAI(irReq)
	if err != nil {
		slog.Warn("applyInlineValidation: SerializeOpenAI failed, using original",
			"error", err.Error(),
		)
		return bodyBytes
	}

	slog.Debug("applyInlineValidation: validation applied",
		"original_size", len(bodyBytes),
		"fixed_size", len(fixedBytes),
	)
	return fixedBytes
}
