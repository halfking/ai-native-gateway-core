// Package credentialtext distinguishes credential declarations from redaction code.
package credentialtext

import (
	"regexp"
	"strings"
)

var sedScript = regexp.MustCompile(`(?:^|[\s;|&()])(?:[/a-zA-Z0-9_.-]+/)?g?sed[ \t]+(?:-[a-zA-Z]+[ \t]*(?:''[ \t]+|""[ \t]+)?)*(?:'([^']*)'|"((?:\\.|[^"\\])*)")`)
var nextSedScript = regexp.MustCompile(`^[ \t]+-e[ \t]*(?:'([^']*)'|"((?:\\.|[^"\\])*)")`)
var wildcardAssignment = regexp.MustCompile(`(?i)^\^?(?:\\\()?["']?[a-z0-9_]*(?:password|passwd|pwd|secret|token|api_?key|access_key|authorization|密码|口令)["']?[ \t]*[:=：][ \t]*(?:\\\))?(?:\.\*|\.\+|\[\^(?: |;|,|\\s)\][*+])\$?$`)
var maskedReplacement = regexp.MustCompile(`(?i)^(?:(?:[a-z0-9_]*(?:password|passwd|pwd|secret|token|api_?key|access_key|authorization|密码|口令)[ \t]*[:=：][ \t]*)|\\[0-9])?(?:\[REDACTED(?::[a-z_]+)?\]|\[MASKED\]|<REDACTED>|\*{3,})$`)

// isPlaceholderSecretValue requires a complete masking replacement. It is only
// consulted after the sed parser proves a credential wildcard and complete
// substitution; a marker prefix alone never exempts a credential assignment.
func isPlaceholderSecretValue(value string) bool {
	return maskedReplacement.MatchString(value)
}

// DetectionText removes only known redaction syntax from credential detection.
// Byte offsets remain unchanged. Only a complete substitution with both a
// credential wildcard and a known masking replacement qualifies. Otherwise
// the original expression remains visible, including its credential context.
func DetectionText(text string) string {
	view, _ := DetectionView(text)
	return view
}

// DetectionView also returns byte ranges recognized as masking syntax. Callers
// can audit exemptions without retaining the script or any credential values.
func DetectionView(text string) (string, [][2]int) {
	view, spans := detectionView(text)
	return maskJSONRedaction(text, view, spans)
}

func detectionView(text string) (string, [][2]int) {
	if !strings.Contains(text, "sed") {
		return text, nil
	}
	var spans [][2]int
	view := []byte(text)
	for _, match := range sedScript.FindAllStringSubmatchIndex(text, -1) {
		maskScript(view, text, match, &spans)
		end := match[1]
		for {
			next := nextSedScript.FindStringSubmatchIndex(text[end:])
			if next == nil {
				break
			}
			for i := range next {
				if next[i] >= 0 {
					next[i] += end
				}
			}
			maskScript(view, text, next, &spans)
			end = next[1]
		}
	}
	return string(view), spans
}

func maskScript(view []byte, text string, match []int, spans *[][2]int) {
	start, end := match[2], match[3]
	if start < 0 {
		start, end = match[4], match[5]
	}
	script := text[start:end]
	for cursor := 0; cursor < len(script); {
		cursor += len(script[cursor:]) - len(strings.TrimLeft(script[cursor:], " \t\r\n;"))
		if cursor+2 >= len(script) || script[cursor] != 's' {
			return
		}
		delimiter := script[cursor+1]
		if !strings.ContainsRune("/|#@:!%", rune(delimiter)) {
			return
		}
		patternStart := cursor + 2
		patternEnd := partEnd(script, patternStart, delimiter)
		if patternEnd < 0 {
			return
		}
		replacementStart := patternEnd + 1
		replacementEnd := partEnd(script, replacementStart, delimiter)
		if replacementEnd < 0 {
			return
		}
		// Validate flags before changing any bytes: unsupported commands are opaque.
		flagsEnd := replacementEnd + 1
		for flagsEnd < len(script) && script[flagsEnd] != ';' && script[flagsEnd] != '\n' {
			if !strings.ContainsRune("gIp0123456789 \t\r", rune(script[flagsEnd])) {
				return
			}
			flagsEnd++
		}
		replacement := script[replacementStart:replacementEnd]
		if wildcardAssignment.MatchString(script[patternStart:patternEnd]) && (replacement == "" || isPlaceholderSecretValue(replacement)) {
			blank(view, start+patternStart, start+patternEnd)
			blank(view, start+replacementStart, start+replacementEnd)
			*spans = append(*spans, [2]int{start + patternStart, start + patternEnd}, [2]int{start + replacementStart, start + replacementEnd})
		}
		cursor = flagsEnd
	}
}

func partEnd(script string, start int, delimiter byte) int {
	for i := start; i < len(script); i++ {
		if script[i] == '\\' {
			i++
			continue
		}
		if script[i] == delimiter {
			return i
		}
	}
	return -1
}

func blank(view []byte, start, end int) {
	for i := start; i < end; i++ {
		view[i] = ' '
	}
}
