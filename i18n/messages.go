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

	// Session errors.
	MsgSessionForbidden    = "SESSION_FORBIDDEN"     // "session not owned by this API key"
	MsgSessionAssignFailed = "SESSION_ASSIGN_FAILED" // "failed to assign session id"

	// Security policy.
	MsgBlocked = "blocked" // "Request blocked by security policy"

	// Model / provider selection.
	MsgNoCandidate   = "no_candidate"    // "No available provider for model '{{.Model}}'"
	MsgMetaToolError = "meta_tool_error" // "Meta-tool processing failed"
	MsgProviderError = "provider_error"  // "upstream request failed"

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
