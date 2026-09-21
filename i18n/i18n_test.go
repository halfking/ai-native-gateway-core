package i18n

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalizerLoadsAllLocales(t *testing.T) {
	// Every shipped locale must resolve MsgInvalidKey to a non-empty,
	// non-key string (i.e. the catalog actually loaded, not the fallback).
	for _, loc := range Supported() {
		ctx := WithLocale(context.Background(), loc)
		got := T(ctx, MsgInvalidKey)
		if got == "" {
			t.Errorf("locale %s: MsgInvalidKey resolved empty", loc)
		}
		if got == MsgInvalidKey {
			t.Errorf("locale %s: MsgInvalidKey fell back to raw key (catalog not loaded)", loc)
		}
	}
}

// upstreamCredentialKeys are the client-facing codes emitted when the
// upstream provider rejects the gateway's own stored credential.
var upstreamCredentialKeys = []string{
	MsgUpstreamCredentialInvalid,
	MsgUpstreamCredentialRevoked,
	MsgUpstreamQuotaPeriodic,
	MsgUpstreamQuotaPermanent,
	// 2026-08-09: added when KindQuota / KindQuotaBalance were finally mapped
	// in classifyUpstreamCredentialFailure. Before that, both kinds produced
	// 503 model_not_found "No available provider" and needed no message.
	MsgUpstreamQuotaBalance,
	MsgUpstreamQuotaGeneric,
}

// allMessageKeys is the union of every user-facing code the gateway can emit.
// Kept in sync with the Msg* constants in messages.go so a new key that is
// referenced at runtime but never added to a locale catalog fails CI instead
// of silently falling back to English or the raw key.
var allMessageKeys = []string{
	MsgMissingKey,
	MsgInvalidKey,
	MsgMissingAuth,
	MsgRateLimitExceeded,
	MsgBudgetExhausted,
	MsgInsufficientCredits,
	MsgSessionForbidden,
	MsgSessionAssignFailed,
	MsgBlocked,
	MsgContentFilter,
	MsgContentFilterHint,
	MsgNoCandidate,
	MsgInvalidModel,
	MsgUnsupportedFeature,
	MsgModelDeprecated,
	MsgMetaToolError,
	MsgProviderError,
	MsgUpstreamCredentialInvalid,
	MsgUpstreamCredentialRevoked,
	MsgUpstreamQuotaPeriodic,
	MsgUpstreamQuotaPermanent,
	MsgUpstreamQuotaBalance,
	MsgUpstreamQuotaGeneric,
	MsgInternalError,
}

// TestLocaleCatalogsCoverAllMessageKeys asserts every shipped locale carries
// its OWN translation for every runtime-referenced message key.
//
// This is the generalized sibling of
// TestLocaleCatalogsCoverUpstreamCredentialKeys. It reads the embedded catalog
// directly (see localeCatalogKeys) because T() silently falls back to English
// for a key a locale lacks, so a T()-based assertion cannot tell "translated"
// from "missing and served in English".
func TestLocaleCatalogsCoverAllMessageKeys(t *testing.T) {
	for _, loc := range Supported() {
		keys := localeCatalogKeys(t, loc)
		for _, key := range allMessageKeys {
			if _, ok := keys[key]; !ok {
				t.Errorf("locale %s: catalog is missing key %q (T() would silently serve English/raw key)", loc, key)
			}
		}
	}
}

// localeCatalogKeys reads the embedded catalog for loc and returns its
// top-level message keys.
//
// This deliberately bypasses T(): go-i18n falls back to the English
// catalog for any key a locale is missing, so a T()-based assertion
// returns the English sentence — non-empty and different from the raw
// key — and therefore CANNOT distinguish "translated" from "missing and
// silently served in English". Reading the catalog file is the only way
// to assert real per-locale coverage.
func localeCatalogKeys(t *testing.T, loc Locale) map[string]struct{} {
	t.Helper()
	raw, err := embeddedLocales.ReadFile(fmt.Sprintf("locales/%s.json", loc))
	if err != nil {
		t.Fatalf("locale %s: embedded catalog unreadable: %v", loc, err)
	}
	var catalog map[string]json.RawMessage
	if err := json.Unmarshal(raw, &catalog); err != nil {
		t.Fatalf("locale %s: catalog is not valid JSON: %v", loc, err)
	}
	keys := make(map[string]struct{}, len(catalog))
	for k := range catalog {
		keys[k] = struct{}{}
	}
	return keys
}

// TestLocaleCatalogsCoverUpstreamCredentialKeys asserts every shipped
// locale carries its OWN translation for each upstream-credential code.
//
// Regression guarded (2026-08-08): MsgUpstreamQuotaPeriodic was added to
// seven locales but omitted from zh-TW. The previous T()-based test
// passed anyway because zh-TW silently fell back to the English string,
// so a Traditional Chinese client would have been shown English text
// with no test failure anywhere.
func TestLocaleCatalogsCoverUpstreamCredentialKeys(t *testing.T) {
	for _, loc := range Supported() {
		keys := localeCatalogKeys(t, loc)
		for _, key := range upstreamCredentialKeys {
			if _, ok := keys[key]; !ok {
				t.Errorf("locale %s: catalog is missing key %q (T() would silently serve English)", loc, key)
			}
		}
	}
}

// TestLocalizerLoadsAllLocales_UpstreamCredentialKeys keeps the runtime
// invariant: T() must resolve each code to a non-empty, non-raw-key
// string. TestLocaleCatalogsCoverUpstreamCredentialKeys covers the
// per-locale completeness this cannot see.
func TestLocalizerLoadsAllLocales_UpstreamCredentialKeys(t *testing.T) {
	for _, loc := range Supported() {
		ctx := WithLocale(context.Background(), loc)
		for _, key := range upstreamCredentialKeys {
			got := T(ctx, key)
			if got == "" {
				t.Errorf("locale %s: %s resolved empty", loc, key)
			}
			if got == key {
				t.Errorf("locale %s: %s fell back to raw key (catalog not loaded)", loc, key)
			}
		}
	}
}

func TestTFallback(t *testing.T) {
	ctx := WithLocale(context.Background(), Ja)
	// Unknown key → fallback to English, then to raw key.
	if got := T(ctx, "definitely_not_a_key"); got != "definitely_not_a_key" {
		t.Errorf("unknown key: want raw key, got %q", got)
	}
}

func TestTemplateInterpolation(t *testing.T) {
	ctx := WithLocale(context.Background(), En)
	got := T(ctx, MsgNoCandidate, map[string]any{"Model": "gpt-4o"})
	want := "No available provider for model 'gpt-4o'"
	if got != want {
		t.Errorf("interpolation: want %q, got %q", want, got)
	}
	// Chinese interpolation uses full-width quotes.
	ctxZh := WithLocale(context.Background(), ZhCN)
	if got := T(ctxZh, MsgNoCandidate, map[string]any{"Model": "gpt-4o"}); got == "" {
		t.Error("zh-CN interpolation resolved empty")
	}
}

func TestDetectPriority(t *testing.T) {
	tests := []struct {
		name        string
		xLang       string
		accept      string
		defaultLang string
		want        Locale
	}{
		{"x-lang wins", "ja", "en", "", Ja},
		{"accept-lang fallback", "", "de", "", De},
		{"config default", "", "", "fr", Fr},
		{"ultimate fallback to en", "", "", "", En},
		{"invalid x-lang falls through to accept", "garbage!!", "es", "", Es},
		{"accept-lang with q-weights", "", "zh-TW,zh-CN;q=0.8,en;q=0.5", "", ZhTW},
		{"accept-lang prefers exact over regional", "", "zh-Hant", "", ZhTW},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.xLang != "" {
				r.Header.Set("X-Lang", tt.xLang)
			}
			if tt.accept != "" {
				r.Header.Set("Accept-Language", tt.accept)
			}
			got := Detect(r, tt.defaultLang)
			if got != tt.want {
				t.Errorf("Detect: want %s, got %s", tt.want, got)
			}
		})
	}
}

func TestLocaleFromContextDefault(t *testing.T) {
	// Nil/empty context → DefaultLocale, never zero value.
	if got := LocaleFromContext(context.Background()); got != DefaultLocale {
		t.Errorf("LocaleFromContext(Background) = %s, want %s", got, DefaultLocale)
	}
	if got := LocaleFromContext(nil); got != DefaultLocale { //nolint:staticcheck // SA1012: testing nil-safety
		t.Errorf("LocaleFromContext(nil) = %s, want %s", got, DefaultLocale)
	}
}

func TestIsRTL(t *testing.T) {
	if !IsRTL(Ar) {
		t.Error("Arabic should be RTL")
	}
	if IsRTL(En) {
		t.Error("English should not be RTL")
	}
}
