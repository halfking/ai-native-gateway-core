// Package urlutil is a deprecated alias for internal/upstreamurl.
//
// All upstream API URL construction in llm-gateway-go MUST go through
// internal/upstreamurl. This package remains as a thin wrapper so that
// external code (and any pre-existing test files) referencing the old
// urlutil.ModelsURL / urlutil.ChatCompletionsURL /
// urlutil.ModelsURLCandidates symbols continues to compile, but the
// canonical implementation lives in upstreamurl.
package urlutil

import "github.com/kaixuan/llm-gateway-go/internal/upstreamurl"

// ModelsURL is a deprecated alias for upstreamurl.ModelsURL.
func ModelsURL(baseURL string) string {
	return upstreamurl.ModelsURL(baseURL)
}

// ChatCompletionsURL is a deprecated alias for upstreamurl.ChatCompletionsURL.
func ChatCompletionsURL(baseURL string) string {
	return upstreamurl.ChatCompletionsURL(baseURL)
}

// ModelsURLCandidates is a deprecated alias for upstreamurl.ModelsURLCandidates.
func ModelsURLCandidates(baseURL string) []string {
	return upstreamurl.ModelsURLCandidates(baseURL)
}
