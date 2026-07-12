package maas

import "math"

// modelValuesFromBase promotes a global base rate set into per-model rate
// values with no manual overrides (manual_* flags remain false / zero).
func modelValuesFromBase(b BaseRateSet) ModelRateValues {
	return ModelRateValues{In: b.In, Out: b.Out, CacheIn: b.CacheIn, CacheOut: b.CacheOut, Image: b.Image, Audio: b.Audio, Video: b.Video}
}

// BaseRateSet is the global default credits per 1M tokens (before discount).
type BaseRateSet struct {
	In       int64
	Out      int64
	CacheIn  int64
	CacheOut int64
	// Image / Audio / Video tokens are model-wide multimodal defaults; only
	// used by IR.Usage-level multimodal counters when canonical model
	// carries that modality. nil / 0 falls back to the matching input rate
	// (so simple text models are unaffected).
	Image int64
	Audio int64
	Video int64
	// Discount applies to all rates (text + multimodal).
	Discount float64
}

func normalizeDiscount(d float64) float64 {
	if d <= 0 || d > 1 {
		return 1
	}
	return d
}

func applyDiscount(base int64, discount float64) int64 {
	if base <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(base) * normalizeDiscount(discount)))
}

func globalEffective(st Settings) BaseRateSet {
	baseIn := st.BaseCreditsPer1MIn
	if baseIn <= 0 {
		baseIn = st.BaseCreditsPer1M
	}
	if baseIn <= 0 {
		baseIn = 10000
	}
	out := st.BaseCreditsPer1MOut
	if out <= 0 {
		out = baseIn
	}
	cacheIn := st.BaseCreditsPer1MCacheIn
	if cacheIn <= 0 {
		cacheIn = baseIn
	}
	cacheOut := st.BaseCreditsPer1MCacheOut
	if cacheOut <= 0 {
		cacheOut = baseIn
	}
	disc := normalizeDiscount(st.GlobalDiscount)
	return BaseRateSet{
		In:       applyDiscount(baseIn, disc),
		Out:      applyDiscount(out, disc),
		CacheIn:  applyDiscount(cacheIn, disc),
		CacheOut: applyDiscount(cacheOut, disc),
		Image:    applyDiscount(baseIn, disc),
		Audio:    applyDiscount(baseIn, disc),
		Video:    applyDiscount(baseIn, disc),
		Discount: disc,
	}
}

// ModelRateValues holds effective billing rates for one model.
type ModelRateValues struct {
	In       int64
	Out      int64
	CacheIn  int64
	CacheOut int64
	Image    int64
	Audio    int64
	Video    int64
}

type storedModelRates struct {
	In, Out, CacheIn, CacheOut    *int64
	Image, Audio, Video           *int64
	ManualIn, ManualOut           bool
	ManualCacheIn, ManualCacheOut bool
	ManualImage                   bool
	ManualAudio                   bool
	ManualVideo                   bool
}

func effectiveModelRates(stored storedModelRates, global BaseRateSet) ModelRateValues {
	pick := func(manual bool, val *int64, fallback int64) int64 {
		if manual && val != nil && *val > 0 {
			return *val
		}
		return fallback
	}
	return ModelRateValues{
		In:       pick(stored.ManualIn, stored.In, global.In),
		Out:      pick(stored.ManualOut, stored.Out, global.Out),
		CacheIn:  pick(stored.ManualCacheIn, stored.CacheIn, global.CacheIn),
		CacheOut: pick(stored.ManualCacheOut, stored.CacheOut, global.CacheOut),
		Image:    pick(stored.ManualImage, stored.Image, global.Image),
		Audio:    pick(stored.ManualAudio, stored.Audio, global.Audio),
		Video:    pick(stored.ManualVideo, stored.Video, global.Video),
	}
}

// IsAnyManual reports whether any custom override is set on this row.
func IsAnyManual(s storedModelRates) bool {
	return storedIsManual(s)
}

func storedIsManual(s storedModelRates) bool {
	return s.ManualIn || s.ManualOut || s.ManualCacheIn || s.ManualCacheOut ||
		s.ManualImage || s.ManualAudio || s.ManualVideo
}
