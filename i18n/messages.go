package i18n

import (
	"context"
	"fmt"

	"github.com/nicksnyder/go-i18n/v2/i18n"
)

// Message keys. These intentionally match the "code" field emitted in data-plane
// error responses (see domains/streaming/handler.go writeErrorJSON* call sites),
// so a single code value is both the machine-readable token and the i18n lookup
// key. Keeping them aligned means no separate mapping table is required.
//
// New keys can be added freely; only the ones actually referenced need entries
// in locales/<tag>.json — T() falls back to "en" then to the key itself.
const (
	// Authentication.
	MsgMissingKey  = "missing_key"  // "Missing API key"
	MsgInvalidKey  = "invalid_key"  // "Invalid or expired API key"
	MsgMissingAuth = "missing_auth" // "Missing or malformed Authorization header"

	// Rate limiting & quota.
	MsgRateLimitExceeded   = "rate_limit_exceeded"  // "Rate limit exceeded"
	MsgBudgetExhausted     = "budget_exhausted"     // "Budget exhausted. Contact admin to top up."
	MsgInsufficientCredits = "insufficient_credits" // "Insufficient credits..."
	// 2026-11-21（W1-Q1，迁移 1000）：租户月度预算 hard 模式达限。
	MsgTenantBudgetExceeded = "tenant_budget_exceeded" // "Monthly budget for this tenant has been reached. Requests are blocked in hard enforcement mode."

	// Session errors.
	MsgSessionForbidden        = "SESSION_FORBIDDEN"         // "session not owned by this API key"
	MsgSessionAssignFailed     = "SESSION_ASSIGN_FAILED"     // "failed to assign session id"
	MsgSessionStoreUnavailable = "SESSION_STORE_UNAVAILABLE" // "session store unavailable; retry later"

	// Security policy.
	MsgBlocked = "blocked" // "Request blocked by security policy"

	// Content moderation.
	MsgContentFilter     = "content_filter"      // "Your request was rejected by the content moderation system (reason: {{.Reason}}). Please rephrase your input and retry."
	MsgContentFilterHint = "content_filter_hint" // "Check your input for sensitive or prohibited content and try again."

	// Model / provider selection.
	MsgNoCandidate        = "no_candidate"        // "No available provider for model '{{.Model}}'"
	MsgInvalidModel       = "invalid_model"       // "Model '{{.Model}}' is not supported by this gateway"
	MsgUnsupportedFeature = "unsupported_feature" // "The selected model does not support this request format or modality."
	// MsgModalityMismatch: the requested model IS routable, but it is
	// registered as a modality that cannot serve this request — e.g. the
	// request carries screenshots and the model is registered as text-only.
	//
	// ★ Why this needs its own message instead of reusing MsgNoCandidate:
	// "No available provider" describes the gateway's state ("nothing to
	// route to right now"), so the client's rational reaction is to retry.
	// But here the model is perfectly available and it is the REQUEST that
	// cannot be served — retrying the identical request fails forever. The
	// two need different remediation (wait / switch model vs. drop the
	// attachments / switch model), so they must not share a sentence.
	MsgModalityMismatch = "modality_mismatch" // "Model '{{.Model}}' does not accept {{.Modality}} input. This request contains {{.Modality}} content, but the model is registered as {{.RegisteredModality}}-only. Send a text-only request to this model, or route the request to a model that supports {{.Modality}}."
	// MsgNoCandidateBlocked: the model IS registered, but every candidate for
	// it is blocked by a gateway state gate (upstream quota/balance exhausted,
	// provider or credential disabled by the operator, …). {{.Causes}} is the
	// localised, semicolon-joined list built from the provider.EmptyCandidate*
	// cause phrases below.
	//
	// ★ Why it must not reuse MsgNoCandidate (2026-11-22, 211 incident):
	// "No available provider" describes a momentary state, so the client's
	// rational reaction is to retry — and the Anthropic envelope's
	// overloaded_error says exactly that. But a credential whose upstream
	// account is out of balance stays blocked until someone tops it up:
	// the owner saw "this model always fails" for an hour with no hint that
	// the cause was upstream balance. The sentence has to carry the cause
	// and say plainly that retrying will not help.
	MsgNoCandidateBlocked = "no_candidate_blocked" // "No available provider for model '{{.Model}}': every candidate for it is blocked ({{.Causes}}). Retrying the same request will not help — switch to another model, or contact the gateway operator."
	// Cause phrases for MsgNoCandidateBlocked. Bounded set, one per
	// provider.ClientCause* token; deliberately carry no counts (how many
	// upstream credentials a model has is operator-side information).
	MsgNoCandidateCauseQuota             = "no_candidate_cause_quota"              // "upstream account quota or balance exhausted"
	MsgNoCandidateCauseDisabled          = "no_candidate_cause_disabled"           // "provider or credential disabled by the gateway operator"
	MsgNoCandidateCauseCredentialMissing = "no_candidate_cause_credential_missing" // "credential records missing"
	MsgNoCandidateCauseOtherTenant       = "no_candidate_cause_other_tenant"       // "providers belong to another tenant"
	MsgNoCandidateCauseNotRegistered     = "no_candidate_cause_not_registered"     // "model not registered"
	// Ingress (LargeIngress) rejections — 2026-11-22, 211 incident. The client
	// used to get a bare English sentence that named neither the request body
	// size nor whether a retry could help ("request preprocessing exceeded its
	// time limit"). These carry the facts ({{.Size}} / {{.Budget}} / {{.Limit}})
	// and the remediation; the stable English sentence still goes to the
	// request log (see ingressRejection).
	MsgIngressPreprocessTimeout     = "ingress_preprocess_timeout"
	MsgIngressContextBudgetExceeded = "ingress_context_budget_exceeded"
	// MsgIngressSingleMessageTooLarge: P1-C (2026-11-23) 单条受保护消息超过上下文预算。
	// 与 MsgIngressContextBudgetExceeded 的区别：后者可能是多条累加，前者是单条即超限。
	// 针对性文案可指导用户压缩媒体/分块/使用 URL 引用。
	MsgIngressSingleMessageTooLarge = "ingress_single_message_too_large"
	MsgIngressPromptTooLarge        = "ingress_prompt_too_large"
	// MsgModelDeprecated: upstream provider has permanently end-of-lifed the
	// requested model (HTTP 410 Gone). The handler appends the upstream's own
	// EOL reason so the client knows which model was removed.
	MsgModelDeprecated = "model_deprecated" // "The requested model has been permanently removed by the upstream provider."
	MsgMetaToolError   = "meta_tool_error"  // "Meta-tool processing failed"
	MsgProviderError   = "provider_error"   // "upstream request failed"

	// Upstream credential errors — the gateway's stored credential for an
	// upstream provider was rejected by that provider (HTTP 401/403/402).
	// These are distinct from the client-side invalid_key / missing_key
	// errors above: the client's request was authenticated fine, but the
	// gateway's outbound credential is broken. Surfacing them with
	// dedicated codes lets ops dashboards filter "is the *upstream*
	// credential broken?" without grepping the upstream error body, and
	// tells the client "this is an ops problem, not your key".
	MsgUpstreamCredentialInvalid = "upstream_credential_invalid" // "Upstream credential API key invalid"
	MsgUpstreamCredentialRevoked = "upstream_credential_revoked" // "Upstream credential API key revoked"
	MsgUpstreamQuotaPeriodic     = "upstream_quota_periodic"     // "Upstream credential periodic quota exhausted"
	MsgUpstreamQuotaPermanent    = "upstream_quota_permanent"    // "Upstream credential quota permanently exhausted"
	// 2026-08-09: KindQuota / KindQuotaBalance previously had no key of their
	// own, so classifyUpstreamCredentialFailure returned "" for them and the
	// caller fell through to the 503 model_not_found "No available provider"
	// branch — a balance-exhaustion event was reported to the client as a
	// missing model. Balance is kept separate from Permanent because the ops
	// action differs: top up the account vs. rotate the credential.
	MsgUpstreamQuotaBalance = "upstream_quota_balance" // "Upstream credential account balance exhausted"
	MsgUpstreamQuotaGeneric = "upstream_quota_generic" // "Upstream credential quota exhausted"

	// Generic.
	MsgInternalError = "internal_error"
)

// LocalizeConfig carries the parameters for a single translation lookup. It is
// the public mirror of i18n.LocalizeConfig so callers do not depend on go-i18n
// directly and so plural selection can be added later without API churn.
type LocalizeConfig struct {
	// MessageID is the translation key (one of the Msg* constants).
	MessageID string

	// TemplateData holds interpolation values, e.g. {"Model": "gpt-4o"} for
	// MsgNoCandidate. Keys must match the {{.Name}} placeholders in the
	// catalog. May be nil.
	TemplateData map[string]any

	// PluralCount selects a plural form when the message defines one. Leave nil
	// for plain (non-plural) messages. Not used by current keys but reserved.
	PluralCount any
}

// T translates a message for the Locale on ctx. It is the convenience entry
// point for the common case (key + optional interpolation). For full control
// (e.g. plurals) use Localize.
//
// Lookup order: requested locale → English (source) → the MessageID itself.
// T therefore never returns an error: a missing/unknown key yields a usable
// string rather than a blank, preserving the error envelope's usefulness.
func T(ctx context.Context, messageID string, templateData ...map[string]any) string {
	var data map[string]any
	if len(templateData) > 0 {
		data = templateData[0]
	}
	return Localize(ctx, LocalizeConfig{MessageID: messageID, TemplateData: data})
}

// Localize translates a message for the Locale on ctx with full configuration.
// See LocalizeConfig for field semantics.
func Localize(ctx context.Context, cfg LocalizeConfig) string {
	loc := LocaleFromContext(ctx)
	msg, err := localizerFor(loc).Localize(&i18n.LocalizeConfig{
		MessageID:    cfg.MessageID,
		TemplateData: cfg.TemplateData,
		PluralCount:  cfg.PluralCount,
	})
	if err == nil && msg != "" {
		return msg
	}
	// Fallback 1: English (source language) — covers a locale that exists in
	// the bundle but lacks this specific key.
	if loc != DefaultLocale {
		if msg, err := localizerFor(DefaultLocale).Localize(&i18n.LocalizeConfig{
			MessageID:    cfg.MessageID,
			TemplateData: cfg.TemplateData,
			PluralCount:  cfg.PluralCount,
		}); err == nil && msg != "" {
			return msg
		}
	}
	// Fallback 2: the raw key. Worst case the client still gets a stable,
	// machine-readable code rather than an empty string.
	if cfg.MessageID == "" {
		return ""
	}
	return formatWithTemplate(cfg.MessageID, cfg.TemplateData)
}

// formatWithTemplate is the last-resort renderer: it returns the MessageID, and
// when TemplateData is present, appends " (key=value ...)" so context isn't
// entirely lost during a missing-translation incident.
func formatWithTemplate(messageID string, data map[string]any) string {
	if len(data) == 0 {
		return messageID
	}
	out := messageID
	first := true
	for k, v := range data {
		if first {
			out += fmt.Sprintf(" (%s=%v", k, v)
			first = false
		} else {
			out += fmt.Sprintf(", %s=%v", k, v)
		}
	}
	out += ")"
	return out
}
