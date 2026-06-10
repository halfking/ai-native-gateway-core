package relay

import (
	"encoding/json"
	"errors"
	"math"
)

var errNotFound = errors.New("key not found")

type UsageData struct {
	PromptTokens     *int
	CompletionTokens *int
	CacheReadTokens  *int
	CacheWriteTokens *int
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
