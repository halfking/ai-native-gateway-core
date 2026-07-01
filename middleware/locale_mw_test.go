package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kaixuan/llm-gateway-go/i18n"
)

func TestLocaleMiddleware_InjectsLocaleIntoContext(t *testing.T) {
	var got i18n.Locale
	// raw handler just captures the resolved locale. The wrapping chain is
	// rebuilt per subtest so each case uses exactly one locale middleware
	// (double-wrapping would let an inner instance overwrite the outer one).
	raw := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = i18n.LocaleFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	execCase := func(defaultLang, xLang, accept string) {
		mw := NewLocaleMiddleware(defaultLang)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if xLang != "" {
			req.Header.Set("X-Lang", xLang)
		}
		if accept != "" {
			req.Header.Set("Accept-Language", accept)
		}
		mw.Wrap(raw).ServeHTTP(httptest.NewRecorder(), req)
	}

	t.Run("X-Lang header", func(t *testing.T) {
		execCase("", "ja", "")
		if got != i18n.Ja {
			t.Errorf("X-Lang=ja: want %s, got %s", i18n.Ja, got)
		}
	})

	t.Run("Accept-Language header", func(t *testing.T) {
		execCase("", "", "de-DE,de;q=0.9")
		if got != i18n.De {
			t.Errorf("Accept-Language=de: want %s, got %s", i18n.De, got)
		}
	})

	t.Run("config default fallback", func(t *testing.T) {
		execCase("fr", "", "")
		if got != i18n.Fr {
			t.Errorf("default=fr: want %s, got %s", i18n.Fr, got)
		}
	})

	t.Run("ultimate fallback to en", func(t *testing.T) {
		execCase("", "", "")
		if got != i18n.En {
			t.Errorf("no headers, no default: want %s, got %s", i18n.En, got)
		}
	})
}

func TestAuthMiddleware_LocalizedError(t *testing.T) {
	// The auth middleware runs AFTER the locale middleware in the chain, so
	// r.Context() already carries the locale. A missing/invalid key must
	// produce a 401 whose message is translated for the requested locale.
	mw := NewAuthMiddleware("correct-secret")

	t.Run("Japanese key error", func(t *testing.T) {
		localeMW := NewLocaleMiddleware("en")
		req := httptest.NewRequest(http.MethodGet, "/v1/chat", nil)
		req.Header.Set("Authorization", "Bearer wrong-key")
		req.Header.Set("X-Lang", "ja")
		rec := httptest.NewRecorder()

		chain := localeMW.Wrap(mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler should not be reached on bad key")
		})))
		chain.ServeHTTP(rec, req)

		body := rec.Body.String()
		// Look up the expected Japanese string directly (the recorder's
		// context wasn't the one that flowed through the middleware).
		wantJa := "APIキーが無効または期限切れです"
		if !contains(body, wantJa) {
			t.Errorf("Japanese auth error: response body missing %q\nbody: %s", wantJa, body)
		}
		if !contains(body, `"code":"invalid_key"`) {
			t.Errorf("code field changed: body: %s", body)
		}
	})

	t.Run("missing Authorization localized to default", func(t *testing.T) {
		localeMW := NewLocaleMiddleware("zh-CN")
		req := httptest.NewRequest(http.MethodGet, "/v1/chat", nil)
		rec := httptest.NewRecorder()
		chain := localeMW.Wrap(mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler should not be reached without Authorization")
		})))
		chain.ServeHTTP(rec, req)

		if !contains(rec.Body.String(), "缺少或格式错误的 Authorization 请求头") {
			t.Errorf("zh-CN missing-auth: body: %s", rec.Body.String())
		}
	})
}

// contains is a tiny local helper to avoid pulling in strings.Contains for one
// assertion; the middleware tests stay dependency-light.
func contains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
