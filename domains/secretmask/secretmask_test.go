package secretmask

import (
	"strings"
	"testing"
)

// TestMaskSecrets_ProviderKeys covers the canonical provider key shapes that
// are the primary C2 concern: keys pasted into conversation bodies.
func TestMaskSecrets_ProviderKeys(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"openai", "my key is sk-" + strings.Repeat("a", 20) + "T3BlbkFJ" + strings.Repeat("b", 20) + " ok"},
		{"anthropic", "use sk-ant-" + strings.Repeat("c", 40) + " for this"},
		{"openai-generic", "sk-" + strings.Repeat("d", 40)},
		{"aws", "creds AKIA" + strings.Repeat("0", 16) + " secret"},
		{"google", "api AIza" + strings.Repeat("e", 35) + " done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := MaskSecrets(c.input)
			if out == c.input {
				t.Fatalf("input unchanged; secret not masked: %s", c.input)
			}
			if !strings.Contains(out, Placeholder) {
				t.Fatalf("output missing placeholder %q: %s", Placeholder, out)
			}
		})
	}
}

// TestMaskSecrets_BearerTokens covers Authorization/Bearer header forms.
func TestMaskSecrets_BearerTokens(t *testing.T) {
	cases := []string{
		"Authorization: Bearer " + strings.Repeat("f", 40),
		"bearer " + strings.Repeat("g", 40),
		"x-api-key: " + strings.Repeat("h", 40),
	}
	for _, in := range cases {
		out := MaskSecrets(in)
		if out == in {
			t.Fatalf("bearer token not masked: %s", in)
		}
		if !strings.Contains(out, Placeholder) {
			t.Fatalf("output missing placeholder: %s", out)
		}
	}
}

// TestMaskSecrets_PreservesLegitText guards against false positives: ordinary
// prose and short tokens must survive unchanged.
func TestMaskSecrets_PreservesLegitText(t *testing.T) {
	cases := []string{
		"hello world",
		"please summarize this conversation",
		"the user asked about sky color", // "sky" must not match sk-
		"token count is 42",
		"a short id 12345",
	}
	for _, in := range cases {
		if out := MaskSecrets(in); out != in {
			t.Fatalf("legit text altered: %q -> %q", in, out)
		}
	}
}

// TestMaskSecrets_MultipleAndEmpty covers edge cases: multiple secrets in one
// string, empty input, and a string with no secrets.
func TestMaskSecrets_MultipleAndEmpty(t *testing.T) {
	// Multiple distinct secrets in one body.
	in := "key1 sk-" + strings.Repeat("a", 40) + " and key2 AKIA" + strings.Repeat("0", 16)
	out := MaskSecrets(in)
	if cnt := strings.Count(out, Placeholder); cnt != 2 {
		t.Fatalf("expected 2 placeholders, got %d in %q", cnt, out)
	}

	if MaskSecrets("") != "" {
		t.Fatal("empty input should return empty")
	}

	plain := "no secrets here at all"
	if MaskSecrets(plain) != plain {
		t.Fatal("plain text altered")
	}
}
