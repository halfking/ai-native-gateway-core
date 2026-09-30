package outputcompliance

import "regexp"

// NewBuiltinChecker keeps output inspection available in lite/data-plane mode
// and when the policy database cannot be initialized. It uses the same base
// PII rules seeded by ensurePIIPatternsTable and the default tenant policy.
// It has no database dependency and must be treated as a conservative fallback;
// tenant-specific exceptions require the full checker.
func NewBuiltinChecker() *Checker {
	patterns := []struct {
		name, kind, pattern string
		severity            int
	}{
		{"email_standard", "email", `[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`, 7},
		{"phone_cn_mobile", "phone", `1[3-9]\d{9}`, 8},
		{"id_card_cn_18", "id_card", `\d{17}[\dXx]`, 9},
		{"credit_card_visa", "credit_card", `4\d{15}`, 9},
	}
	checker := &Checker{biasDetector: NewBiasDetector(), toxicWords: map[string]*ToxicKeyword{}}
	for _, spec := range patterns {
		checker.piiPatterns = append(checker.piiPatterns, &PIIPattern{
			PatternName: spec.name, PatternType: spec.kind, RegexPattern: spec.pattern,
			Enabled: true, Severity: spec.severity, regex: regexp.MustCompile(spec.pattern),
		})
	}
	for _, word := range []struct {
		value, category, language string
		severity                  int
	}{
		{"傻逼", "profanity", "zh", 8},
		{"fuck", "profanity", "en", 9},
		{"kill", "violence", "en", 8},
		{"racist", "hate_speech", "en", 9},
	} {
		checker.toxicWords[word.value] = &ToxicKeyword{
			Keyword: word.value, Category: word.category, Language: word.language,
			Severity: word.severity, Enabled: true,
		}
	}
	return checker
}
