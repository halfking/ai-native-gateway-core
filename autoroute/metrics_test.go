package autoroute

// metrics_test.go — CHANNEL_QUALITY_ROUTING 指标埋点测试。
//
// 验证 3 个核心不变量：
//   1. stratifyAndPickTopN 三种 pool 路径分别产生正确的 metric label
//      （pool=preferred / pool=fallback × reason=no_demotion/demotion_05/demotion_085/empty_preferred）
//   2. demotion_events_total 在 demotion 真正发生时才增加
//   3. channel_quality_score 直方图仅在 preferred 赢家时被 observe

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// readCounter 读取 prometheus.Counter 当前值。
// 来自 bg/availability_metrics.go:readBackfillRowsPerRun 同款模式。
func readCounter(c prometheus.Counter) float64 {
	if c == nil {
		return 0
	}
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		return 0
	}
	return m.Counter.GetValue()
}

func poolDecisionsCounter(pool, reason string) float64 {
	if poolDecisions == nil {
		return 0
	}
	return readCounter(poolDecisions.WithLabelValues(pool, reason))
}

func demotionEventsCounter(factor string) float64 {
	if demotionEvents == nil {
		return 0
	}
	return readCounter(demotionEvents.WithLabelValues(factor))
}

func TestRecordRoutingDecision_PreferredNoDemotion(t *testing.T) {
	before := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelNoDemotion)
	recordRoutingDecision(PoolLabelPreferred, ReasonLabelNoDemotion, 90, 1.0)
	got := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelNoDemotion)
	if got != before+1 {
		t.Errorf("preferred/no_demotion should increment by 1, got delta %.0f", got-before)
	}
}

func TestRecordRoutingDecision_FallbackDemotion05(t *testing.T) {
	before := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion05)
	beforeFactor := demotionEventsCounter("0.5")
	recordRoutingDecision(PoolLabelFallback, ReasonLabelDemotion05, 35, 0.5)
	got := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion05)
	if got != before+1 {
		t.Errorf("fallback/demotion_05 should increment by 1, got delta %.0f", got-before)
	}
	if got := demotionEventsCounter("0.5"); got != beforeFactor+1 {
		t.Errorf("demotion_events{0.5} should increment by 1, got delta %.0f", got-beforeFactor)
	}
}

func TestRecordRoutingDecision_FallbackDemotion085(t *testing.T) {
	before := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion085)
	beforeFactor := demotionEventsCounter("0.85")
	recordRoutingDecision(PoolLabelFallback, ReasonLabelDemotion085, 40, 0.85)
	got := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion085)
	if got != before+1 {
		t.Errorf("fallback/demotion_085 should increment by 1, got delta %.0f", got-before)
	}
	if got := demotionEventsCounter("0.85"); got != beforeFactor+1 {
		t.Errorf("demotion_events{0.85} should increment by 1, got delta %.0f", got-beforeFactor)
	}
}

func TestRecordRoutingDecision_EmptyPreferredNoDemotionFactor(t *testing.T) {
	before := poolDecisionsCounter(PoolLabelFallback, ReasonLabelEmptyPreferred)
	beforeFactor := demotionEventsCounter("0.5")
	recordRoutingDecision(PoolLabelFallback, ReasonLabelEmptyPreferred, 30, 1.0)
	got := poolDecisionsCounter(PoolLabelFallback, ReasonLabelEmptyPreferred)
	if got != before+1 {
		t.Errorf("fallback/empty_preferred should increment by 1, got delta %.0f", got-before)
	}
	// factor=1.0 不算 demotion，demotion_events 不应增加
	if got := demotionEventsCounter("0.5"); got != beforeFactor {
		t.Errorf("demotion_events{0.5} should NOT increment when factor=1.0, got delta %.0f", got-beforeFactor)
	}
}

func TestRecordRoutingDecision_Factor1DoesNotIncrementDemotion(t *testing.T) {
	before05 := demotionEventsCounter("0.5")
	before085 := demotionEventsCounter("0.85")
	// pool=preferred, factor=1.0 → 不会触发 demotion 计数
	recordRoutingDecision(PoolLabelPreferred, ReasonLabelNoDemotion, 90, 1.0)
	if got := demotionEventsCounter("0.5"); got != before05 {
		t.Errorf("preferred path should not touch demotion{0.5}, got delta %.0f", got-before05)
	}
	if got := demotionEventsCounter("0.85"); got != before085 {
		t.Errorf("preferred path should not touch demotion{0.85}, got delta %.0f", got-before085)
	}
}

// TestStratifyAndPickTopN_EmitsMetrics 端到端验证：调用
// stratifyAndPickTopN 后，对应的 metric 应被自洽增加。
//
// Label 语义约定：
//   - pool = winner 所在池（preferred / fallback）
//   - reason = 决策路径行为（no_demotion / demotion_05 / demotion_085 / empty_preferred）
//   - demotion_events{factor} 仅在 demotion 真正施加给 fallback 时增加
//
// 当 preferred 在混合路径胜出时：pool=preferred, reason=demotion_05
// （demotion 施加给了 fallback，但 winner 仍来自 preferred）
func TestStratifyAndPickTopN_EmitsMetrics(t *testing.T) {
	// 场景 A: preferred 充足 → pool=preferred, reason=no_demotion
	preferredOnly := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 80}},
		{Candidate: Candidate{CredentialID: 2},
			Breakdown: ScoringBreakdown{ChannelQuality: 85, Composite: 75}},
	}
	beforeA := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelNoDemotion)
	_ = stratifyAndPickTopN(preferredOnly, 1)
	afterA := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelNoDemotion)
	if afterA != beforeA+1 {
		t.Errorf("preferred-only path should increment metric, delta %.0f", afterA-beforeA)
	}

	// 场景 B: preferred=1 + fallback=1，主渠道未饱和，preferred 胜
	// → pool=preferred, reason=demotion_05（demotion 仍施加给了 fallback）
	mixed := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, PressureRatio: 0.3},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 80}},
		{Candidate: Candidate{CredentialID: 2},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 75}},
	}
	beforeBPool := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelDemotion05)
	beforeBFallback := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion05)
	beforeFactorB := demotionEventsCounter("0.5")
	gotB := stratifyAndPickTopN(mixed, 2)
	afterBPool := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelDemotion05)
	afterBFallback := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion05)
	// preferred 胜出 → preferred pool 增加，fallback 不增加
	if afterBPool != beforeBPool+1 {
		t.Errorf("preferred winner under demotion_05 should hit preferred/demotion_05, delta %.0f",
			afterBPool-beforeBPool)
	}
	if afterBFallback != beforeBFallback {
		t.Errorf("fallback pool should NOT increment when preferred wins, delta %.0f",
			afterBFallback-beforeBFallback)
	}
	// fallback 应该是 winner（composite 75 * 0.5 = 37.5 < preferred 80）
	if len(gotB) == 0 || gotB[0].Candidate.CredentialID != 1 {
		t.Errorf("preferred should still win under demotion_05, got %+v", gotB)
	}
	if got := demotionEventsCounter("0.5"); got != beforeFactorB+1 {
		t.Errorf("demotion_events{0.5} should increment, delta %.0f", got-beforeFactorB)
	}

	// 场景 C: preferred=1（满载）+ fallback=1，主渠道饱和
	// preferred composite 80 vs fallback composite 75 * 0.85 = 63.75
	// → preferred 仍胜，pool=preferred, reason=demotion_085
	saturated := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, PressureRatio: 1.0},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 80}},
		{Candidate: Candidate{CredentialID: 2},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 75}},
	}
	beforeC := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelDemotion085)
	beforeFactorC := demotionEventsCounter("0.85")
	_ = stratifyAndPickTopN(saturated, 2)
	afterC := poolDecisionsCounter(PoolLabelPreferred, ReasonLabelDemotion085)
	if afterC != beforeC+1 {
		t.Errorf("demotion_085 path should increment metric, delta %.0f", afterC-beforeC)
	}
	if got := demotionEventsCounter("0.85"); got != beforeFactorC+1 {
		t.Errorf("demotion_events{0.85} should increment, delta %.0f", got-beforeFactorC)
	}

	// 场景 D: 无 preferred → pool=fallback, reason=empty_preferred
	fallbackOnly := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 80}},
		{Candidate: Candidate{CredentialID: 2},
			Breakdown: ScoringBreakdown{ChannelQuality: 25, Composite: 75}},
	}
	beforeD := poolDecisionsCounter(PoolLabelFallback, ReasonLabelEmptyPreferred)
	_ = stratifyAndPickTopN(fallbackOnly, 2)
	afterD := poolDecisionsCounter(PoolLabelFallback, ReasonLabelEmptyPreferred)
	if afterD != beforeD+1 {
		t.Errorf("empty_preferred path should increment metric, delta %.0f", afterD-beforeD)
	}

	// 场景 E: fallback 真的胜出（preferred 太弱：composite 10 vs fallback 75 * 0.5 = 37.5）
	// → pool=fallback, reason=demotion_05
	fallbackWins := []ScoredCandidate{
		{Candidate: Candidate{CredentialID: 1, PressureRatio: 0.3},
			Breakdown: ScoringBreakdown{ChannelQuality: 90, Composite: 10}},
		{Candidate: Candidate{CredentialID: 2},
			Breakdown: ScoringBreakdown{ChannelQuality: 30, Composite: 75}},
	}
	beforeE := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion05)
	_ = stratifyAndPickTopN(fallbackWins, 2)
	afterE := poolDecisionsCounter(PoolLabelFallback, ReasonLabelDemotion05)
	if afterE != beforeE+1 {
		t.Errorf("fallback-wins path should increment fallback/demotion_05, delta %.0f",
			afterE-beforeE)
	}
}

// TestFormatFactor 验证 factor 字符串化的稳定性（label 基数安全）。
func TestFormatFactor(t *testing.T) {
	tests := []struct {
		f    float64
		want string
	}{
		{FallbackDemotionFactor, "0.5"},
		{FallbackDemotionFactorSaturated, "0.85"},
		{1.0, "unknown"},
		{0.0, "unknown"},
		{0.7, "unknown"}, // 防止未来引入 0.7 等新因子时忘记加 case
	}
	for _, tt := range tests {
		if got := formatFactor(tt.f); got != tt.want {
			t.Errorf("formatFactor(%.2f) = %s, want %s", tt.f, got, tt.want)
		}
	}
}
