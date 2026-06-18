package maas

import "testing"

func TestCalcCredits_fourDimensions(t *testing.T) {
	rates := ModelRateValues{In: 10000, Out: 20000, CacheIn: 5000, CacheOut: 15000}
	got := CalcCredits(1_000_000, 500_000, 200_000, 100_000, rates)
	// 1M*10k + 0.5M*20k + 0.2M*5k + 0.1M*15k = 10k+10k+1k+1.5k = 22500
	if got != 22500 {
		t.Fatalf("got %d want 22500", got)
	}
}

func TestGlobalEffective_discount(t *testing.T) {
	st := Settings{
		BaseCreditsPer1M: 10000,
		BaseCreditsPer1MOut: 12000,
		GlobalDiscount: 0.8,
	}
	g := globalEffective(st)
	if g.In != 8000 || g.Out != 9600 {
		t.Fatalf("discount apply failed: %+v", g)
	}
}

func TestEffectiveModelRates_partialManual(t *testing.T) {
	global := BaseRateSet{In: 100, Out: 200, CacheIn: 50, CacheOut: 80}
	custom := int64(999)
	stored := storedModelRates{ManualIn: true, In: &custom}
	eff := effectiveModelRates(stored, global)
	if eff.In != 999 || eff.Out != 200 {
		t.Fatalf("partial manual failed: %+v", eff)
	}
}
