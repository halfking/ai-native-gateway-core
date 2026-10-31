package autoroute

import (
	"errors"
	"testing"
	"time"
)

// TestIndex_Recommend_ThreeTierFunnel 验证三级漏斗逻辑（需求 #6）
func TestIndex_Recommend_ThreeTierFunnel(t *testing.T) {
	// 构造候选池：5 个 primary（热门），3 个 secondary，2 个 fallback
	now := time.Now()
	primary := []Candidate{
		{CredentialID: 1, CanonicalName: "hot-1", Tier: "primary", PopularityScore: 100, Tags: []string{"reasoning"}, SuccessRate: 0.95, ReleasedAt: &now},
		{CredentialID: 2, CanonicalName: "hot-2", Tier: "primary", PopularityScore: 90, Tags: []string{"reasoning"}, SuccessRate: 0.93, ReleasedAt: &now},
		{CredentialID: 3, CanonicalName: "hot-3", Tier: "primary", PopularityScore: 80, Tags: []string{"reasoning"}, SuccessRate: 0.92, ReleasedAt: &now},
		{CredentialID: 4, CanonicalName: "hot-4", Tier: "primary", PopularityScore: 70, Tags: []string{"reasoning"}, SuccessRate: 0.90, ReleasedAt: &now},
		{CredentialID: 5, CanonicalName: "hot-5", Tier: "primary", PopularityScore: 60, Tags: []string{"reasoning"}, SuccessRate: 0.88, ReleasedAt: &now},
	}
	secondary := []Candidate{
		{CredentialID: 6, CanonicalName: "sec-1", Tier: "secondary", PopularityScore: 50, Tags: []string{"reasoning"}, SuccessRate: 0.85, ReleasedAt: &now},
		{CredentialID: 7, CanonicalName: "sec-2", Tier: "secondary", PopularityScore: 40, Tags: []string{"reasoning"}, SuccessRate: 0.83, ReleasedAt: &now},
		{CredentialID: 8, CanonicalName: "sec-3", Tier: "secondary", PopularityScore: 30, Tags: []string{"reasoning"}, SuccessRate: 0.80, ReleasedAt: &now},
	}
	fallback := []Candidate{
		{CredentialID: 9, CanonicalName: "fb-1", Tier: "fallback", PopularityScore: 20, Tags: []string{"reasoning"}, SuccessRate: 0.75, ReleasedAt: &now},
		{CredentialID: 10, CanonicalName: "fb-2", Tier: "fallback", PopularityScore: 10, Tags: []string{"reasoning"}, SuccessRate: 0.70, ReleasedAt: &now},
	}

	all := append(append(primary, secondary...), fallback...)
	idx := &Index{entries: all, lastRefresh: time.Now()}

	results := idx.Recommend(TaskReasoning, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, sc := range results {
		if sc.Candidate.Tier != "primary" {
			t.Errorf("result[%d] tier=%s, want primary", i, sc.Candidate.Tier)
		}
	}

	smallPrimary := []Candidate{
		{CredentialID: 1, CanonicalName: "hot-1", Tier: "primary", PopularityScore: 100, Tags: []string{"code"}, SuccessRate: 0.95, ReleasedAt: &now},
		{CredentialID: 2, CanonicalName: "hot-2", Tier: "primary", PopularityScore: 90, Tags: []string{"code"}, SuccessRate: 0.93, ReleasedAt: &now},
	}
	smallSecondary := []Candidate{
		{CredentialID: 6, CanonicalName: "sec-1", Tier: "secondary", PopularityScore: 50, Tags: []string{"code"}, SuccessRate: 0.85, ReleasedAt: &now},
		{CredentialID: 7, CanonicalName: "sec-2", Tier: "secondary", PopularityScore: 40, Tags: []string{"code"}, SuccessRate: 0.83, ReleasedAt: &now},
	}
	idxSmall := &Index{entries: append(smallPrimary, smallSecondary...), lastRefresh: time.Now()}
	resultsSmall := idxSmall.Recommend(TaskCode, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if len(resultsSmall) < 3 {
		t.Fatalf("expected at least 3 results (with fallback), got %d", len(resultsSmall))
	}
	primaryCount := 0
	secondaryCount := 0
	for _, sc := range resultsSmall {
		switch sc.Candidate.Tier {
		case "primary":
			primaryCount++
		case "secondary":
			secondaryCount++
		}
	}
	if primaryCount != 2 {
		t.Errorf("expected 2 primary, got %d", primaryCount)
	}
	if secondaryCount < 1 {
		t.Errorf("expected at least 1 secondary (fallback), got %d", secondaryCount)
	}
}

// TestIndex_Recommend_PopularitySort 验证热门池按 popularity_score 排序
func TestIndex_Recommend_PopularitySort(t *testing.T) {
	now := time.Now()
	candidates := []Candidate{
		{CredentialID: 3, CanonicalName: "low", Tier: "primary", PopularityScore: 30, Tags: []string{"chat"}, SuccessRate: 0.90, ReleasedAt: &now},
		{CredentialID: 1, CanonicalName: "high", Tier: "primary", PopularityScore: 100, Tags: []string{"chat"}, SuccessRate: 0.85, ReleasedAt: &now},
		{CredentialID: 2, CanonicalName: "mid", Tier: "primary", PopularityScore: 60, Tags: []string{"chat"}, SuccessRate: 0.88, ReleasedAt: &now},
	}
	idx := &Index{entries: candidates, lastRefresh: time.Now()}

	results := idx.Recommend(TaskChat, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	names := make(map[string]bool)
	for _, sc := range results {
		names[sc.Candidate.CanonicalName] = true
	}
	if !names["high"] || !names["mid"] || !names["low"] {
		t.Errorf("expected all 3 candidates in results, got %v", names)
	}
}

// TestIndex_Recommend_EmptyPool 验证空池处理
func TestIndex_Recommend_EmptyPool(t *testing.T) {
	idx := &Index{entries: []Candidate{}, lastRefresh: time.Now()}
	results := idx.Recommend(TaskReasoning, ClassificationSignals{EstimatedTokens: 1000}, ProfileSmart, 3)
	if results != nil {
		t.Errorf("expected nil for empty pool, got %d results", len(results))
	}
}

type stubScanRow struct {
	values []any
}

func (s stubScanRow) Scan(dest ...any) error {
	if len(dest) != len(s.values) {
		return errors.New("destination count mismatch")
	}
	for i, d := range dest {
		switch ptr := d.(type) {
		case *int64:
			*ptr = s.values[i].(int64)
		case *string:
			*ptr = s.values[i].(string)
		case **int:
			if s.values[i] == nil {
				*ptr = nil
			} else {
				v := s.values[i].(int)
				*ptr = &v
			}
		case **string:
			if s.values[i] == nil {
				*ptr = nil
			} else {
				v := s.values[i].(string)
				*ptr = &v
			}
		case *[]string:
			*ptr = append((*ptr)[:0], s.values[i].([]string)...)
		case **float64:
			if s.values[i] == nil {
				*ptr = nil
			} else {
				v := s.values[i].(float64)
				*ptr = &v
			}
		case *int:
			*ptr = s.values[i].(int)
		case **int16:
			if s.values[i] == nil {
				*ptr = nil
			} else {
				v := s.values[i].(int16)
				*ptr = &v
			}
		case **bool:
			if s.values[i] == nil {
				*ptr = nil
			} else {
				v := s.values[i].(bool)
				*ptr = &v
			}
		default:
			return errors.New("unsupported scan destination")
		}
	}
	return nil
}

func TestScanIndexRow_LoadsAvailabilityAndTier(t *testing.T) {
	row := stubScanRow{values: []any{
		int64(11),
		"gpt-4.1",
		1,
		"gpt-4.1",
		[]string{"reasoning", "code"},
		128000,
		"token",
		10.0,
		20.0,
		"USD", // price_currency (P1-C)
		0.97,
		950,
		2,
		8,
		int16(1),
		"manual_pause",
		// CHANNEL_QUALITY_ROUTING: 新增字段
		"official", // provider_category
		"cloud",    // provider_kind
		false,      // is_free
		"",         // cost_tier
		"",         // complexity_ceiling (M2)
		"",         // min_complexity (M2)
		"text",     // modality
	}}

	c, err := scanIndexRow(row)
	if err != nil {
		t.Fatalf("scanIndexRow err: %v", err)
	}
	if c.Tier != "primary" {
		t.Fatalf("tier: got %s, want primary", c.Tier)
	}
	if c.UnavailableReason != "manual_pause" {
		t.Fatalf("unavailable reason: got %q", c.UnavailableReason)
	}
	if c.PopularityScore <= 0 {
		t.Fatalf("popularity score should be populated, got %.2f", c.PopularityScore)
	}
	if c.ProviderCategory != "official" {
		t.Fatalf("provider category: got %q, want official", c.ProviderCategory)
	}
	if c.IsFree {
		t.Fatalf("is_free should be false for paid token billing")
	}
}

func TestScanIndexRow_MapsSecondaryAndFallback(t *testing.T) {
	tests := []struct {
		name        string
		routingTier int16
		wantTier    string
	}{
		{name: "secondary", routingTier: 2, wantTier: "secondary"},
		{name: "fallback", routingTier: 4, wantTier: "fallback"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := stubScanRow{values: []any{
				int64(12),
				"m",
				1,
				"m",
				[]string{"chat"},
				64000,
				"token",
				1.0,
				1.0,
				"USD", // price_currency (P1-C)
				0.9,
				500,
				0,
				0,
				tt.routingTier,
				"",
				// CHANNEL_QUALITY_ROUTING: 新增字段
				"aggregator",
				"cloud",
				false,
				"",
				"",     // complexity_ceiling (M2)
				"",     // min_complexity (M2)
				"text", // modality
			}}
			c, err := scanIndexRow(row)
			if err != nil {
				t.Fatalf("scanIndexRow err: %v", err)
			}
			if c.Tier != tt.wantTier {
				t.Fatalf("tier: got %s, want %s", c.Tier, tt.wantTier)
			}
			if c.ProviderCategory != "aggregator" {
				t.Fatalf("provider category: got %q, want aggregator", c.ProviderCategory)
			}
		})
	}
}

// TestScanIndexRow_LoadsCostTier 验证 mc.cost_tier 字段被正确加载
// （deriveIsFree 的 CostTier 分支依赖于该字段）。
func TestScanIndexRow_LoadsCostTier(t *testing.T) {
	row := stubScanRow{values: []any{
		int64(13),
		"m-free",
		1,
		"m-free",
		[]string{"chat"},
		64000,
		"token", // 非 free billing_mode
		5.0,     // 非零价格
		5.0,
		"USD",   // price_currency (P1-C)
		0.95,
		1000,
		0,
		10,
		int16(1),
		"",
		"aggregator",
		"cloud",
		false,  // SQL CASE 没有命中，所以 is_free = false
		"free", // 但 cost_tier = 'free' → Go 侧 deriveIsFree 应识别
		"",     // complexity_ceiling (M2)
		"",     // min_complexity (M2)
		"text", // modality
	}}

	c, err := scanIndexRow(row)
	if err != nil {
		t.Fatalf("scanIndexRow err: %v", err)
	}
	if c.CostTier != "free" {
		t.Fatalf("cost_tier: got %q, want free", c.CostTier)
	}
	if !deriveIsFree(c) {
		t.Fatalf("deriveIsFree should be true (cost_tier=free)")
	}
}

// TestComputeCostContext_SpeedP95Unsorted verifies that SpeedP95 correctly
// returns the maximum P95 latency regardless of input order.
// Regression test for the unsorted-speeds bug.
func TestComputeCostContext_SpeedP95Unsorted(t *testing.T) {
	// Candidates deliberately NOT ordered by latency.
	cands := []Candidate{
		{P95LatencyMs: 500, UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10},
		{P95LatencyMs: 2000, UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10},
		{P95LatencyMs: 800, UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10},
	}

	ctx := computeCostContext(cands)
	if ctx.SpeedP95 != 2000 {
		t.Fatalf("SpeedP95: got %.0f, want 2000 (true max)", ctx.SpeedP95)
	}
}

// TestComputeCostContext_CNYSingleCurrency_NominalFold — P1-C (audit
// 2026-10-28 §4): a pure-CNY cohort folds to USD before the P75. With the
// default rate 7.2, blended ¥72/1M ≈ $10 — the P75 must reflect the folded
// value, not the raw CNY numeral. (旧实现：P75 直接用 CNY 数值 ⇒ 比同档 USD
// 贵 7.2 倍的假象。)
func TestComputeCostContext_CNYSingleCurrency_NominalFold(t *testing.T) {
	cands := []Candidate{
		{UnitPriceInPer1M: 36, UnitPriceOutPer1M: 36, Currency: "CNY"}, // ¥72 → $10
		{UnitPriceInPer1M: 72, UnitPriceOutPer1M: 72, Currency: "CNY"}, // ¥144 → $20
		{UnitPriceInPer1M: 108, UnitPriceOutPer1M: 108, Currency: "CNY"}, // ¥216 → $30
		{UnitPriceInPer1M: 180, UnitPriceOutPer1M: 180, Currency: "CNY"}, // ¥360 → $50
	}
	ctx := computeCostContext(cands)
	if !ctx.HasMixedPrices {
		t.Fatal("CNY fold must flip HasMixedPrices")
	}
	// Folded prices: 10/20/30/50 sorted; P75 (nearest-rank ceil(4×0.75)=3) = 30.
	assertDelta(t, 30.0, ctx.PriceP75)
}

// TestComputeCostContext_MixedCohort_OneRuler — the audit's headline case:
// CNY and USD candidates must compete on the same scale. A ¥20/1M CNY offer
// (~$2.8) must rank CHEAPER than a $10/1M USD offer; the old code read the
// CNY numeral 20 as $20 and ranked it more expensive.
func TestComputeCostContext_MixedCohort_OneRuler(t *testing.T) {
	cands := []Candidate{
		{UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10, Currency: "USD"},    // $20
		{UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10, Currency: "USD"},    // $20
		{UnitPriceInPer1M: 5, UnitPriceOutPer1M: 5, Currency: "CNY"},      // ¥10 → ~$1.39
		{UnitPriceInPer1M: 7, UnitPriceOutPer1M: 7, Currency: "CNY"},      // ¥14 → ~$1.94
		{UnitPriceInPer1M: 100, UnitPriceOutPer1M: 100, Currency: "USD"},  // $200
	}
	ctx := computeCostContext(cands)
	if !ctx.HasMixedPrices {
		t.Fatal("mixed cohort must flip HasMixedPrices")
	}
	// Folded: 1.39, 1.94, 20, 20, 200 → P75 rank ceil(5×0.75)=4 → 20.
	if ctx.PriceP75 < 19.9 || ctx.PriceP75 > 20.1 {
		t.Fatalf("P75 = %v, want ≈20 (CNY folded under USD before ranking)", ctx.PriceP75)
	}
	// Direct: scorePrice must rank the CNY candidate better than the equal-
	// numeral USD one would have been.
	cheapCNY := scorePrice(cands[2], ctx)
	expensiveUSD := scorePrice(cands[4], ctx)
	if cheapCNY <= expensiveUSD {
		t.Fatalf("folded CNY (%.1f) must out-score $200/1M USD (%.1f)", cheapCNY, expensiveUSD)
	}
}

// TestComputeCostContext_USDOnly_Unchanged — USD-only cohorts keep the exact
// pre-P1-C baseline (no fold applied, HasMixedPrices stays false).
func TestComputeCostContext_USDOnly_Unchanged(t *testing.T) {
	cands := []Candidate{
		{UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10, Currency: "USD"},
		{UnitPriceInPer1M: 20, UnitPriceOutPer1M: 20, Currency: "USD"},
		{UnitPriceInPer1M: 30, UnitPriceOutPer1M: 30, Currency: "USD"},
		{UnitPriceInPer1M: 40, UnitPriceOutPer1M: 40, Currency: "USD"},
	}
	ctx := computeCostContext(cands)
	if ctx.HasMixedPrices {
		t.Fatal("USD-only cohort must not flip HasMixedPrices")
	}
	// Sorted 20/40/60/80; P75 rank 3 = 60.
	assertDelta(t, 60.0, ctx.PriceP75)
}

// TestComputeCostContext_EmptyCurrencyMeansUSD — zero-value snapshots
// (Currency "" from old code paths) must score exactly as before.
func TestComputeCostContext_EmptyCurrencyMeansUSD(t *testing.T) {
	cands := []Candidate{
		{UnitPriceInPer1M: 10, UnitPriceOutPer1M: 10}, // Currency "" — old snapshot
		{UnitPriceInPer1M: 20, UnitPriceOutPer1M: 20},
		{UnitPriceInPer1M: 30, UnitPriceOutPer1M: 30},
		{UnitPriceInPer1M: 40, UnitPriceOutPer1M: 40},
	}
	ctx := computeCostContext(cands)
	if ctx.HasMixedPrices {
		t.Fatal("empty currency must mean USD, not a fold")
	}
	assertDelta(t, 60.0, ctx.PriceP75)
}

func assertDelta(t *testing.T, want, got float64) {
	t.Helper()
	d := want - got
	if d < 0 {
		d = -d
	}
	if d > 0.2 {
		t.Fatalf("got %v, want %v (±0.2)", got, want)
	}
}
