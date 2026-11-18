package modelbudget

import (
	"math"
	"sync"
	"testing"
)

// The ladders and the default pricing table must stay aligned: a ladder
// member the estimator can't price estimates 0 and would "fit" any
// budget, silently defeating degradation. This test is the alignment钉测.
func TestDefaultLaddersFullyPriced(t *testing.T) {
	e := NewEstimator()
	for _, ladder := range defaultLadders {
		for _, m := range ladder {
			// A priced model has a positive input OR output price.
			if e.Estimate(m, 1000, 1000) <= 0 {
				t.Errorf("ladder member %q unpriced in default table — degradation would silently pass it through", m)
			}
		}
	}
}

func TestSelect_NoBudgetPassthrough(t *testing.T) {
	d := NewDegrader(NewEstimator())
	dec := d.Select("gpt-4", 10_000, 5_000, 0)
	if dec.Model != "gpt-4" || dec.Degraded || !dec.FitsBudget {
		t.Fatalf("decision = %+v, want passthrough", dec)
	}
	// 10K in × $0.03/1K + 5K out × $0.06/1K = 0.30 + 0.30 = 0.60
	if math.Abs(dec.EstimatedCost-0.60) > 1e-9 {
		t.Errorf("cost = %v, want 0.60", dec.EstimatedCost)
	}
}

func TestSelect_WithinBudgetPassthrough(t *testing.T) {
	d := NewDegrader(NewEstimator())
	dec := d.Select("gpt-4", 1_000, 1_000, 1.00) // costs 0.09
	if dec.Model != "gpt-4" || dec.Degraded || !dec.FitsBudget {
		t.Fatalf("decision = %+v, want passthrough", dec)
	}
	if dec.EstimatedCost <= 0 {
		t.Errorf("cost = %v, want priced", dec.EstimatedCost)
	}
}

func TestSelect_DegradesWithinFamily(t *testing.T) {
	d := NewDegrader(NewEstimator())
	// gpt-4 at 100K/50K = 3.00+3.00 = 6.00 over a 1.00 budget:
	// gpt-4-turbo = 1.00+1.50 = 2.50 still over → gpt-4o = 0.50+0.75 = 1.25
	// still over → gpt-4o-mini = 0.015+0.03 = 0.045 fits.
	dec := d.Select("gpt-4", 100_000, 50_000, 1.00)
	if !dec.Degraded || !dec.FitsBudget {
		t.Fatalf("decision = %+v, want degraded+fits", dec)
	}
	if dec.Model != "gpt-4o-mini" {
		t.Fatalf("model = %q, want gpt-4o-mini (first fitting ladder step)", dec.Model)
	}
	if dec.Requested != "gpt-4" {
		t.Errorf("requested lost: %+v", dec)
	}
}

func TestSelect_DegradesOneStepWhenPossible(t *testing.T) {
	d := NewDegrader(NewEstimator())
	// Budget 2.60: gpt-4 (6.00) over → gpt-4-turbo (2.50) fits → one step only.
	dec := d.Select("gpt-4", 100_000, 50_000, 2.60)
	if !dec.Degraded || dec.Model != "gpt-4-turbo" {
		t.Fatalf("decision = %+v, want one-step degrade to gpt-4-turbo", dec)
	}
}

func TestSelect_NoFamilyMemberFits(t *testing.T) {
	d := NewDegrader(NewEstimator())
	// Budget 0.001: every OpenAI family member is over. Cheapest
	// (gpt-3.5-turbo) returned with FitsBudget=false — caller decides.
	dec := d.Select("gpt-4", 100_000, 50_000, 0.001)
	if !dec.Degraded || dec.FitsBudget {
		t.Fatalf("decision = %+v, want degraded+not-fits", dec)
	}
	if dec.Model != "gpt-3.5-turbo" {
		t.Fatalf("model = %q, want family cheapest gpt-3.5-turbo", dec.Model)
	}
	if dec.EstimatedCost <= 0 {
		t.Errorf("cheapest pick must carry an honest price, got %v", dec.EstimatedCost)
	}
}

// The口径 symmetry钉测: unknown pricing must pass through, never reject —
// the identical rule the agent side's WithinBudget enforces.
func TestSelect_UnknownModelPassthrough(t *testing.T) {
	d := NewDegrader(NewEstimator())
	dec := d.Select("mystery-model-9", 1_000_000, 1_000_000, 0.000001)
	if dec.Model != "mystery-model-9" || dec.Degraded || !dec.FitsBudget {
		t.Fatalf("decision = %+v, want unknown-price passthrough", dec)
	}
	if dec.EstimatedCost != 0 {
		t.Errorf("cost = %v, want 0 for unknown pricing", dec.EstimatedCost)
	}
}

func TestSelect_UnladdredModelOverBudget(t *testing.T) {
	// claude-3-5-sonnet is priced but NOT on a ladder (same price as
	// claude-3-sonnet — a step with no price delta is pointless). Over
	// budget it must be reported honestly, not silently kept.
	d := NewDegrader(NewEstimator())
	dec := d.Select("claude-3-5-sonnet", 1_000_000, 1_000_000, 0.01)
	if dec.Degraded || dec.FitsBudget {
		t.Fatalf("decision = %+v, want honest not-fits (no ladder)", dec)
	}
	if dec.Model != "claude-3-5-sonnet" {
		t.Errorf("model = %q, want requested (nothing to degrade to)", dec.Model)
	}
}

func TestSelect_AlreadyFamilyCheapest(t *testing.T) {
	d := NewDegrader(NewEstimator())
	dec := d.Select("gemini-1.5-flash", 1_000_000, 1_000_000, 0.000001)
	if dec.Degraded || dec.FitsBudget {
		t.Fatalf("decision = %+v, want not-fits with no degrade", dec)
	}
	if dec.Model != "gemini-1.5-flash" {
		t.Errorf("model = %q, want requested", dec.Model)
	}
}

func TestSelect_CustomLadders(t *testing.T) {
	custom := [][]string{{"big", "small"}}
	src := fixedPricing{"big": 5.0, "small": 1.0}
	d := NewDegraderWithLadders(src, custom)
	// 500+500 tokens → multiplier 1: big costs 5.0 (over 2.0), small 1.0 (fits).
	dec := d.Select("big", 500, 500, 2.0)
	if !dec.Degraded || dec.Model != "small" || !dec.FitsBudget {
		t.Fatalf("decision = %+v, want degrade to small", dec)
	}
}

func TestSelect_NilPricingDefaults(t *testing.T) {
	d := NewDegrader(nil)
	dec := d.Select("gpt-4o", 1_000, 1_000, 0) // no budget: pure passthrough with priced cost
	if !dec.FitsBudget || dec.EstimatedCost <= 0 {
		t.Fatalf("decision = %+v, want priced passthrough via default estimator", dec)
	}
}

// fixedPricing is a deterministic PricingSource double.
type fixedPricing map[string]float64

func (f fixedPricing) Estimate(model string, inputTokens, outputTokens int) float64 {
	v, ok := f[model]
	if !ok {
		return 0
	}
	return v * float64(inputTokens+outputTokens) / 1000
}

// Estimator is immutable and shared across goroutines on the request path —
// race the reads.
func TestEstimator_ConcurrentReads(t *testing.T) {
	e := NewEstimator()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				_ = e.Estimate("gpt-4", 1000, 1000)
				_ = e.Estimate("unknown", 1000, 1000)
			}
		}()
	}
	wg.Wait()
}
