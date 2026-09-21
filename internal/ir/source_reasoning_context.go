package ir

import (
	"context"
)

// SourceReasoning carries an inbound request's protocol identity and reasoning
// intent across the handler → executor boundary for protocols that must
// serialize to the OpenAI wire form BEFORE the target provider is known.
//
// Background (R43 §五#1 / R45 专项, 2026-09-19): the Gemini handler converts
// Gemini bodies to OpenAI Chat form at step-6 (handler_gemini.go), strictly
// before routing — so req.TargetProvider is necessarily empty there and
// applyThinkingToOpenAIChat cannot render the budget-shaped Reasoning intent
// (parse_gemini maps thinkingConfig.thinkingBudget into Reasoning{Type:
// "enabled", BudgetTokens}). The serialized synthetic body therefore reaches
// the executor with the intent already gone; the executor's re-parse
// (ParseOpenAI) can never recover it even though it knows the target dialect
// at that point.
//
// Design decision (二选一定稿，方案一): carry the intent through the request
// context and restore it at the executor's serialization point, where
// TargetProvider is known. The alternative — deferring step-6 serialization
// until after routing — would require the whole ChatHandler pipeline
// (request logging, sticky sessions, streamretry body snapshots) to accept an
// IR instead of a body, an unbounded change surface. Context values survive
// the streamretry wrapper (it only derives via WithValue/WithContext chains),
// so retries keep the intent.
//
// Only the three fields the serializer needs are carried — not the whole
// InternalRequest — to keep the merge surface auditable.
type SourceReasoning struct {
	// Thinking is the Anthropic-inbound thinking config (ir.Thinking).
	// Currently only stashed by handlers that lose it pre-routing; the
	// Anthropic /v1/messages path re-parses its original body at the
	// executor, so it does not need this carrier.
	Thinking *ThinkingConfig

	// Reasoning is the request-level reasoning config; the budget-shaped
	// form (Gemini thinkingConfig) is the one the OpenAI wire cannot
	// express natively.
	Reasoning *ReasoningConfig

	// SourceProtocol is the TRUE inbound protocol (e.g. ProtocolGeminiGenerate).
	// It must be restored alongside the intent: ParseOpenAI stamps
	// SourceProtocol=ProtocolOpenAIChat on the re-parsed IR, and
	// openAIReasoningIntent deliberately excludes OpenAI-protocol sources
	// from the budget fallback (double-expression guard). Without restoring
	// the protocol identity the merged intent would be rejected by that
	// same guard.
	SourceProtocol string
}

type sourceReasoningContextKey struct{}

// HasReasoningIntent reports whether the source carries any reasoning intent
// worth attaching. Handlers use this to skip the context mutation entirely on
// the (overwhelmingly common) no-thinking path.
func (s SourceReasoning) HasReasoningIntent() bool {
	return s.Thinking != nil || s.Reasoning != nil
}

// WithSourceReasoning returns a context carrying the inbound reasoning intent.
// Attach it to the *original* request before building any synthetic request —
// synthetic builders derive their context from the original (WithContext), so
// the value flows through.
func WithSourceReasoning(ctx context.Context, src SourceReasoning) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, sourceReasoningContextKey{}, src)
}

// SourceReasoningFromContext extracts a carried inbound reasoning intent.
func SourceReasoningFromContext(ctx context.Context) (SourceReasoning, bool) {
	if ctx == nil {
		return SourceReasoning{}, false
	}
	src, ok := ctx.Value(sourceReasoningContextKey{}).(SourceReasoning)
	return src, ok && src.HasReasoningIntent()
}

// RestoreSourceReasoning merges a context-carried inbound reasoning intent into
// a re-parsed IR right before target-dialect serialization. It is the executor
// half of the SourceReasoning carrier (see WithSourceReasoning).
//
// Idempotence / precedence rules:
//   - No carried intent (context value absent or empty) → req returned as-is.
//   - req already expresses reasoning (Thinking or Reasoning set, e.g. the
//     body carried a native field) → req returned as-is; the body's own
//     expression always wins over the carrier. In the Gemini-handler flow the
//     step-6 body never carries these (that loss is the very defect being
//     fixed), but the guard keeps the restore safe against future senders
//     that preserve intent natively. (R45 复核注记：该守卫只看 IR 结构字段；
//     ParseOpenAI 把顶层 dialect thinking 对象收进 Extensions 而非
//     ir.Thinking——若未来某发送方同时挂载体又在 body 自带 thinking 对象，
//     applyThinkingToOpenAIChat 先渲染载体意图且 restoreExtensions 不覆盖
//     已有键，载体将反超 body。当前生产不可达，登记为未来发送方陷阱。)
//   - SourceProtocol is only overwritten when it is an OpenAI form — i.e.
//     the ParseOpenAI stamp. A non-OpenAI SourceProtocol set by an upstream
//     parse path is left untouched.
//
// The restored SourceProtocol is what lets openAIReasoningIntent's
// double-expression guard accept the merged intent: the request genuinely
// originated from a non-OpenAI protocol whose intent has no native OpenAI
// Chat representation.
func RestoreSourceReasoning(req *InternalRequest, ctx context.Context) *InternalRequest {
	src, ok := SourceReasoningFromContext(ctx)
	if !ok || req == nil {
		return req
	}
	if req.Thinking != nil || req.Reasoning != nil {
		return req
	}
	req.Thinking = src.Thinking
	req.Reasoning = src.Reasoning
	switch req.SourceProtocol {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, "":
		req.SourceProtocol = src.SourceProtocol
	}
	return req
}

// anthropicMinBudget mirrors reasonnorm.AnthropicMinBudget (the hard floor for
// Anthropic budget_tokens). Declared locally to avoid exporting reasonnorm
// internals through this file; the canonical constant lives in reasonnorm.
const anthropicMinBudget = 1024

// ClampRestoredReasoningForAnthropic adapts a cross-protocol restored reasoning
// intent (R45: Gemini thinkingConfig carried via SourceReasoning) to Anthropic's
// budget constraints before SerializeAnthropic: budget ≥ 1024 and strictly
// less than max_tokens. A client that chose its budget under Gemini's rules
// (e.g. thinkingBudget=128, legal for Gemini 2.5 Pro) would otherwise produce
// an invalid Anthropic thinking object and a 400 upstream.
//
// Semantics mirror reasonnorm.renderAnthropic: floor at 1024; cap at
// max_tokens-1; if the cap cannot keep the budget above the floor, drop the
// thinking intent entirely (a sendable request beats an unsendable one).
// Type-only intents (disabled / enabled without budget) pass through
// untouched; nil req is a no-op.
func ClampRestoredReasoningForAnthropic(req *InternalRequest) *InternalRequest {
	if req == nil {
		return req
	}
	if b := req.Reasoning; b != nil && b.BudgetTokens != nil {
		budget := *b.BudgetTokens
		if budget > 0 {
			if budget < anthropicMinBudget {
				budget = anthropicMinBudget
			}
			if req.MaxTokens > 0 && budget >= req.MaxTokens {
				budget = req.MaxTokens - 1
			}
			if budget < anthropicMinBudget {
				req.Reasoning = nil
				return req
			}
			*b.BudgetTokens = budget
		}
	}
	if t := req.Thinking; t != nil && t.BudgetTokens > 0 {
		budget := t.BudgetTokens
		if budget < anthropicMinBudget {
			budget = anthropicMinBudget
		}
		if req.MaxTokens > 0 && budget >= req.MaxTokens {
			budget = req.MaxTokens - 1
		}
		if budget < anthropicMinBudget {
			req.Thinking = nil
			return req
		}
		t.BudgetTokens = budget
	}
	return req
}
