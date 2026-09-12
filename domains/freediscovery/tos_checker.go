package freediscovery

import "strings"

// ToSChecker keyword-rule based ToS compliance first-pass check.
// MVP uses a conservative strategy: when it cannot decide → ambiguous (requires
// manual review); never guess "ok".
type ToSChecker struct {
	rules map[string]*ToSRule
}

// ToSRule ToS judgement rule for a single provider.
type ToSRule struct {
	ProviderCode string
	// ModelID keyword classification (lowercase match).
	AvoidKeywords   []string // match → avoid (e.g. vision-model ToS forbids automation)
	CautionKeywords []string // match → caution (e.g. vision/experimental)
	AllowKeywords   []string // match → ok (e.g. :free / official "free" marking)
	// ProviderVerdict provider-level fallback verdict (template tos_verdict takes precedence).
	ProviderVerdict string
	Notes           string
}

// NewToSChecker constructs the checker and automatically loads rules from the
// built-in presets.
func NewToSChecker() *ToSChecker {
	c := &ToSChecker{rules: make(map[string]*ToSRule)}
	for code, p := range builtinPresets {
		c.rules[code] = &ToSRule{
			ProviderCode:    code,
			AllowKeywords:   []string{":free", "-free", "free-"},
			CautionKeywords: []string{"vision", "preview", "experimental", "beta"},
			AvoidKeywords:   []string{"discontinued", "deprecated"},
			ProviderVerdict: p.TosVerdict,
			Notes:           p.TosNotes,
		}
	}
	return c
}

// Check performs a ToS first-pass judgement on a single model. When the template
// already has a verdict, that value takes precedence.
func (c *ToSChecker) Check(tpl *ProviderTemplate, modelID string) (verdict, notes string) {
	if tpl != nil && tpl.TosVerdict != "" && tpl.TosVerdict != "unknown" {
		// Template-level verdict takes precedence (manual review conclusion), but
		// avoid keywords can still downgrade it.
		verdict = tpl.TosVerdict
		notes = tpl.TosNotes
	}

	id := strings.ToLower(modelID)
	rule := c.rules[tplProviderCode(tpl)]
	if rule == nil {
		if verdict == "" {
			return "unknown", "no ToS rule for provider"
		}
		return verdict, notes
	}

	if containsAny(id, rule.AvoidKeywords) {
		return "avoid", "model id contains " + strings.Join(rule.AvoidKeywords, "/") + " keyword"
	}
	if containsAny(id, rule.CautionKeywords) {
		// caution keyword hit: when template-level is ok, downgrade to caution.
		if verdict == "ok" {
			return "caution", "downgraded from template verdict: model id contains caution keyword"
		}
		if verdict == "" {
			return "caution", "model id contains caution keyword"
		}
	}
	if verdict == "" {
		if containsAny(id, rule.AllowKeywords) {
			if rule.ProviderVerdict == "avoid" || rule.ProviderVerdict == "caution" {
				// When the provider-level verdict is more conservative, use the provider-level verdict.
				return rule.ProviderVerdict, rule.Notes
			}
			return "ok", "official free marking in model id"
		}
		// No keyword hit: fall back to provider-level, then to ambiguous (conservative).
		if rule.ProviderVerdict == "ok" || rule.ProviderVerdict == "caution" {
			return rule.ProviderVerdict, rule.Notes
		}
		return "ambiguous", "no explicit free marking; manual review required"
	}
	return verdict, notes
}

func tplProviderCode(tpl *ProviderTemplate) string {
	if tpl == nil {
		return ""
	}
	return tpl.ProviderCode
}

func containsAny(s string, keywords []string) bool {
	for _, kw := range keywords {
		if kw != "" && strings.Contains(s, kw) {
			return true
		}
	}
	return false
}
