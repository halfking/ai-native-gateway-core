// Package i18n provides locale detection, translation lookup, and request
// context plumbing for the gateway's user-facing error messages.
//
// The package is backed by nicksnyder/go-i18n (v2) and golang.org/x/text for
// BCP-47 language negotiation. Translation sources live under i18n/locales and
// are embedded into the binary so the gateway is self-contained; external
// overrides can be layered on via Init (hot-reload friendly).
//
// Locale resolution priority (see Detect):
//  1. X-Lang request header (explicit client override)
//  2. Accept-Language header (negotiated against supported locales)
//  3. configured default language (config.DefaultLanguage)
//  4. English (the source/catalog language)
package i18n

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

// Locale is a BCP-47 language tag identifying a supported translation.
type Locale string

// Supported locales. String values are the BCP-47 tags used as message-file
// language tags (see i18n/locales/<tag>.json) and as go-i18n Localizer ids.
const (
	En   Locale = "en"    // English (source language / ultimate fallback)
	Ja   Locale = "ja"    // Japanese
	De   Locale = "de"    // German
	Fr   Locale = "fr"    // French
	Es   Locale = "es"    // Spanish
	ZhTW Locale = "zh-TW" // Traditional Chinese
	ZhCN Locale = "zh-CN" // Simplified Chinese
	Ar   Locale = "ar"    // Arabic (RTL)
)

// DefaultLocale is used when no locale can be determined. It is also the
// catalog source language: a missing translation always falls back here.
const DefaultLocale Locale = En

// supportedEntries pairs each supported Locale with its parsed language.Tag.
// Order matters: the first tag is the matcher's "exact" default.
var supportedEntries = []struct {
	locale Locale
	tag    language.Tag
}{
	{En, language.English},
	{ZhCN, language.MustParse("zh-CN")},
	{ZhTW, language.MustParse("zh-TW")},
	{Ja, language.MustParse("ja")},
	{De, language.MustParse("de")},
	{Fr, language.MustParse("fr")},
	{Es, language.MustParse("es")},
	{Ar, language.Arabic},
}

// supportedTags is the slice fed to language.NewMatcher. Built once via an
// IIFE so the matcher sees a fully-populated, immutable slice.
var supportedTags = func() []language.Tag {
	tags := make([]language.Tag, len(supportedEntries))
	for i, e := range supportedEntries {
		tags[i] = e.tag
	}
	return tags
}()

var matcher = language.NewMatcher(supportedTags)

// Supported returns the locales this gateway ships translations for.
func Supported() []Locale {
	out := make([]Locale, len(supportedEntries))
	for i, e := range supportedEntries {
		out[i] = e.locale
	}
	return out
}

// IsSupported reports whether loc is one of the shipped locales.
func IsSupported(loc Locale) bool {
	for _, e := range supportedEntries {
		if e.locale == loc {
			return true
		}
	}
	return false
}

// indexToLocale maps a matcher result index back to its Locale.
func indexToLocale(idx int) Locale {
	if idx >= 0 && idx < len(supportedEntries) {
		return supportedEntries[idx].locale
	}
	return DefaultLocale
}

// localeToIndex maps a Locale to its index in supportedEntries (-1 if absent).
//
//nolint:unused // Reserved for future locale lookup optimization
func localeToIndex(loc Locale) int {
	for i, e := range supportedEntries {
		if e.locale == loc {
			return i
		}
	}
	return -1
}

// matchTags negotiates one or more desired tags against the supported set.
// Returns the best supported Locale, or "" if the matcher rejected them all
// (only happens for malformed input).
func matchTags(desired ...language.Tag) Locale {
	if len(desired) == 0 {
		return ""
	}
	_, idx, conf := matcher.Match(desired...)
	if conf == language.No {
		return ""
	}
	return indexToLocale(idx)
}

// matchSingle parses a single language string (e.g. "ja", "zh-CN", "en-US")
// and negotiates it against the supported set. Returns "" on parse failure or
// no match.
func matchSingle(raw string) Locale {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	t, err := language.Parse(raw)
	if err != nil {
		// Accept-Language-style list fallback: let ParseAcceptLanguage handle
		// comma-separated values with q-weights.
		tags, _, perr := language.ParseAcceptLanguage(raw)
		if perr != nil || len(tags) == 0 {
			return ""
		}
		return matchTags(tags...)
	}
	return matchTags(t)
}

// matchAcceptLanguage negotiates an Accept-Language header value.
func matchAcceptLanguage(raw string) Locale {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	tags, _, err := language.ParseAcceptLanguage(raw)
	if err != nil || len(tags) == 0 {
		return ""
	}
	return matchTags(tags...)
}

// Detect resolves the Locale for an inbound request.
//
// Priority: X-Lang header > Accept-Language header > defaultLang (config) > En.
// An empty/unparseable value at any tier falls through to the next tier rather
// than failing, so a malformed X-Lang never blocks a valid Accept-Language.
func Detect(r *http.Request, defaultLang string) Locale {
	// 1. Explicit X-Lang override.
	if loc := matchSingle(r.Header.Get("X-Lang")); loc != "" {
		return loc
	}
	// 2. Standard Accept-Language negotiation.
	if loc := matchAcceptLanguage(r.Header.Get("Accept-Language")); loc != "" {
		return loc
	}
	// 3. Configured default language.
	if loc := matchSingle(defaultLang); loc != "" {
		return loc
	}
	// 4. Ultimate fallback.
	return DefaultLocale
}

// IsRTL reports whether the locale renders right-to-left (currently only ar).
// Callers may use this to set dir="rtl" on documents or flip layouts.
func IsRTL(loc Locale) bool {
	return loc == Ar
}
