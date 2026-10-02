package streaming

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
)

// CNYToUSDFX converts native CNY upstream cost to USD for KPI aggregation.
// Matches pricing research docs (2026-06-12-cny-fix).
const CNYToUSDFX = 7.2

var errNotFound = errors.New("key not found")

type UsageData struct {
	PromptTokens     *int
	CompletionTokens *int
	CacheReadTokens  *int
	CacheWriteTokens *int
	ReasoningTokens  *int
	CacheMissTokens  *int
	ProviderTokens   *int
}

// Wave4-D4 (2026-09-22): the usage field-variant table — the single place
// mapping vendor wire names to gateway slots. The streaming extractor
// (ExtractUsageFromChunk) and the non-streaming one
// (handler.go extractTokensFromResponseBody) both resolve slots through
// lookupUsageInt, so a new vendor field name lands in exactly one list.
// Order matters: first match wins, matching the historical per-site
// precedence (direct Anthropic/OpenAI names before *_details fallbacks).
type usagePath []string

var (
	usagePromptPaths     = []usagePath{{"prompt_tokens"}, {"input_tokens"}}
	usageCompletionPaths = []usagePath{{"completion_tokens"}, {"output_tokens"}}
	usageCacheReadPaths  = []usagePath{
		{"cache_read_input_tokens"},
		{"cache_read_tokens"},
		{"prompt_tokens_details", "cached_tokens"},
		{"input_token_details", "cache_read"},
	}
	usageCacheWritePaths = []usagePath{
		{"cache_creation_input_tokens"},
		{"cache_write_tokens"},
		{"input_token_details", "cache_creation"},
	}
)

// lookupUsageInt resolves the first path in paths that yields a number.
// A path is either ["key"] (top-level usage field) or
// ["detail_object", "key"] (nested prompt_tokens_details /
// input_token_details family). Numbers are read as float64 then truncated
// so both integer and "12.0"-shaped JSON count.
func lookupUsageInt(usage map[string]json.RawMessage, paths []usagePath) (int, bool) {
	for _, path := range paths {
		switch len(path) {
		case 1:
			raw, ok := usage[path[0]]
			if !ok {
				continue
			}
			var f float64
			if err := json.Unmarshal(raw, &f); err != nil {
				continue
			}
			return int(f), true
		case 2:
			raw, ok := usage[path[0]]
			if !ok {
				continue
			}
			var detail map[string]json.RawMessage
			if err := json.Unmarshal(raw, &detail); err != nil {
				continue
			}
			raw, ok = detail[path[1]]
			if !ok {
				continue
			}
			var f float64
			if err := json.Unmarshal(raw, &f); err != nil {
				continue
			}
			return int(f), true
		}
	}
	return 0, false
}

func ExtractUsageFromChunk(payload string) UsageData {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return UsageData{}
	}

	usageRaw, ok := obj["usage"]
	if !ok {
		return UsageData{}
	}

	var usage map[string]json.RawMessage
	if err := json.Unmarshal(usageRaw, &usage); err != nil {
		return UsageData{}
	}

	result := UsageData{}

	// prompt/completion/cache slots resolve through the shared variant
	// table (Wave4-D4); Anthropic-native names are the fallback tails.
	if v, ok := lookupUsageInt(usage, usagePromptPaths); ok {
		result.PromptTokens = &v
	}
	if v, ok := lookupUsageInt(usage, usageCompletionPaths); ok {
		result.CompletionTokens = &v
	}
	if v, ok := lookupUsageInt(usage, usageCacheReadPaths); ok {
		result.CacheReadTokens = &v
	}
	if v, ok := lookupUsageInt(usage, usageCacheWritePaths); ok {
		result.CacheWriteTokens = &v
	}

	// Reasoning usage is reported by OpenAI-compatible providers under either
	// completion_tokens_details or a provider-specific top-level field.
	if detail, err := objVal(usage, "completion_tokens_details"); err == nil {
		if v, err := intValue(detail, "reasoning_tokens"); err == nil {
			result.ReasoningTokens = &v
		}
	}
	if result.ReasoningTokens == nil {
		for _, key := range []string{"reasoning_tokens", "reasoning_token_count"} {
			if v, err := intValue(usage, key); err == nil {
				result.ReasoningTokens = &v
				break
			}
		}
	}

	// DeepSeek/Doubao-compatible endpoints may expose cache hit/miss as
	// separate counters. Keep miss separate because it is normally billed at
	// the regular input rate rather than the cache-read rate.
	for _, key := range []string{"prompt_cache_miss_tokens", "cache_miss_tokens"} {
		if v, err := intValue(usage, key); err == nil {
			result.CacheMissTokens = &v
			break
		}
	}

	// Doubao Seed usage is provider billing evidence, not a generic token
	// replacement. Preserve it separately until the provider rate card maps it.
	for _, key := range []string{"seed_token_usage", "seed_tokens"} {
		if v, err := intValue(usage, key); err == nil {
			result.ProviderTokens = &v
			break
		}
	}

	// total_tokens fallback: if we have total but missing prompt/completion,
	// infer the missing side. R57 D4：与非流式 extractTokensFromResponseBody
	// 双向推断对称化——原条件 `(P==nil||C==nil) && P==nil` 塌缩成 P==nil 单
	// 向，completion-only usage（部分上游流式尾块只报 completion+total）永
	// 远推不出 prompt，计费相邻面流式/非流式口径不一致。
	if result.PromptTokens == nil || result.CompletionTokens == nil {
		if total, err := intValue(usage, "total_tokens"); err == nil && total > 0 {
			if result.PromptTokens == nil && result.CompletionTokens != nil && total > *result.CompletionTokens {
				pt := total - *result.CompletionTokens
				result.PromptTokens = &pt
			} else if result.CompletionTokens == nil && result.PromptTokens != nil && total > *result.PromptTokens {
				ct := total - *result.PromptTokens
				result.CompletionTokens = &ct
			}
		}
	}

	return result
}

// ExtractDoubaoUsageFromChunk extracts only fields that are valid for the
// official Doubao profile. The catalog guard prevents a volcengine-coding
// aggregate response from being interpreted as Doubao usage.
func ExtractDoubaoUsageFromChunk(payload string, catalogCode string) UsageData {
	if strings.ToLower(strings.TrimSpace(catalogCode)) != "doubao" {
		return UsageData{}
	}
	return ExtractUsageFromChunk(payload)
}

type CostInput struct {
	PromptTokens     *float64
	CompletionTokens *float64
	CacheReadTokens  *float64
	CacheWriteTokens *float64
	PriceIn          *float64
	PriceOut         *float64
	CacheReadPrice   *float64
	CacheWritePrice  *float64
}

func CalcCost(input CostInput) *float64 {
	if input.PromptTokens == nil && input.CompletionTokens == nil {
		return nil
	}

	priceIn := floatPtr(input.PriceIn, 0)
	priceOut := floatPtr(input.PriceOut, 0)
	if priceIn == 0 && priceOut == 0 {
		return nil
	}

	promptCount := floatPtr(input.PromptTokens, 0)
	cacheReadCount := floatPtr(input.CacheReadTokens, 0)
	cacheWriteCount := floatPtr(input.CacheWriteTokens, 0)

	promptCost := promptCount * priceIn

	if input.CacheReadPrice != nil && *input.CacheReadPrice > 0 && cacheReadCount > 0 {
		promptCost -= cacheReadCount * priceIn
		promptCost += cacheReadCount * *input.CacheReadPrice
	}

	if input.CacheWritePrice != nil && *input.CacheWritePrice > 0 && cacheWriteCount > 0 {
		promptCost -= cacheWriteCount * priceIn
		promptCost += cacheWriteCount * *input.CacheWritePrice
	}

	completionCount := floatPtr(input.CompletionTokens, 0)
	total := (promptCost + completionCount*priceOut) / 1_000_000.0

	if math.IsNaN(total) || math.IsInf(total, 0) {
		return nil
	}

	total = math.Round(total*1e8) / 1e8
	return &total
}

// CostPriceInput carries token usage and offer pricing for request-log cost fields.
type CostPriceInput struct {
	PromptTokens     *int
	CompletionTokens *int
	CacheReadTokens  *int
	CacheWriteTokens *int
	PriceIn          *float64
	PriceOut         *float64
	CacheReadPrice   *float64
	CacheWritePrice  *float64
	Currency         string
}

// AssignRequestCost fills cost_usd / cost_display / cost_currency for telemetry.
// USD offers write cost_usd only; non-USD writes native cost_display plus USD KPI
// via CNYToUSDFX (7.2). Returns all nil when pricing or tokens are insufficient.
func AssignRequestCost(in CostPriceInput) (costUSD, costDisplay *float64, costCurrency *string) {
	if in.PromptTokens == nil && in.CompletionTokens == nil {
		return nil, nil, nil
	}

	native := CalcCost(CostInput{
		PromptTokens:     intPtrToFloatPtr(in.PromptTokens),
		CompletionTokens: intPtrToFloatPtr(in.CompletionTokens),
		CacheReadTokens:  intPtrToFloatPtr(in.CacheReadTokens),
		CacheWriteTokens: intPtrToFloatPtr(in.CacheWriteTokens),
		PriceIn:          in.PriceIn,
		PriceOut:         in.PriceOut,
		CacheReadPrice:   in.CacheReadPrice,
		CacheWritePrice:  in.CacheWritePrice,
	})
	if native == nil {
		return nil, nil, nil
	}

	curr := strings.ToUpper(strings.TrimSpace(in.Currency))
	if curr == "" || curr == "USD" {
		return native, nil, nil
	}

	display := native
	currency := strings.TrimSpace(in.Currency)
	if currency == "" {
		currency = in.Currency
	}
	currencyCopy := currency
	usd := math.Round((*native/CNYToUSDFX)*1e8) / 1e8
	return &usd, display, &currencyCopy
}

func intPtrToFloatPtr(p *int) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}

func intValue(m map[string]json.RawMessage, key string) (int, error) {
	raw, ok := m[key]
	if !ok {
		return 0, errNotFound
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, err
	}
	return v, nil
}

func objVal(m map[string]json.RawMessage, key string) (map[string]json.RawMessage, error) {
	raw, ok := m[key]
	if !ok {
		return nil, errNotFound
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func floatPtr(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}
