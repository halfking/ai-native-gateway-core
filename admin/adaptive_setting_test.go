package admin

import (
	"encoding/json"
	"testing"

	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// TestValidateSettingValue_AdaptiveAllowed pins the wire shape of the
// "which protocols does this upstream actually serve" declaration.
//
// Shape is an ORDERED JSON string array; order is semantic because the first
// entry is the base protocol used when a model's recommended protocol is not
// declared (see catalog.ResolveAdaptiveProtocol).
//
// Rejection rules matter as much as acceptance: the selector itself must not be
// accepted as a "usable protocol" (that would make the resolver reference
// itself), and non-string-array shapes must 400 rather than silently store
// something the reader cannot parse.
func TestValidateSettingValue_AdaptiveAllowed(t *testing.T) {
	accepted := []string{
		`[]`,
		`["openai-completions"]`,
		`["openai-responses","openai-completions"]`,
		`["openai-completions","openai-responses","anthropic-messages"]`,
		`["openai-response"]`, // alias: accepted on write, normalized on read
		`["openai-responses","openai-responses"]`,
	}
	for _, raw := range accepted {
		if err := validateSettingValue(ProviderSettingKeyAdaptiveAllowed, json.RawMessage(raw)); err != nil {
			t.Errorf("validateSettingValue(%q) = %v, want nil", raw, err)
		}
	}

	rejected := []struct {
		name string
		raw  string
	}{
		{"not an array", `"openai-responses"`},
		{"number", `42`},
		{"null", `null`},
		{"object", `{"a":"b"}`},
		{"array of numbers", `[1,2]`},
		{"array of objects", `[{"p":"openai-responses"}]`},
		{"mixed array", `["openai-responses",1]`},
		{"unknown protocol", `["grpc"]`},
		{"selector as usable protocol", `["adaptive"]`},
		{"selector among valid", `["openai-responses","adaptive"]`},
		{"blank entry", `[""]`},
		{"malformed json", `["openai-responses"`},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateSettingValue(ProviderSettingKeyAdaptiveAllowed, json.RawMessage(tc.raw)); err == nil {
				t.Errorf("validateSettingValue(%q) = nil, want error", tc.raw)
			}
		})
	}
}

// TestSettingKeyIsWritable is a guard against the classic half-ship: the reader
// (provider/adaptive_protocol.go) knows the key, but the writer's hardcoded
// allow-list was never extended, so the declaration can never be stored through
// the API. Both sides must agree.
func TestSettingKeyIsWritable(t *testing.T) {
	keys := []string{
		"compression.mode",
		"cache.enabled",
		"format_conversion.enabled",
		ProviderSettingKeyAdaptiveAllowed,
	}
	for _, key := range keys {
		if !isWritableSettingKey(key) {
			t.Errorf("setting key %q is not writable — the declaration channel is closed", key)
		}
	}
	if isWritableSettingKey("protocol.adaptive.unknown") {
		t.Error("unknown key accepted as writable")
	}
}

// TestProvierSettingKeyMatchesCatalogKey keeps the admin-side constant and the
// provider-side constant from drifting. The two live in different packages and
// are compared as a raw string in SQL, so a typo on either side is silent.
func TestProvierSettingKeyMatchesCatalogKey(t *testing.T) {
	if ProviderSettingKeyAdaptiveAllowed != providercatalog.AdaptiveAllowedSettingKey {
		t.Fatalf("admin key %q != catalog key %q",
			ProviderSettingKeyAdaptiveAllowed, providercatalog.AdaptiveAllowedSettingKey)
	}
}
