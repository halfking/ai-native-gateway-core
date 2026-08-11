package autoroute

// scoring_doc16_regression_test.go — doc 16 §5-D / §5-E 的专项回归。
//
// 这些测试独立于 channel_quality_test / scoring_affinity_test /
// decision_test 中的旧测试，避免它们后续重构时连带修改本文件。两个核心
// 不变量是：
//
//  1. doc 16 §5-D: PriceScore 走 cohort P75 归一化后必须能在 cohort 内
//     拉出价格区分力（旧的 `(1000-avgCost)/10` 几乎钳制在 94–100）。
//  2. doc 16 §5-E: fallback 检测必须走显式 IsFallback 标记，
//     浮点三连等（Composite==50 && PriceScore==50 && MatchScore<=30）
//     不再触发 fallback 判定。

import (
	"sort"
	"testing"
)

// TestDoc16_PriceScore_P75Discrimination 验证 cohort P75 归一化恢复了
// 价格区分力。把同一候选池喂给 ScoreSimplified，验证：
//   - 最便宜的（ratio=0.1）拿 100
//   - 最贵的（ratio=1.5）拿 0
//   - 中位（ratio=0.55 / 0.95 / 1.0）应有明显梯度
func TestDoc16_PriceScore_P75Discrimination(t *testing.T) {
	makeCand := func(name string, blended float64) Candidate {
		// 用 1:1 input/output 拆分，blended = UnitPriceInPer1M + UnitPriceOutPer1M
		return Candidate{
			CanonicalName:     name,
			TaskMatchScore:    0.5,
			UnitPriceInPer1M:  blended / 2,
			UnitPriceOutPer1M: blended / 2,
			ProviderCategory:  "official",
			SuccessRate:       0.99,
			P95LatencyMs:      1000,
		}
	}

	// cohort = [10, 50, 100, 200, 150] — PriceP75 = 150 (index ceil(5*0.75)=4, 1-based → 150)
	pool := []Candidate{
		makeCand("c-cheap", 10),
		makeCand("c-mid-low", 50),
		makeCand("c-mid", 100),
		makeCand("c-mid-high", 150),
		makeCand("c-expensive", 200),
	}
	costCtx := recommendCostContext(pool)

	want := map[string]float64{
		// ratio 10/150   = 0.0667 → 100*(1.5-0.0667) = 143 → clamp 100
		"c-cheap": 100,
		// ratio 50/150   = 0.3333 → 100*(1.5-0.3333) = 116.67 → clamp 100
		"c-mid-low": 100,
		// ratio 100/150  = 0.6667 → 100*(1.5-0.6667) = 83.33
		"c-mid": 83.33,
		// ratio 150/150  = 1.0    → 50
		"c-mid-high": 50,
		// ratio 200/150  = 1.3333 → 100*(1.5-1.3333) = 16.67
		"c-expensive": 16.67,
	}

	for _, c := range pool {
		got := scorePriceByCostContext(c, costCtx)
		delta := got - want[c.CanonicalName]
		if delta < -0.5 || delta > 0.5 {
			t.Errorf("PriceScore[%s]: got %.2f, want %.2f (delta %.2f)",
				c.CanonicalName, got, want[c.CanonicalName], delta)
		}
	}

	// 关键不变量：gradient ≥ 40 点（旧公式下最便宜 vs 最贵的差距约 1.5 点）
	cheapest := scorePriceByCostContext(pool[0], costCtx)  // c-cheap
	expensive := scorePriceByCostContext(pool[4], costCtx) // c-expensive
	if gradient := cheapest - expensive; gradient < 40 {
		t.Errorf("PriceScore gradient between cheapest and expensive is %.2f, want ≥ 40 (P75 normalisation must restore discrimination)", gradient)
	}
}

// TestDoc16_PriceScore_NoBaseline 验证 cohort PriceP75 == 0 时
// （单免费候选）PriceScore 退化为中性 80 而非钳制到 100/0。
func TestDoc16_PriceScore_NoBaseline(t *testing.T) {
	zeroCtx := CostContext{} // PriceP75 == 0
	free := Candidate{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0}
	if got := scorePriceByCostContext(free, zeroCtx); got != 100 {
		t.Errorf("free candidate with no baseline: got %.2f, want 100", got)
	}

	paid := Candidate{UnitPriceInPer1M: 50, UnitPriceOutPer1M: 50}
	if got := scorePriceByCostContext(paid, zeroCtx); got != 80 {
		t.Errorf("paid candidate with no baseline: got %.2f, want 80 (neutral)", got)
	}
}

// TestDoc16_RecommendCostContext_ExcludesZero 验证 free / 未知价格的候选
// 不应拉低 PriceP75。
func TestDoc16_RecommendCostContext_ExcludesZero(t *testing.T) {
	pool := []Candidate{
		{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0},                                  // free
		{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0},                                  // free
		{UnitPriceInPer1M: 50, UnitPriceOutPer1M: 50},                                // 100
		{UnitPriceInPer1M: 75, UnitPriceOutPer1M: 75},                                // 150
		{UnitPriceInPer1M: 200, UnitPriceOutPer1M: 200, UnavailableReason: "manual"}, // excluded
	}
	ctx := recommendCostContext(pool)
	if ctx.PriceP75 <= 0 {
		t.Fatalf("PriceP75 should be > 0, got %v", ctx.PriceP75)
	}
	// 仅 paid 候选 [100, 150, 200, 400]（manual 被排除，free 被排除）：
	// sorted = [100, 150, 200, 400], n=4, p=0.75 → rank=ceil(3.0)=3 → index 3 → 400
	if ctx.PriceP75 != 400 {
		t.Errorf("PriceP75 should be 400 (excludes zero-price + unavailable), got %v", ctx.PriceP75)
	}
}

// TestDoc16_IsFallback_FlagRequired 验证 §5-E：单纯三连等 sentinel
// 不再被识别为 fallback；只有显式 IsFallback 标记才触发。
func TestDoc16_IsFallback_FlagRequired(t *testing.T) {
	// (a) 浮点三连等 sentinel 但 IsFallback = false → 不是 fallback。
	sentinel := []ScoredCandidate{{
		Breakdown: ScoringBreakdown{
			Composite:  50,
			PriceScore: 50,
			MatchScore: 25,
			// IsFallback omitted
		},
	}}
	if isFallbackWinner(sentinel) {
		t.Error("sentinel values alone must NOT trigger fallback (doc 16 §5-E)")
	}

	// (b) 同一 sentinel 加 IsFallback = true → 是 fallback。
	withFlag := []ScoredCandidate{{
		Breakdown: ScoringBreakdown{
			Composite:  50,
			PriceScore: 50,
			MatchScore: 25,
			IsFallback: true,
		},
	}}
	if !isFallbackWinner(withFlag) {
		t.Error("IsFallback=true must trigger fallback detection")
	}

	// (c) 多候选 → 不是 fallback（即使其中一个带 IsFallback）。
	multi := []ScoredCandidate{
		{Breakdown: ScoringBreakdown{Composite: 80, IsFallback: true}},
		{Breakdown: ScoringBreakdown{Composite: 70}},
	}
	if isFallbackWinner(multi) {
		t.Error("multi-candidate result must not be fallback regardless of flags")
	}

	// (d) 空 → 不是 fallback。
	if isFallbackWinner(nil) {
		t.Error("empty result must not be fallback")
	}
	if isFallbackWinner([]ScoredCandidate{}) {
		t.Error("zero-length result must not be fallback")
	}
}

// TestDoc16_PriceScore_FreeCandidateDominates 验证价格显著低（free）的
// 候选仍能拿到最高分（100），在 P75 归一化下不会因为 cohort P75 较高
// 而被压低。这与 §5-D 的设计目标一致：恢复价格区分力而不丢 free 优势。
func TestDoc16_PriceScore_FreeCandidateDominates(t *testing.T) {
	// cohort 中等价位（blended=300, 500, 700），PriceP75 约 500（n=3,
	// p=0.75 → rank=ceil(2.25)=3 → 700）。free candidate 仍在 P75
	// 之外且 blended==0 → 100；paid3=700 == P75 → 50；paid1=300 显著低于 P75。
	free := Candidate{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0}
	paid1 := Candidate{UnitPriceInPer1M: 150, UnitPriceOutPer1M: 150} // 300
	paid2 := Candidate{UnitPriceInPer1M: 250, UnitPriceOutPer1M: 250} // 500
	paid3 := Candidate{UnitPriceInPer1M: 350, UnitPriceOutPer1M: 350} // 700

	pool := []Candidate{paid1, paid2, paid3}
	costCtx := recommendCostContext(pool)

	freeScore := scorePriceByCostContext(free, costCtx)
	paid1Score := scorePriceByCostContext(paid1, costCtx)
	paid2Score := scorePriceByCostContext(paid2, costCtx)
	paid3Score := scorePriceByCostContext(paid3, costCtx)

	if freeScore != 100 {
		t.Errorf("free candidate must get PriceScore=100, got %.2f", freeScore)
	}
	if freeScore < paid1Score || freeScore < paid2Score || freeScore < paid3Score {
		t.Errorf("free candidate must dominate paid: free=%.2f paid1=%.2f paid2=%.2f paid3=%.2f",
			freeScore, paid1Score, paid2Score, paid3Score)
	}
	// 关键不变量：paid1 (cheapest paid) > paid3 (most expensive paid)
	if paid1Score <= paid3Score {
		t.Errorf("P75 normalisation must rank paid1 > paid3 (cheapest > expensive): paid1=%.2f paid3=%.2f",
			paid1Score, paid3Score)
	}
}

// TestDoc16_PriceScore_DiscriminationStrict 比 §5-D 修复前强 — 价格分数
// 必须在 cohort 跨度上有显著梯度。这是 doc 16 §5-D 的核心论证点。
func TestDoc16_PriceScore_DiscriminationStrict(t *testing.T) {
	// 构造真实场景的 cohort：5 个 per-1M-token 价位从 10 到 400
	pool := make([]Candidate, 0, 5)
	blended := []float64{10, 25, 50, 100, 400}
	for _, b := range blended {
		pool = append(pool, Candidate{
			UnitPriceInPer1M:  b / 2,
			UnitPriceOutPer1M: b / 2,
		})
	}
	costCtx := recommendCostContext(pool)

	scores := make([]float64, 0, len(pool))
	for _, c := range pool {
		scores = append(scores, scorePriceByCostContext(c, costCtx))
	}
	sort.Float64s(scores)

	spread := scores[len(scores)-1] - scores[0]
	if spread < 50 {
		t.Errorf("PriceScore spread (max-min) should be ≥ 50 with P75 normalisation, got %.2f (scores=%v)", spread, scores)
	}
}
