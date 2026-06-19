package modelname

import (
	"regexp"
	"strings"
)

var versionDashPair = regexp.MustCompile(`(^|[^0-9])(\d+)-(\d+)([^0-9]|$)`)
var versionDotPair = regexp.MustCompile(`(^|[^0-9])(\d+)\.(\d+)([^0-9]|$)`)

var removableWrapperTokens = map[string]bool{
	"thinking":  true,
	"reasoning": true,
	"flash":     true,
	"turbo":     true,
	"preview":   true,
	"highspeed": true,
	"air":       true,
	"vision":    true,
	"audio":     true,
}

// GenerateAliasVariants produces safe routing aliases for a model name.
// It intentionally stays conservative: only low-risk wrappers, provider
// prefixes, date suffixes, and version punctuation variants are generated.
func GenerateAliasVariants(values ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values)*4)

	add := func(v string) {
		v = strings.TrimSpace(strings.ToLower(v))
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}

	for _, value := range values {
		norm := NormalizeRouteKey(value)
		if norm == "" {
			continue
		}
		add(value)
		add(norm)
		add(strings.ReplaceAll(norm, "-", "_"))
		add(strings.ReplaceAll(norm, "_", "-"))
		for _, variant := range versionPunctuationVariants(norm) {
			add(variant)
		}
		for _, variant := range stripWrapperVariants(norm) {
			add(variant)
			for _, punct := range versionPunctuationVariants(variant) {
				add(punct)
			}
		}
	}

	return out
}

func versionPunctuationVariants(v string) []string {
	variants := make([]string, 0, 2)
	if strings.Contains(v, "-") {
		variants = append(variants, versionDashPair.ReplaceAllString(v, `${1}${2}.${3}${4}`))
	}
	if strings.Contains(v, ".") {
		variants = append(variants, versionDotPair.ReplaceAllString(v, `${1}${2}-${3}${4}`))
	}
	return variants
}

func stripWrapperVariants(v string) []string {
	tokens := strings.Split(v, "-")
	if len(tokens) < 2 {
		return nil
	}
	variants := []string{}
	if removableWrapperTokens[tokens[0]] {
		variants = append(variants, strings.Join(tokens[1:], "-"))
	}
	if removableWrapperTokens[tokens[len(tokens)-1]] {
		variants = append(variants, strings.Join(tokens[:len(tokens)-1], "-"))
	}
	return variants
}
