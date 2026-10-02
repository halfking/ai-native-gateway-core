package ir

import (
	"strings"

	"github.com/kaixuan/llm-gateway-go/internal/reasonnorm"
)

// reasoningDisabled resolves conflicting IR fields in this order: explicit
// Type, explicit budget, then the effort spelling. A present zero budget means
// off; a positive budget beats an effort such as "none" or "disabled".
func reasoningDisabled(r *ReasoningConfig) bool {
	if r == nil {
		return false
	}
	if r.Type != "" {
		return r.Type == "disabled"
	}
	if r.BudgetTokens != nil {
		return *r.BudgetTokens == 0
	}
	return reasonnorm.IsDisableEffort(r.Effort)
}

// modelHasFamilyPrefix matches a provider model family and its dated/preview
// variants. The route may retain a provider-qualified name in IR.Model.
func modelHasFamilyPrefix(model, family string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return model == family || strings.HasPrefix(model, family+"-")
}

// Gemini 2.5 Flash and Flash Lite are the confirmed thinkingBudget=0 families.
// Pro, Gemini 3, and unrecognised aliases must not receive that sentinel.
func geminiSupportsDisabledThinking(model string) bool {
	return modelHasFamilyPrefix(model, "gemini-2.5-flash")
}

// Claude Opus 5.5 always thinks and rejects thinking.type=disabled. This
// deliberately names only the confirmed family; other model capabilities
// need a provider-aware registry rather than a guessed universal rule.
func anthropicRejectsDisabledThinking(model string) bool {
	return modelHasFamilyPrefix(model, "claude-opus-5-5")
}

func reasoningDisableFieldPath(req *InternalRequest, nativeThinking bool) string {
	if nativeThinking {
		return "thinking.type"
	}
	if req.SourceProtocol == ProtocolOpenAIChat {
		return "reasoning_effort"
	}
	return "reasoning"
}
