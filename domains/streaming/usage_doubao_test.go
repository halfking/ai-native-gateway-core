package streaming

import "testing"

func TestExtractDoubaoUsageFromChunk(t *testing.T) {
	payload := `{
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 40,
			"prompt_tokens_details": {"cached_tokens": 25},
			"completion_tokens_details": {"reasoning_tokens": 12},
			"prompt_cache_miss_tokens": 75,
			"seed_token_usage": 140
		}
	}`

	got := ExtractDoubaoUsageFromChunk(payload, "doubao")
	assertUsageInt(t, "prompt", got.PromptTokens, 100)
	assertUsageInt(t, "completion", got.CompletionTokens, 40)
	assertUsageInt(t, "cache read", got.CacheReadTokens, 25)
	assertUsageInt(t, "reasoning", got.ReasoningTokens, 12)
	assertUsageInt(t, "cache miss", got.CacheMissTokens, 75)
	assertUsageInt(t, "provider", got.ProviderTokens, 140)
}

func TestExtractDoubaoUsageFromChunkRejectsAggregateCatalog(t *testing.T) {
	payload := `{"usage":{"prompt_tokens":100,"seed_token_usage":100}}`
	got := ExtractDoubaoUsageFromChunk(payload, "volcengine-coding")
	if got.PromptTokens != nil || got.ProviderTokens != nil {
		t.Fatalf("aggregate catalog must not use Doubao extractor: %+v", got)
	}
}

func assertUsageInt(t *testing.T, name string, got *int, want int) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s usage is nil, want %d", name, want)
	}
	if *got != want {
		t.Fatalf("%s usage = %d, want %d", name, *got, want)
	}
}
