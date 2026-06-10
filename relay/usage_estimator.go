package relay

import (
	"encoding/json"
	"unicode"
)

// UsageSource values stored in request_logs.usage_source.
const (
	UsageSourceLLM       = "llm"       // From upstream response usage block
	UsageSourceEstimated = "estimated" // Computed locally from request/response text
	UsageSourceNone      = ""          // No usage data and not estimable
)

// estimatePromptTokens computes an approximate prompt token count from the
// OpenAI-style chat-completions request body. Used as a fallback when the
// upstream provider (e.g. minimax) does not return a `usage` block.
//
// Heuristic (intentionally conservative — slight overestimate is safer for
// cost display than underestimate):
//   - CJK ideographs: 1 char ≈ 1 token
//   - Other chars:    4 chars ≈ 1 token
//   - Per-word overhead: +0.3 tokens (BPE boundary markers)
//   - Per-message overhead: +4 tokens (role + formatting)
//   - System prompt overhead: +2 tokens
//
// NOTE: This is an ESTIMATE, not a BPE-exact count. Rows written with these
// values are tagged with usage_source='estimated' in request_logs so the UI
// can mark them distinctly from upstream-reported usage.
func estimatePromptTokens(requestBody []byte) int {
	if len(requestBody) == 0 {
		return 0
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Name    string          `json:"name,omitempty"`
		} `json:"messages"`
		System json.RawMessage `json:"system,omitempty"`
	}
	if err := json.Unmarshal(requestBody, &req); err != nil {
		// Last-resort: estimate from entire body text
		return estimateTextTokens(string(requestBody))
	}
	total := 0
	if len(req.Messages) > 0 {
		total += 2
	}
	for _, msg := range req.Messages {
		total += 4
		if msg.Name != "" {
			total += estimateTextTokens(msg.Name)
		}
		total += extractContentTokens(msg.Content)
	}
	if len(req.System) > 0 {
		total += 2 + extractContentTokens(req.System)
	}
	return total
}

// estimateCompletionTokens computes an approximate completion token count
// from a non-streaming OpenAI-style chat-completions response body.
func estimateCompletionTokens(responseBody []byte) int {
	if len(responseBody) == 0 {
		return 0
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
			Text  string `json:"text"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &resp); err != nil {
		return estimateTextTokens(string(responseBody))
	}
	total := 0
	for _, choice := range resp.Choices {
		if len(choice.Message.Content) > 0 {
			total += extractContentTokens(choice.Message.Content)
		} else if choice.Text != "" {
			total += estimateTextTokens(choice.Text)
		} else if choice.Delta.Content != "" {
			total += estimateTextTokens(choice.Delta.Content)
		}
	}
	return total
}

// extractContentTokens handles OpenAI content that is either a string or an
// array of typed parts (text/image_url/etc).
func extractContentTokens(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return estimateTextTokens(s)
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		total := 0
		for _, p := range parts {
			if p.Text != "" {
				total += estimateTextTokens(p.Text)
			}
			if p.ImageURL.URL != "" {
				// Image tokens: ~765 for low-res, varies for high-res.
				total += 765
			}
		}
		return total
	}
	return estimateTextTokens(string(raw))
}

// estimateTextTokens applies the CJK + word heuristic to a free-form string.
func estimateTextTokens(text string) int {
	if text == "" {
		return 0
	}
	var cjk, other, words int
	inWord := false
	for _, r := range text {
		if isCJK(r) {
			cjk++
			if inWord {
				inWord = false
			}
			continue
		}
		other++
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			if inWord {
				inWord = false
			}
		} else if !inWord {
			words++
			inWord = true
		}
	}
	cjkTokens := cjk
	otherTokens := (other + 3) / 4
	wordOverhead := (words*3 + 9) / 10
	return cjkTokens + otherTokens + wordOverhead
}

// isCJK returns true for Han, Kana, and Hangul code points.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF:
		return true
	case r >= 0x3400 && r <= 0x4DBF:
		return true
	case r >= 0x20000 && r <= 0x2A6DF:
		return true
	case r >= 0xF900 && r <= 0xFAFF:
		return true
	case r >= 0x2F800 && r <= 0x2FA1F:
		return true
	case r >= 0x3040 && r <= 0x309F:
		return true
	case r >= 0x30A0 && r <= 0x30FF:
		return true
	case r >= 0xAC00 && r <= 0xD7AF:
		return true
	case r >= 0xFF65 && r <= 0xFF9F:
		return true
	}
	return false
}
