package maas

import (
	"math"
	"testing"
)

func TestGlobalEffectiveMultimodal(t *testing.T) {
	st := Settings{
		BaseCreditsPer1M:         10000,
		BaseCreditsPer1MIn:       10000,
		BaseCreditsPer1MOut:      20000,
		BaseCreditsPer1MCacheIn:  1000,
		BaseCreditsPer1MCacheOut: 2000,
		GlobalDiscount:           0.8,
	}
	g := globalEffective(st)
	if g.In != 8000 || g.Out != 16000 || g.CacheIn != 800 || g.CacheOut != 1600 {
		t.Fatalf("global effective text rates wrong: %+v", g)
	}
	if g.Image != 8000 || g.Audio != 8000 || g.Video != 8000 {
		t.Fatalf("multimodal defaults should follow In×discount: %+v", g)
	}
}

func TestEffectiveModelRatesMultimodalFallback(t *testing.T) {
	st := Settings{BaseCreditsPer1M: 10000, GlobalDiscount: 1.0}
	global := globalEffective(st)
	stored := storedModelRates{}
	got := effectiveModelRates(stored, global)
	if got.Image != global.In || got.Audio != global.In || got.Video != global.In {
		t.Fatalf("multimodal should fall back to input rate: %+v", got)
	}
}

func TestEffectiveModelRatesMultimodalManual(t *testing.T) {
	st := Settings{BaseCreditsPer1M: 10000, GlobalDiscount: 1.0}
	global := globalEffective(st)
	imageVal := int64(12345)
	audioVal := int64(22345)
	stored := storedModelRates{
		Image:       &imageVal,
		Audio:       &audioVal,
		ManualImage: true,
		ManualAudio: true,
	}
	got := effectiveModelRates(stored, global)
	if got.Image != imageVal || got.Audio != audioVal {
		t.Fatalf("multimodal manual not honored: %+v", got)
	}
	if got.Video != global.In {
		t.Fatalf("video without manual flag should fall back: %+v", got)
	}
}

func TestStoredIsManualMultimodal(t *testing.T) {
	if !storedIsManual(storedModelRates{ManualImage: true}) {
		t.Fatalf("manual_image should mark custom")
	}
	if storedIsManual(storedModelRates{}) {
		t.Fatalf("empty stored should not be manual")
	}
}

func TestModelRateUpsertAnyManual(t *testing.T) {
	base := ModelRateUpsert{}
	if base.AnyManual() {
		t.Fatalf("zero-valued upsert should not be manual")
	}
	im := ModelRateUpsert{ManualImage: true}
	if !im.AnyManual() {
		t.Fatalf("manual_image alone should be enough")
	}
	cache := ModelRateUpsert{ManualCacheOut: true}
	if !cache.AnyManual() {
		t.Fatalf("manual_cache_out alone should be enough")
	}
}

func TestCalcCreditsMultimodal(t *testing.T) {
	rates := ModelRateValues{In: 10000, Out: 20000, CacheIn: 1000, CacheOut: 2000, Image: 30000, Audio: 15000, Video: 50000}

	t.Run("text only", func(t *testing.T) {
		amount := CalcCreditsMultimodal(TokenUsage{
			PromptTokens:     1_000_000,
			CompletionTokens: 1_000_000,
		}, rates)
		// 1M * 10000 + 1M * 20000 = 30000 credits
		if amount != 30000 {
			t.Fatalf("got %d want 30000", amount)
		}
	})

	t.Run("adds image tokens", func(t *testing.T) {
		amount := CalcCreditsMultimodal(TokenUsage{
			PromptTokens: 1_000_000,
			ImageTokens:  1_000_000,
		}, rates)
		// 1M * 10000 (text) + 1M * 30000 (image) = 40000
		if amount != 40000 {
			t.Fatalf("got %d want 40000", amount)
		}
	})

	t.Run("all buckets combined", func(t *testing.T) {
		amount := CalcCreditsMultimodal(TokenUsage{
			PromptTokens:     500_000,
			CompletionTokens: 500_000,
			CacheReadTokens:  1_000_000,
			CacheWriteTokens: 1_000_000,
			ImageTokens:      250_000,
			AudioTokens:      250_000,
			VideoTokens:      250_000,
		}, rates)
		// 0.5M*10000 + 0.5M*20000 + 1M*1000 + 1M*2000 + 0.25M*30000 + 0.25M*15000 + 0.25M*50000
		// = 5000 + 10000 + 1000 + 2000 + 7500 + 3750 + 12500 = 41750
		if amount != 41750 {
			t.Fatalf("got %d want 41750", amount)
		}
	})

	t.Run("missing rate falls back to input rate", func(t *testing.T) {
		noImage := ModelRateValues{In: 10000, Out: 20000}
		amount := CalcCreditsMultimodal(TokenUsage{
			ImageTokens: 1_000_000,
		}, noImage)
		// imageRate falls back to In, so 1M * 10000 = 10000
		if amount != 10000 {
			t.Fatalf("got %d want 10000", amount)
		}
	})

	t.Run("zero usage returns zero", func(t *testing.T) {
		if amount := CalcCreditsMultimodal(TokenUsage{}, rates); amount != 0 {
			t.Fatalf("expected zero, got %d", amount)
		}
	})

	t.Run("legacy wrapper still works", func(t *testing.T) {
		// CalcCredits is the 4-tuple wrapper — its result must
		// match CalcCreditsMultimodal for the same inputs.
		amountLegacy := CalcCredits(1_000_000, 1_000_000, 1_000_000, 1_000_000, rates)
		amountNew := CalcCreditsMultimodal(TokenUsage{
			PromptTokens:     1_000_000,
			CompletionTokens: 1_000_000,
			CacheReadTokens:  1_000_000,
			CacheWriteTokens: 1_000_000,
		}, rates)
		if amountLegacy != amountNew {
			t.Fatalf("legacy %d != new %d", amountLegacy, amountNew)
		}
		// 1M * (10000 + 20000 + 1000 + 2000) = 33000
		if amountLegacy != 33000 {
			t.Fatalf("legacy total: got %d want 33000", amountLegacy)
		}
	})

	t.Run("rounds up not down", func(t *testing.T) {
		// ceil(0.4 / 1.0) = 1 to keep semantics identical to legacy
		rate := ModelRateValues{In: 1}
		amount := CalcCreditsMultimodal(TokenUsage{PromptTokens: 400_000}, rate)
		if amount != int64(math.Ceil(0.4)) {
			t.Fatalf("expected ceil(0.4)=1, got %d", amount)
		}
	})
}
