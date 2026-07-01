package middleware

import (
	"net/http"

	"github.com/kaixuan/llm-gateway-go/i18n"
)

// LocaleMiddleware resolves the request locale once per inbound request and
// stores it on the request context, so every downstream handler/writer can
// localize its response via i18n.LocaleFromContext(r.Context()).
//
// It must run BEFORE AuthMiddleware so that authentication-failure responses
// are themselves localized (those are written by auth_mw.go, which reads the
// locale from context). Placing it right after RequestIDMiddleware matches the
// chain order in cmd/gateway/main.go.
type LocaleMiddleware struct {
	BaseMiddleware
	defaultLanguage string
}

// NewLocaleMiddleware creates the locale middleware. defaultLanguage is the
// config-tier fallback (see config.DefaultLanguage); pass "" to let i18n.Detect
// fall back to English.
func NewLocaleMiddleware(defaultLanguage string) *LocaleMiddleware {
	return &LocaleMiddleware{
		BaseMiddleware:  BaseMiddleware{name: "locale"},
		defaultLanguage: defaultLanguage,
	}
}

func (m *LocaleMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loc := i18n.Detect(r, m.defaultLanguage)
		ctx := i18n.WithLocale(r.Context(), loc)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
