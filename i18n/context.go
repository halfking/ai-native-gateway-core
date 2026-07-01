package i18n

import "context"

// ctxKey is the unexported context key type used to carry the resolved Locale.
// Unexported prevents collisions with keys defined in other packages.
type ctxKey struct{}

// WithLocale returns a copy of ctx carrying loc. Handlers downstream read it via
// LocaleFromContext to pick the right translation for the active request.
func WithLocale(ctx context.Context, loc Locale) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, loc)
}

// LocaleFromContext returns the Locale stored on ctx, or DefaultLocale when no
// locale is present (e.g. background jobs, tests, or paths that skip the locale
// middleware). It never returns the zero value.
func LocaleFromContext(ctx context.Context) Locale {
	if ctx == nil {
		return DefaultLocale
	}
	v, ok := ctx.Value(ctxKey{}).(Locale)
	if !ok || v == "" {
		return DefaultLocale
	}
	return v
}
