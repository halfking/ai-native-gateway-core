// Package modelname holds the single model-name identity judgment shared
// by the integrity detection chain (domains/streaming/integrity detector,
// the anthropic bridge side-channel, and the executor CheckSoftMismatch
// family): two model names denote the same model when their LAST
// '/'-segment matches case-insensitively.
//
// 2026-11-20 (owner ruling): vendor prefixes are display routing, not
// substitution. Providers require publisher-prefixed upstream ids
// ("z-ai/glm-5.3-flash", "minimaxai/minimax-m3.1-flash") — possibly
// multi-level ("a/b/model") — while the upstream response echoes the bare
// name. 211 production: 3721 real-time model_mismatch events in 14 days,
// 3691 of them (99.2%) last-segment-consistent; only 28 carried a genuinely
// different name. Comparing full strings turned the 模型不一致 list and the
// credibility verdict's passive identity channel into constant noise.
//
// The comparison set is "any trusted name", not a single name: the recorded
// outbound_model can be a display name (offer raw, e.g.
// "MiniMax-M3.1-Flash-Preview") while the id actually sent upstream is the
// client name — request_logs cross-check 2026-11-20 — and the OpenAI
// non-stream path rewrites the response ".model" to clientModel before the
// detector runs (executor_chat.go replaceModelInResponseBody). A reported
// name matching EITHER the outbound or the client identity is consistent;
// matching NEITHER is the substitution signal.
package modelname

import "strings"

// LastSegment returns the final '/'-segment of a model name with
// surrounding whitespace and trailing slashes removed ("z-ai/glm-5.3-flash"
// → "glm-5.3-flash", "a/b/model" → "model", "" / "/" → "").
func LastSegment(model string) string {
	s := strings.TrimSpace(model)
	s = strings.TrimRight(s, "/")
	if s == "" {
		return ""
	}
	if idx := strings.LastIndex(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
	return s
}

// Consistent reports whether the upstream-reported model name matches ANY
// of the trusted names (outbound id / client-requested name), comparing
// last '/'-segments case-insensitively. An empty reported name or an empty
// trusted set is never consistent — callers gate on emptiness themselves
// because "upstream reported nothing" is not evidence of consistency.
func Consistent(reported string, trusted ...string) bool {
	rep := LastSegment(reported)
	if rep == "" {
		return false
	}
	for _, t := range trusted {
		want := LastSegment(t)
		if want == "" {
			continue
		}
		if strings.EqualFold(rep, want) {
			return true
		}
	}
	return false
}
