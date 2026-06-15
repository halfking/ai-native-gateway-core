package memora

import (
	"regexp"
	"strings"
	"unicode"
)

// PreviewTurn is one request_log row used for session extraction.
type PreviewTurn struct {
	Direction        string // "user" | "assistant"
	PromptPreview    string
	ResponsePreview  string
}

// ExtractStats counts extraction outcomes.
type ExtractStats struct {
	Candidates        []string
	SkippedNoise      int
	SkippedDuplicate  int
}

var (
	reCodeFence     = regexp.MustCompile(`(?s)^\s*` + "```" + `[\w]*\s*$`)
	reToolJSON      = regexp.MustCompile(`"(tool_call|function_call|tool_use)"\s*:`)
	reSysPrompt     = regexp.MustCompile(`(?i)(you are |follow all |<user_query>|agents\.md|system_reminder)`)
	reMetaLine      = regexp.MustCompile(`(?i)^(pid:|cwd:|exit_code:|---\s*$|last_command:)`)
	reHTMLEntity    = regexp.MustCompile(`&[a-z]+;`)
)

const minUsefulLen = 24

// ExtractFromPreviews pulls factual snippets from session previews,
// filtering format noise and deduplicating against existing Memora facts.
func ExtractFromPreviews(turns []PreviewTurn, existingFacts []string, includeResponses bool) ExtractStats {
	var st ExtractStats
	seen := make(map[string]struct{})
	for _, f := range existingFacts {
		n := normalizeForDedup(f)
		if n != "" {
			seen[n] = struct{}{}
		}
	}

	for _, t := range turns {
		if t.Direction == "user" || t.Direction == "" {
			if txt := strings.TrimSpace(t.PromptPreview); txt != "" {
				harvest(&st, seen, txt)
			}
		}
		if includeResponses {
			if txt := strings.TrimSpace(t.ResponsePreview); txt != "" {
				harvest(&st, seen, txt)
			}
		}
	}
	return st
}

func harvest(st *ExtractStats, seen map[string]struct{}, raw string) {
	for _, chunk := range splitChunks(raw) {
		chunk = strings.TrimSpace(chunk)
		if chunk == "" {
			continue
		}
		if isNoise(chunk) {
			st.SkippedNoise++
			continue
		}
		key := normalizeForDedup(chunk)
		if key == "" {
			st.SkippedNoise++
			continue
		}
		if _, ok := seen[key]; ok {
			st.SkippedDuplicate++
			continue
		}
		if overlapsExisting(key, seen) {
			st.SkippedDuplicate++
			continue
		}
		seen[key] = struct{}{}
		st.Candidates = append(st.Candidates, chunk)
	}
}

func splitChunks(text string) []string {
	// Paragraph breaks first; long single blocks stay intact.
	parts := strings.Split(text, "\n\n")
	if len(parts) == 1 {
		return []string{text}
	}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func isNoise(s string) bool {
	if len([]rune(s)) < minUsefulLen {
		return true
	}
	if reCodeFence.MatchString(s) {
		return true
	}
	if reToolJSON.MatchString(s) {
		return true
	}
	if reSysPrompt.MatchString(s) {
		return true
	}
	if reHTMLEntity.MatchString(s) && strings.Count(s, "&") > 2 {
		return true
	}
	lines := strings.Split(s, "\n")
	metaLines := 0
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if reMetaLine.MatchString(ln) {
			metaLines++
		}
		if strings.HasPrefix(ln, "```") {
			return true
		}
	}
	if len(lines) > 2 && metaLines*2 >= len(lines) {
		return true
	}
	// Mostly non-letter (tool dumps, stack traces without prose).
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if letters < minUsefulLen/2 {
		return true
	}
	return false
}

func normalizeForDedup(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func overlapsExisting(key string, seen map[string]struct{}) bool {
	for existing := range seen {
		if len(existing) < 12 {
			continue
		}
		if strings.Contains(key, existing) || strings.Contains(existing, key) {
			return true
		}
	}
	return false
}
