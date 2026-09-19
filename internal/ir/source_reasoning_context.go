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
//     that preserve intent natively.
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
