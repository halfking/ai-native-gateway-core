
package relay

import (
	"encoding/json"
	"testing"
)

func TestExtractUsageFromChunk(t *testing.T) {
	t.Run("basic_usage", func(t *testing.T) {
		payload := `{"id":"chatcmpl-1","model":"gpt-4","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20}}`
		usage := ExtractUsageFromChunk(payload)
		if usage.PromptTokens == nil || *usage.PromptTokens != 10 {
			t.Errorf("expected prompt_tokens=10, got %v", usage.PromptTokens)
		}
		if usage.CompletionTokens == nil || *usage.CompletionTokens != 20 {
			t.Errorf("expected completion_tokens=20, got %v", usage.CompletionTokens)
		}
	})

	t.Run("cache_read_tokens", func(t *testing.T) {
		payload := `{"usage":{"prompt_tokens":100,"completion_tokens":50,"cache_read_input_tokens":80}}`
		usage := ExtractUsageFromChunk(payload)
		if usage.CacheReadTokens == nil || *usage.CacheReadTokens != 80 {
			t.Errorf("expected cache_read_tokens=80, got %v", usage.CacheReadTokens)
		}
	})

	t.Run("cache_read_alt_names", func(t *testing.T) {
		payload := `{"usage":{"prompt_tokens":100,"cache_read_tokens":60}}`
		usage := ExtractUsageFromChunk(payload)
		if usage.CacheReadTokens == nil || *usage.CacheReadTokens != 60 {
			t.Errorf("expected cache_read_tokens=60, got %v", usage.CacheReadTokens)
		}
	})

	t.Run("cache_read_prompt_tokens_details", func(t *testing.T) {
		payload := `{"usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":40}}}`
		usage := ExtractUsageFromChunk(payload)
		if usage.CacheReadTokens == nil || *usage.CacheReadTokens != 40 {
			t.Errorf("expected cache_read_tokens=40, got %v", usage.CacheReadTokens)
		}
	})

	t.Run("cache_creation_input_tokens", func(t *testing.T) {
		payload := `{"usage":{"prompt_tokens":100,"cache_creation_input_tokens":30}}`
		usage := ExtractUsageFromChunk(payload)
		if usage.CacheWriteTokens == nil || *usage.CacheWriteTokens != 30 {
			t.Errorf("expected cache_write_tokens=30, got %v", usage.CacheWriteTokens)
		}
	})

	t.Run("no_usage", func(t *testing.T) {
		payload := `{"id":"chatcmpl-1","choices":[]}`
		usage := ExtractUsageFromChunk(payload)
		if usage.PromptTokens != nil {
			t.Errorf("expected nil prompt_tokens, got %v", usage.PromptTokens)
		}
	})

	t.Run("invalid_json", func(t *testing.T) {
		usage := ExtractUsageFromChunk("not json")
		if usage.PromptTokens != nil {
			t.Errorf("expected nil for invalid json")
		}
	})
}

func TestExtractFinishReason(t *testing.T) {
	t.Run("stop", func(t *testing.T) {
		payload := `{"choices":[{"finish_reason":"stop"}]}`
		fr := ExtractFinishReason(payload)
		if fr != "stop" {
			t.Errorf("expected 'stop', got '%s'", fr)
		}
	})

	t.Run("null_finish_reason", func(t *testing.T) {
		payload := `{"choices":[{"finish_reason":null}]}`
		fr := ExtractFinishReason(payload)
		if fr != "" {
			t.Errorf("expected empty, got '%s'", fr)
		}
	})

	t.Run("no_choices", func(t *testing.T) {
		payload := `{"id":"1"}`
		fr := ExtractFinishReason(payload)
		if fr != "" {
			t.Errorf("expected empty, got '%s'", fr)
		}
	})
}

func TestInjectStreamOptions(t *testing.T) {
	t.Run("no_existing_options", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4","stream":true}`)
		result := InjectStreamOptions(body)

		var obj map[string]any
		if err := json.Unmarshal(result, &obj); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		opts, ok := obj["stream_options"].(map[string]any)
		if !ok {
			t.Fatal("stream_options not found or not object")
		}
		if opts["include_usage"] != true {
			t.Errorf("expected include_usage=true, got %v", opts["include_usage"])
		}
	})

	t.Run("existing_options", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4","stream":true,"stream_options":{"include_usage":false}}`)
		result := InjectStreamOptions(body)

		var obj map[string]any
		if err := json.Unmarshal(result, &obj); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
		opts := obj["stream_options"].(map[string]any)
		if opts["include_usage"] != true {
			t.Errorf("expected include_usage to be overridden to true")
		}
	})
}

func TestCalcCost(t *testing.T) {
	t.Run("basic_cost", func(t *testing.T) {
		pt := float64(1000)
		ct := float64(500)
		priceIn := float64(30)
		priceOut := float64(60)
		cost := CalcCost(CostInput{
			PromptTokens:     &pt,
			CompletionTokens: &ct,
			PriceIn:          &priceIn,
			PriceOut:         &priceOut,
		})
		if cost == nil {
			t.Fatal("cost is nil")
		}
		expected := (1000*30 + 500*60) / 1_000_000.0
		if *cost != expected {
			t.Errorf("expected %f, got %f", expected, *cost)
		}
	})

	t.Run("nil_tokens", func(t *testing.T) {
		priceIn := float64(30)
		priceOut := float64(60)
		cost := CalcCost(CostInput{
			PriceIn:  &priceIn,
			PriceOut: &priceOut,
		})
		if cost != nil {
			t.Errorf("expected nil cost for nil tokens, got %v", cost)
		}
	})

	t.Run("zero_prices", func(t *testing.T) {
		pt := float64(1000)
		ct := float64(500)
		cost := CalcCost(CostInput{
			PromptTokens:     &pt,
			CompletionTokens: &ct,
		})
		if cost != nil {
			t.Errorf("expected nil for zero prices, got %v", cost)
		}
	})

	t.Run("cache_aware_pricing", func(t *testing.T) {
		pt := float64(1000)
		ct := float64(500)
		cacheRead := float64(800)
		priceIn := float64(30)
		priceOut := float64(60)
		cacheReadPrice := float64(3)
		cost := CalcCost(CostInput{
			PromptTokens:     &pt,
			CompletionTokens: &ct,
			CacheReadTokens:  &cacheRead,
			PriceIn:          &priceIn,
			PriceOut:         &priceOut,
			CacheReadPrice:   &cacheReadPrice,
		})
		if cost == nil {
			t.Fatal("cost is nil")
		}
		expected := ((1000*30 - 800*30 + 800*3) + 500*60) / 1_000_000.0
		if *cost != expected {
			t.Errorf("expected %f, got %f", expected, *cost)
		}
	})
}
