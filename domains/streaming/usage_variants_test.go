package streaming

import (
	"testing"
)

// Wave4-D4 (2026-09-22) regression pins for the unified usage field-variant
// table. Both the streaming extractor (ExtractUsageFromChunk) and the
// non-streaming one (extractTokensFromResponseBody) resolve vendor field
// names through the same lookupUsageInt table, so a new vendor field name
// lands in exactly one list. MiniMax / GLM / Doubao shapes are covered.

func TestExtractUsageFromChunk_VendorVariants(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    UsageData
	}{
		{
			name:    "OpenAI canonical",
			payload: `{"usage":{"prompt_tokens":100,"completion_tokens":20}}`,
			want:    UsageData{PromptTokens: usageVariantIntPtr(100), CompletionTokens: usageVariantIntPtr(20)},
		},
		{
			name:    "Anthropic native input/output_tokens",
			payload: `{"usage":{"input_tokens":50,"output_tokens":7}}`,
			want:    UsageData{PromptTokens: usageVariantIntPtr(50), CompletionTokens: usageVariantIntPtr(7)},
		},
		{
			name:    "Anthropic cache creation/read names",
			payload: `{"usage":{"input_tokens":10,"cache_read_input_tokens":30,"cache_creation_input_tokens":5}}`,
			want: UsageData{PromptTokens: usageVariantIntPtr(10), CacheReadTokens: usageVariantIntPtr(30), CacheWriteTokens: usageVariantIntPtr(5)},
		},
		{
			name:    "OpenAI prompt_tokens_details.cached_tokens",
			payload: `{"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":64}}}`,
			want:    UsageData{PromptTokens: usageVariantIntPtr(100), CompletionTokens: usageVariantIntPtr(10), CacheReadTokens: usageVariantIntPtr(64)},
		},
		{
			name:    "OpenAI input_token_details cache_read/cache_creation",
			payload: `{"usage":{"prompt_tokens":100,"completion_tokens":10,"input_token_details":{"cache_read":21,"cache_creation":9}}}`,
			want:    UsageData{PromptTokens: usageVariantIntPtr(100), CompletionTokens: usageVariantIntPtr(10), CacheReadTokens: usageVariantIntPtr(21), CacheWriteTokens: usageVariantIntPtr(9)},
		},
		{
			name:    "Doubao prompt_cache_miss_tokens",
			payload: `{"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_cache_miss_tokens":88}}`,
			want:    UsageData{PromptTokens: usageVariantIntPtr(100), CompletionTokens: usageVariantIntPtr(10), CacheMissTokens: usageVariantIntPtr(88)},
		},
		{
			name:    "reasoning via completion_tokens_details",
			payload: `{"usage":{"prompt_tokens":10,"completion_tokens":30,"completion_tokens_details":{"reasoning_tokens":25}}}`,
			want:    UsageData{PromptTokens: usageVariantIntPtr(10), CompletionTokens: usageVariantIntPtr(30), ReasoningTokens: usageVariantIntPtr(25)},
		},
		{
			name:    "total_tokens fallback fills prompt",
			payload: `{"usage":{"total_tokens":120,"completion_tokens":20}}`,
			want:    UsageData{CompletionTokens: usageVariantIntPtr(20), PromptTokens: usageVariantIntPtr(100)},
		},
		{
			name:    "no usage object",
			payload: `{"choices":[]}`,
			want:    UsageData{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractUsageFromChunk(tt.payload)
			assertUsageEqual(t, got, tt.want)
		})
	}
}

// TestExtractTokensFromResponseBody_VendorVariants pins the non-stream
// extractor on the same table plus its MiniMax top-level-usage fallback and
// two-directional total inference.
func TestExtractTokensFromResponseBody_VendorVariants(t *testing.T) {
	tests := []struct {
		name             string
		body             string
		pt, ct, crt, cwt int
	}{
		{"canonical chat body", `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20}}`, 100, 20, 0, 0},
		{"details cache_read", `{"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_tokens_details":{"cached_tokens":64}}}`, 100, 10, 64, 0},
		{"input_token_details cache_write", `{"usage":{"prompt_tokens":100,"completion_tokens":10,"input_token_details":{"cache_read":21,"cache_creation":9}}}`, 100, 10, 21, 9},
		{"anthropic names", `{"usage":{"input_tokens":50,"output_tokens":7}}`, 50, 7, 0, 0},
		{"minimax top-level usage", `{"base_resp":{"status_code":0},"model":"abab","input_tokens":11,"output_tokens":3}`, 11, 3, 0, 0},
		{"total fills missing prompt", `{"usage":{"total_tokens":120,"completion_tokens":20}}`, 100, 20, 0, 0},
		{"total fills missing completion", `{"usage":{"total_tokens":120,"prompt_tokens":100}}`, 100, 20, 0, 0},
		{"empty body", ``, 0, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pt, ct, crt, cwt := extractTokensFromResponseBody([]byte(tt.body))
			if pt != tt.pt || ct != tt.ct || crt != tt.crt || cwt != tt.cwt {
				t.Fatalf("extractTokensFromResponseBody(%s) = (%d,%d,%d,%d), want (%d,%d,%d,%d)",
					tt.body, pt, ct, crt, cwt, tt.pt, tt.ct, tt.crt, tt.cwt)
			}
		})
	}
}

// TestUsageExtractorsAgreeOnSameUsage pins parity: the same usage object fed
// through the streaming extractor and the non-streaming extractor yields the
// same four core slots.
func TestUsageExtractorsAgreeOnSameUsage(t *testing.T) {
	usage := `{"prompt_tokens":100,"completion_tokens":20,"cache_read_input_tokens":30,"cache_creation_input_tokens":5}`
	bodies := []string{
		`{"usage":` + usage + `}`,
		`{"id":"x","usage":` + usage + `,"choices":[]}`,
	}
	for _, body := range bodies {
		stream := ExtractUsageFromChunk(body)
		pt, ct, crt, cwt := extractTokensFromResponseBody([]byte(body))
		if stream.PromptTokens == nil || *stream.PromptTokens != pt ||
			stream.CompletionTokens == nil || *stream.CompletionTokens != ct ||
			stream.CacheReadTokens == nil || *stream.CacheReadTokens != crt ||
			stream.CacheWriteTokens == nil || *stream.CacheWriteTokens != cwt {
			t.Fatalf("extractors disagree on %s: stream=%+v non-stream=(%d,%d,%d,%d)",
				body, stream, pt, ct, crt, cwt)
		}
	}
}

func usageVariantIntPtr(v int) *int { return &v }

func assertUsageEqual(t *testing.T, got, want UsageData) {
	t.Helper()
	cmp := func(name string, g, w *int) {
		if (g == nil) != (w == nil) || (g != nil && w != nil && *g != *w) {
			t.Fatalf("%s = %v, want %v", name, g, w)
		}
	}
	cmp("PromptTokens", got.PromptTokens, want.PromptTokens)
	cmp("CompletionTokens", got.CompletionTokens, want.CompletionTokens)
	cmp("CacheReadTokens", got.CacheReadTokens, want.CacheReadTokens)
	cmp("CacheWriteTokens", got.CacheWriteTokens, want.CacheWriteTokens)
	cmp("ReasoningTokens", got.ReasoningTokens, want.ReasoningTokens)
	cmp("CacheMissTokens", got.CacheMissTokens, want.CacheMissTokens)
}
