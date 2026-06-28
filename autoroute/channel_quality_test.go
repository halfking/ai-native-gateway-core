package autoroute

// CHANNEL_QUALITY_ROUTING 单元测试。
//
// 覆盖：
//   - scoreChannelQuality：各 category 静态分 + 运行时 delta
//   - scoreReliability：success_rate + p95_latency 推导
//   - ScoreWithChannelQuality：4 维公式
//   - StratifyByChannelQuality / IsPreferredChannelSaturated / ApplyFallbackDemotion
//   - stratifyAndPickTopN：路由核心流程
//   - deriveIsFree：三路判定（billing_mode / cost_tier / price==0）

import (
	"sort"
	"testing"
)

// ── scoreChannelQuality ──────────────────────────────────────────

func TestScoreChannelQuality_ByCategory(t *testing.T) {
	tests := []struct {
		name     string
		category string
		wantBase float64
	}{
		{"official", "official", 90},
		{"official_proxy", "official_proxy", 90},
		{"self_host", "self_host", 80},
		{"aggregator", "aggregator", 60},
		{"third_party_relay", "third_party_relay", 50},
		{"unknown", "weird_new_kind", 40},
		{"empty", "", 40},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Candidate{ProviderCategory: tt.category}
			got := scoreChannelQuality(c)
			if got != tt.wantBase {
				t.Errorf("scoreChannelQuality(%s) = %.2f, want %.2f", tt.category, got, tt.wantBase)
			}
		})
	}
}

func TestScoreChannelQuality_RuntimeDelta(t *testing.T) {
	// 官方 + 高成功 + 低延迟 → +10 奖励
	officialGreat := Candidate{
		ProviderCategory:  "official",
		SuccessRate:       0.98,
		P95LatencyMs:      1500,
		UnitPriceInPer1M:  10, // 显式付费，避免被 deriveIsFree 误判
		UnitPriceOutPer1M: 30,
	}
	if got := scoreChannelQuality(officialGreat); got != 100 {
		t.Errorf("official great should be 100, got %.2f", got)
	}

	// 官方 + 低成功率 → -20
	officialBad := Candidate{
		ProviderCategory:  "official",
		SuccessRate:       0.70,
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
	}
	if got := scoreChannelQuality(officialBad); got != 70 {
		t.Errorf("official low success (70/100) should be 70, got %.2f", got)
	}

	// 第三方 + 高延迟 → -15
	relaySlow := Candidate{
		ProviderCategory:  "third_party_relay",
		SuccessRate:       0.90,
		P95LatencyMs:      6000,
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
	}
	if got := scoreChannelQuality(relaySlow); got != 35 {
		t.Errorf("relay+slow should be 35, got %.2f", got)
	}

	// 严重低成功率 → 额外 -30
	relayTerrible := Candidate{
		ProviderCategory:  "third_party_relay",
		SuccessRate:       0.50,
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
	}
	if got := scoreChannelQuality(relayTerrible); got != 0 {
		// 50 - 20 (low success) - 30 (terrible) = 0 (clamped)
		t.Errorf("relay+terrible should clamp to 0, got %.2f", got)
	}
}

func TestScoreChannelQuality_FreeAndUnreliable(t *testing.T) {
	// 免费 + success < 0.9 → 额外 -25
	freeBad := Candidate{
		ProviderCategory: "aggregator",
		BillingMode:      "free",
		SuccessRate:      0.85,
	}
	got := scoreChannelQuality(freeBad)
	wantBase := 60.0
	wantDelta := 0.0 - 25.0 // free+unreliable: -25
	if got != wantBase+wantDelta {
		t.Errorf("free+aggregator+low success: got %.2f, want %.2f", got, wantBase+wantDelta)
	}

	// 价格全 0 也算免费
	freeZeroPrice := Candidate{
		ProviderCategory:  "third_party_relay",
		UnitPriceInPer1M:  0,
		UnitPriceOutPer1M: 0,
		SuccessRate:       0.85,
	}
	got2 := scoreChannelQuality(freeZeroPrice)
	wantBase2 := 50.0
	if got2 != wantBase2-25.0 {
		t.Errorf("free zero price: got %.2f, want %.2f", got2, wantBase2-25.0)
	}
}

func TestScoreChannelQuality_ClampToBounds(t *testing.T) {
	// 全部为正：不应超过 100
	c := Candidate{ProviderCategory: "official", SuccessRate: 1.0, P95LatencyMs: 500,
		UnitPriceInPer1M: 10, UnitPriceOutPer1M: 30}
	if got := scoreChannelQuality(c); got > 100 {
		t.Errorf("should not exceed 100, got %.2f", got)
	}

	// 全部为负：不应低于 0（且 IsFree=true 配合低质量会压到底）
	c = Candidate{ProviderCategory: "third_party_relay", SuccessRate: 0.30, P95LatencyMs: 10000, IsFree: true}
	if got := scoreChannelQuality(c); got < 0 {
		t.Errorf("should not be below 0, got %.2f", got)
	}
}

// ── scoreReliability ─────────────────────────────────────────────

func TestScoreReliability(t *testing.T) {
	tests := []struct {
		name    string
		c       Candidate
		wantMin float64
		wantMax float64
	}{
		{"perfect", Candidate{SuccessRate: 1.0, P95LatencyMs: 500}, 100, 100},
		{"good", Candidate{SuccessRate: 0.95, P95LatencyMs: 2000}, 88, 90},
		{"slow", Candidate{SuccessRate: 0.95, P95LatencyMs: 4000}, 82, 84},
		{"unreliable", Candidate{SuccessRate: 0.50, P95LatencyMs: 1000}, 60, 60},
		{"cold_start", Candidate{SuccessRate: 0, P95LatencyMs: 0}, 50, 50},
		{"unknown_latency", Candidate{SuccessRate: 0.9, P95LatencyMs: 0}, 80, 84},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreReliability(tt.c)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("scoreReliability(%+v) = %.2f, want [%.2f, %.2f]",
					tt.c, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// ── deriveIsFree ────────────────────────────────────────────────

func TestDeriveIsFree(t *testing.T) {
	tests := []struct {
		name string
		c    Candidate
		want bool
	}{
		{"explicit_true", Candidate{IsFree: true}, true},
		{"billing_mode_free", Candidate{BillingMode: "free"}, true},
		{"billing_mode_token_plan", Candidate{BillingMode: "token_plan"}, true},
		{"billing_mode_code_plan", Candidate{BillingMode: "code_plan"}, true},
		{"billing_mode_monthly", Candidate{BillingMode: "monthly"}, true},
		{"cost_tier_free", Candidate{CostTier: "free"}, true},
		{"price_zero", Candidate{UnitPriceInPer1M: 0, UnitPriceOutPer1M: 0}, true},
		{"pay_as_you_go", Candidate{BillingMode: "token", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2}, false},
		{"paid_default", Candidate{UnitPriceInPer1M: 5, UnitPriceOutPer1M: 15}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := deriveIsFree(tt.c); got != tt.want {
				t.Errorf("deriveIsFree(%+v) = %v, want %v", tt.c, got, tt.want)
			}
		})
	}
}

// ── ScoreWithChannelQuality（4 维公式） ──────────────────────────

func TestScoreWithChannelQuality_4Dim(t *testing.T) {
	// 构造：Minimax 官方 vs NVIDIA NIM 免费（同 intent、同价格）
	minimax := Candidate{
		CanonicalID:       1,
		CanonicalName:     "minimax-m2",
		ProviderCategory:  "official",
		SuccessRate:       0.97,
		P95LatencyMs:      1500,
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
		TaskMatchScore:    0.8,
	}
	nvidiaNim := Candidate{
		CanonicalID:       2,
		CanonicalName:     "nvidia-nim-free",
		ProviderCategory:  "aggregator",
		BillingMode:       "free",
		SuccessRate:       0.78, // 错误率较高
		P95LatencyMs:      6500, // 延迟较大
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30, // 同价
		TaskMatchScore:    0.8,
	}

	avgPrices := map[int]float64{1: 40, 2: 40}

	sMinimax := ScoreWithChannelQuality(minimax, TaskCode, avgPrices, 0)
	sNvidia := ScoreWithChannelQuality(nvidiaNim, TaskCode, avgPrices, 0)

	// ChannelQuality 应当清晰分层
	if sMinimax.ChannelQuality <= sNvidia.ChannelQuality {
		t.Errorf("Minimax ChannelQuality (%.2f) should beat NVIDIA NIM (%.2f)",
			sMinimax.ChannelQuality, sNvidia.ChannelQuality)
	}
	if sMinimax.ChannelQuality < 80 {
		t.Errorf("Minimax ChannelQuality should be >= 80, got %.2f", sMinimax.ChannelQuality)
	}
	if sNvidia.ChannelQuality >= 50 {
		t.Errorf("NVIDIA NIM free+unreliable should be < 50 (fallback pool), got %.2f", sNvidia.ChannelQuality)
	}

	// Composite 应当也分层（即使价格、intent 相同）
	if sMinimax.Composite <= sNvidia.Composite {
		t.Errorf("Minimax Composite (%.2f) should beat NVIDIA NIM (%.2f) at same price",
			sMinimax.Composite, sNvidia.Composite)
	}
}

func TestScoreWithChannelQuality_Formula(t *testing.T) {
	// 验证权重：composite = intent*0.4 + price*0.2 + channel*0.3 + reliability*0.1 + correction
	c := Candidate{
		TaskMatchScore:    0.5, // → intent 50
		UnitPriceInPer1M:  100,
		UnitPriceOutPer1M: 100,        // → price (1000-200)/10 = 80
		ProviderCategory:  "official", // → channel base 90
		SuccessRate:       0.97,       // → +10 delta → channel 100
		P95LatencyMs:      1500,       // 满足 p95<2000 触发 +10
	}
	avgPrices := map[int]float64{0: 200}

	got := ScoreWithChannelQuality(c, TaskCode, avgPrices, 0)
	// reliability: 0.97*80 + 20 (p95<=1000ms 阈值改: <=1000) — 1500 走 <=3000 档 → +12
	//   实际: 0.97*80 = 77.6, latencyFactor 12 → 89.6
	want := 50*0.4 + 80*0.2 + 100*0.3 + 89.6*0.1 + 0
	if diff := got.Composite - want; diff < -0.5 || diff > 0.5 {
		t.Errorf("composite: got %.2f, want ~%.2f (diff %.2f)", got.Composite, want, diff)
	}
	if got.MatchScore != 50 {
		t.Errorf("MatchScore: got %.2f, want 50", got.MatchScore)
	}
	if got.PriceScore != 80 {
		t.Errorf("PriceScore: got %.2f, want 80", got.PriceScore)
	}
	if got.ChannelQuality != 100 {
		t.Errorf("ChannelQuality: got %.2f, want 100", got.ChannelQuality)
	}
}

func TestScoreSimplified_LegacyFormulaPreserved(t *testing.T) {
	// 验证 ScoreSimplified 仍然使用旧的 2 维公式（向后兼容）
	c := Candidate{
		TaskMatchScore:    0.5,
		UnitPriceInPer1M:  100,
		UnitPriceOutPer1M: 100,
		ProviderCategory:  "official",
	}
	avgPrices := map[int]float64{0: 200}

	got := ScoreSimplified(c, TaskCode, avgPrices, 0)
	want := 50*0.6 + 80*0.4 + 0
	if diff := got.Composite - want; diff < -0.01 || diff > 0.01 {
		t.Errorf("ScoreSimplified composite: got %.2f, want %.2f (legacy 2-dim)", got.Composite, want)
	}
}

// ── StratifyByChannelQuality ─────────────────────────────────────

func TestStratifyByChannelQuality(t *testing.T) {
	scored := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, CanonicalName: "official"},
			Breakdown: ScoringBreakdown{ChannelQuality: 95, Composite: 80}},
		{Candidate: Candidate{CredentialID: 2, CanonicalName: "self_host"},
			Breakdown: ScoringBreakdown{ChannelQuality: 75, Composite: 70}},
		{Candidate: Candidate{CredentialID: 3, CanonicalName: "aggregator_low"},
			Breakdown: ScoringBreakdown{ChannelQuality: 40, Composite: 60}},
		{Candidate: Candidate{CredentialID: 4, CanonicalName: "relay_bad"},
			Breakdown: ScoringBreakdown{ChannelQuality: 20, Composite: 55}},
	}

	preferred, fallback := StratifyByChannelQuality(scored)
	if len(preferred) != 2 {
		t.Errorf("preferred count: got %d, want 2", len(preferred))
	}
	if len(fallback) != 2 {
		t.Errorf("fallback count: got %d, want 2", len(fallback))
	}
	for _, p := range preferred {
		if p.Breakdown.ChannelQuality < ChannelQualityPreferredThreshold {
			t.Errorf("preferred should have ChannelQuality >= 50, got %.2f", p.Breakdown.ChannelQuality)
		}
	}
	for _, f := range fallback {
		if f.Breakdown.ChannelQuality >= ChannelQualityPreferredThreshold {
			t.Errorf("fallback should have ChannelQuality < 50, got %.2f", f.Breakdown.ChannelQuality)
		}
	}
}

// ── IsPreferredChannelSaturated ──────────────────────────────────

func TestIsPreferredChannelSaturated(t *testing.T) {
	// 空 preferred → 视为饱和
	if !IsPreferredChannelSaturated(nil) {
		t.Errorf("empty preferred should be saturated")
	}

	// 至少一个不饱和 → 不饱和
	notSaturated := []ScoredCandidate{
		{Candidate: Candidate{PressureRatio: 0.5}},
		{Candidate: Candidate{PressureRatio: 0.99}},
	}
	if IsPreferredChannelSaturated(notSaturated) {
		t.Errorf("mixed should not be saturated")
	}

	// 全部 >= 0.95 → 饱和
	saturated := []ScoredCandidate{
		{Candidate: Candidate{PressureRatio: 0.95}},
		{Candidate: Candidate{PressureRatio: 1.0}},
	}
	if !IsPreferredChannelSaturated(saturated) {
		t.Errorf("all >= 0.95 should be saturated")
	}
}

// ── ApplyFallbackDemotion ────────────────────────────────────────

func TestApplyFallbackDemotion(t *testing.T) {
	scored := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1}, Breakdown: ScoringBreakdown{Composite: 100}},
		{Candidate: Candidate{CredentialID: 2}, Breakdown: ScoringBreakdown{Composite: 50}},
	}
	ApplyFallbackDemotion(scored, 0.5)
	if scored[0].Breakdown.Composite != 50 {
		t.Errorf("composite 100 * 0.5 = 50, got %.2f", scored[0].Breakdown.Composite)
	}
	if scored[1].Breakdown.Composite != 25 {
		t.Errorf("composite 50 * 0.5 = 25, got %.2f", scored[1].Breakdown.Composite)
	}
}

// ── stratifyAndPickTopN：核心路由流程 ────────────────────────────

func TestStratifyAndPickTopN_PreferredFull(t *testing.T) {
	// preferred 池足够（>= topN）→ 只用 preferred
	scored := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, CanonicalName: "official-1"},
			Breakdown: ScoringBreakdown{ChannelQuality: 95, Composite: 90, Reliability: 95, PriceScore: 50}},
		{Candidate: Candidate{CredentialID: 2, CanonicalName: "official-2"},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 80, Reliability: 90, PriceScore: 60}},
		{Candidate: Candidate{CredentialID: 3, CanonicalName: "nvidia-free"},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 85, Reliability: 70, PriceScore: 100}},
	}

	got := stratifyAndPickTopN(scored, 2)
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	for _, sc := range got {
		if sc.Breakdown.ChannelQuality < ChannelQualityPreferredThreshold {
			t.Errorf("result should all be preferred, got ChannelQuality=%.2f", sc.Breakdown.ChannelQuality)
		}
	}
}

func TestStratifyAndPickTopN_FallbackDemoted(t *testing.T) {
	// preferred 池只有 1 个（< topN=3），fallback 补足 + 降权
	scored := []ScoredCandidate{
		// preferred
		{Candidate: Candidate{CredentialID: 1, PressureRatio: 0.3},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 80, Reliability: 90, PriceScore: 50}},
		// fallback（NVIDIA NIM 风格：免费 + 不可靠）
		{Candidate: Candidate{CredentialID: 2, IsFree: true},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 75, Reliability: 70, PriceScore: 100}},
		{Candidate: Candidate{CredentialID: 3, IsFree: true},
			Breakdown: ScoringBreakdown{ChannelQuality: 25, Composite: 70, Reliability: 65, PriceScore: 100}},
	}

	got := stratifyAndPickTopN(scored, 3)
	if len(got) != 3 {
		t.Fatalf("len: got %d, want 3", len(got))
	}

	// 第一个必须是 preferred（ChannelQuality=90）
	if got[0].Candidate.CredentialID != 1 {
		t.Errorf("first should be preferred (cred 1), got cred %d", got[0].Candidate.CredentialID)
	}
	// fallback 的 composite 已经被 demoted
	// original 75 * 0.5 = 37.5
	// original 70 * 0.5 = 35
	for _, sc := range got[1:] {
		if sc.Candidate.CredentialID == 2 && sc.Breakdown.Composite != 37.5 {
			t.Errorf("cred 2 demoted composite: got %.2f, want 37.5", sc.Breakdown.Composite)
		}
		if sc.Candidate.CredentialID == 3 && sc.Breakdown.Composite != 35 {
			t.Errorf("cred 3 demoted composite: got %.2f, want 35", sc.Breakdown.Composite)
		}
	}

	// 排序后：preferred (80) > demoted fallback (37.5) > demoted fallback (35)
	composites := []float64{got[0].Breakdown.Composite, got[1].Breakdown.Composite, got[2].Breakdown.Composite}
	if !sort.Float64sAreSorted([]float64{composites[2], composites[1], composites[0]}) {
		t.Errorf("composites should be descending, got %v", composites)
	}
}

func TestStratifyAndPickTopN_PreferredSaturated_RelaxDemotion(t *testing.T) {
	// preferred 池全饱和（PressureRatio=0.95+）→ demotion 放宽到 0.85
	scored := []ScoredCandidate{
		// preferred 但饱和
		{Candidate: Candidate{CredentialID: 1, PressureRatio: 1.0},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 80, Reliability: 90, PriceScore: 50}},
		// fallback
		{Candidate: Candidate{CredentialID: 2, IsFree: true},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 75, Reliability: 70, PriceScore: 100}},
	}

	got := stratifyAndPickTopN(scored, 2)
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}

	// fallback 已经被 demoted 0.85（不是 0.5）
	for _, sc := range got {
		if sc.Candidate.CredentialID == 2 {
			want := 75.0 * 0.85
			if diff := sc.Breakdown.Composite - want; diff < -0.01 || diff > 0.01 {
				t.Errorf("saturated demotion: got %.2f, want %.2f", sc.Breakdown.Composite, want)
			}
		}
	}
}

func TestStratifyAndPickTopN_TieBreaker(t *testing.T) {
	// 同 composite 时，Reliability 高者胜
	scored := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, CanonicalName: "low-reliability"},
			Breakdown: ScoringBreakdown{ChannelQuality: 50, Composite: 60, Reliability: 30, PriceScore: 50}},
		{Candidate: Candidate{CredentialID: 2, CanonicalName: "high-reliability"},
			Breakdown: ScoringBreakdown{ChannelQuality: 50, Composite: 60, Reliability: 90, PriceScore: 50}},
	}

	got := stratifyAndPickTopN(scored, 1)
	if got[0].Candidate.CanonicalName != "high-reliability" {
		t.Errorf("tie-breaker should prefer higher reliability, got %s", got[0].Candidate.CanonicalName)
	}
}

// ── 端到端：业务场景 ─────────────────────────────────────────────

// TestChannelQualityRouting_MinimaxBeatsNvidiaNim 验证业务核心诉求：
// 同价时，Minimax 官方渠道 > NVIDIA NIM 免费凭据。
func TestChannelQualityRouting_MinimaxBeatsNvidiaNim(t *testing.T) {
	minimax := Candidate{
		CredentialID:      1,
		CanonicalID:       1,
		CanonicalName:     "minimax-m2",
		ProviderCategory:  "official",
		SuccessRate:       0.97,
		P95LatencyMs:      1500,
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
		TaskMatchScore:    0.8,
		Tags:              []string{"reasoning", "code"},
	}
	nvidiaNim := Candidate{
		CredentialID:      2,
		CanonicalID:       2,
		CanonicalName:     "nvidia-nim-free",
		ProviderCategory:  "aggregator",
		BillingMode:       "free",
		SuccessRate:       0.78, // 高错误率
		P95LatencyMs:      6500, // 高延迟
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30, // 同价
		TaskMatchScore:    0.8,
		Tags:              []string{"reasoning", "code"},
		IsFree:            true,
	}

	avgPrices := map[int]float64{1: 40, 2: 40}
	sMinimax := ScoreWithChannelQuality(minimax, TaskCode, avgPrices, 0)
	sNvidia := ScoreWithChannelQuality(nvidiaNim, TaskCode, avgPrices, 0)

	// 业务断言 1：Minimax 应该是 Preferred（>= 50）
	if sMinimax.ChannelQuality < 50 {
		t.Errorf("Minimax official should be preferred, got ChannelQuality=%.2f", sMinimax.ChannelQuality)
	}
	// 业务断言 2：NVIDIA NIM 应该是 Fallback（< 50）
	if sNvidia.ChannelQuality >= 50 {
		t.Errorf("NVIDIA NIM free+unreliable should be fallback, got ChannelQuality=%.2f", sNvidia.ChannelQuality)
	}
	// 业务断言 3：同价下，Minimax composite 应当更高
	if sMinimax.Composite <= sNvidia.Composite {
		t.Errorf("Minimax composite (%.2f) should beat NVIDIA NIM (%.2f) at same price",
			sMinimax.Composite, sNvidia.Composite)
	}

	// 业务断言 4：池分层后，Minimax 一定胜出
	scored := []ScoredCandidate{
		{Candidate: minimax, Breakdown: sMinimax},
		{Candidate: nvidiaNim, Breakdown: sNvidia},
	}
	got := stratifyAndPickTopN(scored, 1)
	if got[0].Candidate.CredentialID != 1 {
		t.Errorf("expected Minimax to win, got cred %d", got[0].Candidate.CredentialID)
	}
}

// TestChannelQualityRouting_FallbackUsedWhenSaturated 验证主渠道饱和时
// fallback 池能补足（demotion 放宽到 0.85）。
//
// 修复（audit #4）：freeBackup 必须是真正的 fallback（ChannelQuality < 50），
// 否则该测试只是验证 preferred 池内部的排序，没有覆盖 fallback 路径。
func TestChannelQualityRouting_FallbackUsedWhenSaturated(t *testing.T) {
	saturated := Candidate{
		CredentialID:      1,
		CanonicalID:       1,
		CanonicalName:     "minimax-saturated",
		ProviderCategory:  "official",
		SuccessRate:       0.95,
		P95LatencyMs:      1500,
		PressureRatio:     1.0, // 满载
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
		TaskMatchScore:    0.8,
	}
	// 真正的 fallback：success < 0.90 → -25 demotion → 60-25=35 < 50
	freeBackup := Candidate{
		CredentialID:      2,
		CanonicalID:       2,
		CanonicalName:     "nvidia-backup",
		ProviderCategory:  "aggregator",
		BillingMode:       "free",
		SuccessRate:       0.85, // < 0.90 触发 free+unreliable demotion
		P95LatencyMs:      2000,
		UnitPriceInPer1M:  10,
		UnitPriceOutPer1M: 30,
		TaskMatchScore:    0.8,
		IsFree:            true,
	}

	avgPrices := map[int]float64{1: 40, 2: 40}
	sSat := ScoreWithChannelQuality(saturated, TaskCode, avgPrices, 0)
	sFree := ScoreWithChannelQuality(freeBackup, TaskCode, avgPrices, 0)

	// 验证：freeBackup 确实落入 fallback 池
	if sFree.ChannelQuality >= ChannelQualityPreferredThreshold {
		t.Fatalf("freeBackup should be in fallback pool, got ChannelQuality=%.2f", sFree.ChannelQuality)
	}
	// 验证：saturated 是 preferred
	if sSat.ChannelQuality < ChannelQualityPreferredThreshold {
		t.Fatalf("saturated should be in preferred pool, got ChannelQuality=%.2f", sSat.ChannelQuality)
	}

	scored := []ScoredCandidate{
		{Candidate: saturated, Breakdown: sSat},
		{Candidate: freeBackup, Breakdown: sFree},
	}
	got := stratifyAndPickTopN(scored, 2)
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	// saturated 是 preferred，freeBackup 是 fallback
	// 主渠道饱和（PressureRatio=1.0）→ factor=0.85
	found := false
	for _, sc := range got {
		if sc.Candidate.CredentialID == 2 {
			found = true
			want := sFree.Composite * 0.85
			if diff := sc.Breakdown.Composite - want; diff < -0.01 || diff > 0.01 {
				t.Errorf("saturated fallback demotion: got %.2f, want %.2f (orig %.2f * 0.85)",
					sc.Breakdown.Composite, want, sFree.Composite)
			}
		}
	}
	if !found {
		t.Errorf("freeBackup (fallback) should still be selected when preferred is saturated")
	}
}

// TestChannelQualityRouting_EmptyPreferredNoDemotion 验证当所有候选都
// 落入 fallback（冷启动 / provider_category 全部为空）时，**不**施加
// demotion（factor = 1.0）。这是对 BUG #2 的回归测试。
func TestChannelQualityRouting_EmptyPreferredNoDemotion(t *testing.T) {
	// 所有候选都是 fallback（ChannelQuality < 50）
	scored := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, CanonicalName: "fb-1"},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 80, Reliability: 80, PriceScore: 60}},
		{Candidate: Candidate{CredentialID: 2, CanonicalName: "fb-2"},
			Breakdown: ScoringBreakdown{ChannelQuality: 25, Composite: 75, Reliability: 75, PriceScore: 55}},
		{Candidate: Candidate{CredentialID: 3, CanonicalName: "fb-3"},
			Breakdown: ScoringBreakdown{ChannelQuality: 20, Composite: 70, Reliability: 70, PriceScore: 50}},
	}

	got := stratifyAndPickTopN(scored, 3)
	if len(got) != 3 {
		t.Fatalf("len: got %d, want 3", len(got))
	}
	// 所有 composite 必须保持不变（factor = 1.0）
	for _, sc := range got {
		// 找到原始 composite
		var orig float64
		for _, s := range scored {
			if s.Candidate.CredentialID == sc.Candidate.CredentialID {
				orig = s.Breakdown.Composite
				break
			}
		}
		if diff := sc.Breakdown.Composite - orig; diff < -0.01 || diff > 0.01 {
			t.Errorf("empty preferred: composite should be unchanged, got %.2f, want %.2f",
				sc.Breakdown.Composite, orig)
		}
	}
}

// TestChannelQualityRouting_ThreeWayTieBreak 验证 3 路 tie-break 顺序：
// Composite > Reliability > PriceScore。
func TestChannelQualityRouting_ThreeWayTieBreak(t *testing.T) {
	scored := []ScoredCandidate{
		// Composite=60, Reliability=30, PriceScore=80 → 最差
		{Candidate: Candidate{CredentialID: 1, CanonicalName: "comp-only"},
			Breakdown: ScoringBreakdown{ChannelQuality: 50, Composite: 60, Reliability: 30, PriceScore: 80}},
		// Composite=60, Reliability=90, PriceScore=30 → 中（reliability 胜）
		{Candidate: Candidate{CredentialID: 2, CanonicalName: "comp-rel"},
			Breakdown: ScoringBreakdown{ChannelQuality: 50, Composite: 60, Reliability: 90, PriceScore: 30}},
		// Composite=60, Reliability=90, PriceScore=50 → 最佳
		{Candidate: Candidate{CredentialID: 3, CanonicalName: "comp-rel-price"},
			Breakdown: ScoringBreakdown{ChannelQuality: 50, Composite: 60, Reliability: 90, PriceScore: 50}},
	}

	got := stratifyAndPickTopN(scored, 1)
	if len(got) != 1 {
		t.Fatalf("len: got %d, want 1", len(got))
	}
	if got[0].Candidate.CanonicalName != "comp-rel-price" {
		t.Errorf("3-way tie-break should pick highest price (comp-rel-price), got %s", got[0].Candidate.CanonicalName)
	}
}
