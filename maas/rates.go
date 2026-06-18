package maas

import "math"

// BaseRateSet is the global default credits per 1M tokens (before discount).
type BaseRateSet struct {
	In       int64
	Out      int64
	CacheIn  int64
	CacheOut int64
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
		Discount: disc,
	}
}

// ModelRateValues holds effective billing rates for one model.
type ModelRateValues struct {
	In       int64
	Out      int64
	CacheIn  int64
	CacheOut int64
}

type storedModelRates struct {
	In, Out, CacheIn, CacheOut       *int64
	ManualIn, ManualOut              bool
	ManualCacheIn, ManualCacheOut    bool
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
	}
}

func storedIsManual(s storedModelRates) bool {
	return s.ManualIn || s.ManualOut || s.ManualCacheIn || s.ManualCacheOut
}
