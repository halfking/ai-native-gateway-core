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

	// prompt_tokens / input_tokens (Anthropic native)
	if v, err := intValue(usage, "prompt_tokens"); err == nil {
		result.PromptTokens = &v
	} else if v, err := intValue(usage, "input_tokens"); err == nil {
		result.PromptTokens = &v
	}
	// completion_tokens / output_tokens (Anthropic native)
	if v, err := intValue(usage, "completion_tokens"); err == nil {
		result.CompletionTokens = &v
	} else if v, err := intValue(usage, "output_tokens"); err == nil {
		result.CompletionTokens = &v
	}

	// cache_read: try 4 field name variants
	if v, err := intValue(usage, "cache_read_input_tokens"); err == nil {
		result.CacheReadTokens = &v
	} else if v, err := intValue(usage, "cache_read_tokens"); err == nil {
		result.CacheReadTokens = &v
	} else {
		if detail, err := objVal(usage, "prompt_tokens_details"); err == nil {
			if v, err := intValue(detail, "cached_tokens"); err == nil {
				result.CacheReadTokens = &v
			}
		} else if detail, err := objVal(usage, "input_token_details"); err == nil {
			if v, err := intValue(detail, "cache_read"); err == nil {
				result.CacheReadTokens = &v
			}
		}
	}
	// cache_write: try 3 field name variants
	if v, err := intValue(usage, "cache_creation_input_tokens"); err == nil {
		result.CacheWriteTokens = &v
	} else if v, err := intValue(usage, "cache_write_tokens"); err == nil {
		result.CacheWriteTokens = &v
	} else {
		if detail, err := objVal(usage, "input_token_details"); err == nil {
			if v, err := intValue(detail, "cache_creation"); err == nil {
				result.CacheWriteTokens = &v
			}
		}
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

	// total_tokens fallback: if we have total but missing prompt/completion, infer them
	if (result.PromptTokens == nil || result.CompletionTokens == nil) && result.PromptTokens == nil {
		if total, err := intValue(usage, "total_tokens"); err == nil && total > 0 {
			if result.CompletionTokens != nil {
				pt := total - *result.CompletionTokens
				if pt >= 0 {
					result.PromptTokens = &pt
				}
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

func ExtractFinishReason(payload string) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &obj); err != nil {
		return ""
	}

	choicesRaw, ok := obj["choices"]
	if !ok {
		return ""
	}

	var choices []map[string]json.RawMessage
	if err := json.Unmarshal(choicesRaw, &choices); err != nil {
		return ""
	}

	for _, choice := range choices {
		if frRaw, ok := choice["finish_reason"]; ok {
			var fr string
			if err := json.Unmarshal(frRaw, &fr); err == nil && fr != "" && fr != "null" {
				return fr
			}
		}
	}
	return ""
}

func InjectStreamOptions(body []byte) []byte {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		return body
	}

	var streamOpts map[string]json.RawMessage
	if raw, ok := obj["stream_options"]; ok {
		if err := json.Unmarshal(raw, &streamOpts); err != nil {
			streamOpts = make(map[string]json.RawMessage)
		}
	} else {
		streamOpts = make(map[string]json.RawMessage)
	}

	streamOpts["include_usage"], _ = json.Marshal(true)
	obj["stream_options"] = relayMustMarshal(streamOpts)

	result, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return result
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

func relayMustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func floatPtr(p *float64, def float64) float64 {
	if p != nil {
		return *p
	}
	return def
}
