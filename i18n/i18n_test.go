package i18n

import (
	"context"
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
