package sanitize

import (
	"context"
	"testing"
)

// 2026-10-10 — audit 244 F2 regression gate.
//
// F2 said the main-line secret rule was `(sk|ak)-[a-zA-Z0-9]{16,}`: the
// character class excludes `-`, so every real vendor key whose body carries
// an infix hyphen (sk-proj-…, sk-ant-api03-…, sk-or-v1-…) was a silent miss.
// A miss here is not a cosmetic gap — the plaintext key is forwarded to the
// upstream provider and persisted in _request_logs_bodies, which is exactly
// the leak this package exists to prevent.
//
// The fix admits hyphen-joined segments. Two constraints keep the widened
// shape from becoming a new false-positive source, and both are asserted
// below rather than assumed: the 16-character floor, and a digit requirement
// (pure-alphabetic prose like "api-design-patterns" would otherwise match).
//
// The same three constraints live in configs/sensitive_patterns.yaml, which
// is what production actually loads (goal_control.go:newSanitizePatternDetector
// → NewPatternDetectorFromFile). TestSecretRuleMatchesShippedYAMLConfig pins
// the two copies together so they cannot drift apart silently again.

func secretHits(t *testing.T, d *PatternDetector, text string) []SensitiveFragment {
	t.Helper()
	frags, err := d.Detect(context.Background(), text)
	if err != nil {
		t.Fatalf("Detect(%q): %v", text, err)
	}
	return frags
}

func assertSecretHit(t *testing.T, d *PatternDetector, label, text string) {
	t.Helper()
	for _, f := range secretHits(t, d, text) {
		if f.Type == TypeSecret {
			return
		}
	}
	t.Errorf("%s: expected a secret fragment for %q, got %+v", label, text, secretHits(t, d, text))
}

func assertNoSecretHit(t *testing.T, d *PatternDetector, label, text string) {
	t.Helper()
	for _, f := range secretHits(t, d, text) {
		if f.Type == TypeSecret {
			t.Errorf("%s: unexpected secret fragment %q for %q", label, f.Value, text)
		}
	}
}

// TestSecretRuleMatchesHyphenatedVendorFormats is the F2 gate proper: every
// format named in the audit must now be caught.
func TestSecretRuleMatchesHyphenatedVendorFormats(t *testing.T) {
	d := NewPatternDetector()

	hit := []struct {
		label string
		key   string
	}{
		{"openai project key", "sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV"},
		{"anthropic key", "sk-ant-api03-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV"},
		{"openrouter key", "sk-or-v1-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV"},
		{"openai legacy key", "sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ"},
		{"kaixuan key", "sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9"},
		{"aws style key", "ak-1234567890123456"},
		{"double hyphen body", "sk-probe-ABCDEFGHIJKLMNOP1234"},
	}
	for _, tc := range hit {
		t.Run(tc.label, func(t *testing.T) {
			assertSecretHit(t, d, tc.label, "我的密钥是 "+tc.key+" 请勿外传")
		})
	}
}

// TestSecretRuleFullSpanMatch pins that the match covers the WHOLE key. A
// partial match would leave the tail of the key in plaintext, so a
// prefix-only hit is a leak even though "a secret was detected".
func TestSecretRuleFullSpanMatch(t *testing.T) {
	d := NewPatternDetector()
	keys := []string{
		"sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV",
		"sk-ant-api03-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV",
		"sk-or-v1-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV",
		"sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ",
		"ak-1234567890123456",
	}
	for _, key := range keys {
		frags := secretHits(t, d, "key="+key)
		found := false
		for _, f := range frags {
			if f.Type == TypeSecret && f.Value == key {
				found = true
			}
		}
		if !found {
			t.Errorf("key %q: no fragment spans the full key (got %+v)", key, frags)
		}
	}
}

// TestSecretRuleRejectsProseAndShortTokens guards the two false-positive
// classes the widened shape would otherwise open up. These strings all
// contain a rule prefix followed by 16+ hyphen-joined characters, which is
// exactly the shape the fix makes matchable.
func TestSecretRuleRejectsProseAndShortTokens(t *testing.T) {
	d := NewPatternDetector()

	clean := []struct {
		label string
		text  string
	}{
		{"short token", "sk-test"},
		{"short ak", "ak-short"},
		{"prefix only", "sk-or-v1"},
		{"prose ask", "ask-me-about-tasks"},
		{"prose risk", "risk-management"},
		{"prose task oriented", "task-oriented-design"},
		{"prose disk space", "disk-space"},
		{"prose mask filter", "mask-filter"},
		{"prose api design", "api-design-patterns"},
		{"prose bearer", "bearer-of-responsibility"},
		{"prose token bucket", "token-bucket-algorithm"},
		{"prose key value", "key-value-store"},
		{"prose token expiry", "user-token-expiration-policy"},
	}
	for _, tc := range clean {
		t.Run(tc.label, func(t *testing.T) {
			assertNoSecretHit(t, d, tc.label, tc.text)
		})
	}
}

// TestCredibleSecretCandidate pins the whole-string qualification directly,
// including the separator variants the pattern admits. The rule strips only
// up to the first '-', so a '_'-separated candidate such as `key_…` keeps its
// prefix inside the measured body — asserted here so that interaction stays
// intentional rather than accidental.
func TestCredibleSecretCandidate(t *testing.T) {
	cases := []struct {
		value string
		want  bool
		why   string
	}{
		{"sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ", true, "legacy openai key"},
		{"sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9", true, "kaixuan key"},
		{"sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV", true, "openai project key"},
		{"key_Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo", true, "underscore separator"},
		{"token-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo", true, "token with dash"},
		{"apiAb3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo", true, "api without separator"},
		{"sk-test", false, "short token"},
		{"ak-short", false, "short ak token"},
		{"sk-abcdefghijklmnop", false, "long enough but no digit"},
		{"bearer-of-responsibility", false, "prose"},
		{"token-bucket-algorithm", false, "prose"},
		{"key-value-store", false, "prose"},
	}
	for _, c := range cases {
		t.Run(c.why, func(t *testing.T) {
			if got := credibleSecretCandidate(c.value); got != c.want {
				t.Errorf("credibleSecretCandidate(%q) = %v, want %v", c.value, got, c.want)
			}
		})
	}
}

// TestSecretRuleMatchesShippedYAMLConfig keeps the built-in fallback and the
// shipped YAML in agreement. Production loads the YAML (not the fallback), so
// a fix applied to only one of the two copies would pass the tests above and
// still ship unfixed.
func TestSecretRuleMatchesShippedYAMLConfig(t *testing.T) {
	d, err := NewPatternDetectorFromFile("../../configs/sensitive_patterns.yaml")
	if err != nil {
		t.Fatalf("load shipped YAML: %v", err)
	}

	hit := []string{
		"sk-proj-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV",
		"sk-ant-api03-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV",
		"sk-or-v1-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNoPqRsTuV",
		"sk-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJ",
		"sk-1vH6C2I9pywyvUXaUXj4vdMZbeYVE5VB0fBYVgqA97JrltE9",
		"bearer Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo",
		"key_Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo",
		"token-Ab3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo",
		"apiAb3dEfGh1jKlMn0pQrStUvWxYz4aBcDeFgHiJkLmNo",
		"sk-probe-ABCDEFGHIJKLMNOP1234",
	}
	for _, key := range hit {
		assertSecretHit(t, d, "yaml", "密钥是 "+key+" 请勿外传")
	}

	clean := []string{
		"sk-test", "key-abc", "api-x", "token-x", "sk-or-v1",
		"ask-me-about-tasks", "risk-management", "task-oriented-design",
		"disk-space", "mask-filter", "api-design-patterns",
		"bearer-of-responsibility", "token-bucket-algorithm", "key-value-store",
		"user-token-expiration-policy",
	}
	for _, text := range clean {
		assertNoSecretHit(t, d, "yaml", text)
	}
}
