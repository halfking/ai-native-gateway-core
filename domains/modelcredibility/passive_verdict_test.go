package modelcredibility

import (
	"testing"
)

// passive_verdict_test.go — 维度 5（被动时序指纹）接入判定引擎的契约门。
//
// 钉四条语义：
//  1. PassiveResult=nil ⇒ 不计维（冷启动常态，不是异常）；
//  2. Confidence < PassiveMinConfidence ⇒ 不计维（基线没立起来，分数是噪音）；
//  3. 过门 ⇒ 计维进综合分，权重走 SubScoreWeights[DimPassive]；
//  4. 被动异常逐条透传，且低分能把 verdict 压到 suspected/diluted 档。
//
// 判据纪律：期望综合分用 SubScoreWeights 动态推导（不写死权重字面量，
// 配比调整不破门——与 TestComputeOverallExcludesMissingDimensions 同款）。

func passiveTestInput() Input {
	return Input{
		Target: NodeTarget{
			CredentialID:  31,
			ProviderID:    587,
			RawModelName:  "claude-opus-5-5",
			CanonicalID:   42,
			CanonicalName: "claude-opus-5-5",
		},
		Observed: Observed{
			ReportedModel: "claude-opus-5-5",
			HTTPStatus:    200,
			Content:       "ready",
			PromptTokens:  9,
			HasUsage:      true,
		},
	}
}

// ① nil ⇒ 不计维。
func TestPassiveDimensionNilNotMeasured(t *testing.T) {
	in := passiveTestInput()
	res := Evaluate(in, DefaultConfig())
	if res.Scores.Measured.Has(DimPassive) {
		t.Fatalf("PassiveResult=nil 却计了被动维（measured=%b）", res.Scores.Measured)
	}
	for _, a := range res.Anomalies {
		if a == AnomalyPassiveNoData {
			t.Fatalf("nil 被动结果不该产生异常项（冷启动常态），got %v", res.Anomalies)
		}
	}
}

// ② 低置信 ⇒ 不计维。
func TestPassiveDimensionLowConfidenceNotMeasured(t *testing.T) {
	in := passiveTestInput()
	in.PassiveResult = &PassiveVerdict{
		OverallScore: 10, // 极差分数，但基线只有 5 个样本
		Confidence:   0.05,
		Anomalies:    []string{AnomalyPassiveITTMismatch},
	}
	res := Evaluate(in, DefaultConfig())
	if res.Scores.Measured.Has(DimPassive) {
		t.Fatalf("置信度 %v < %v 仍计维", in.PassiveResult.Confidence, PassiveMinConfidence)
	}
	// 异常也不透传（分数没进判定，异常不该跟进来吓人）。
	for _, a := range res.Anomalies {
		if a == AnomalyPassiveITTMismatch {
			t.Fatalf("低置信时异常不该透传，got %v", res.Anomalies)
		}
	}
}

// ③ 过门 ⇒ 计维，综合分按权重合成。
func TestPassiveDimensionMeasuredContributes(t *testing.T) {
	in := passiveTestInput()
	in.PassiveResult = &PassiveVerdict{
		OverallScore: 90,
		Confidence:   1.0,
	}
	res := Evaluate(in, DefaultConfig())
	if !res.Scores.Measured.Has(DimPassive) {
		t.Fatal("过置信门却没有计被动维")
	}
	if res.Scores.Passive != 90 {
		t.Fatalf("Scores.Passive = %v, want 90", res.Scores.Passive)
	}
	// 综合分验证：身份 100 + 被动 90 两维（其余缺维），按权重归一。
	want := 100*SubScoreWeights[DimIdentity] + 90*SubScoreWeights[DimPassive]
	want /= SubScoreWeights[DimIdentity] + SubScoreWeights[DimPassive]
	if diff := res.Overall - want; diff < -0.01 || diff > 0.01 {
		t.Fatalf("Overall = %v, want %v（按有数据维度归一）", res.Overall, want)
	}
}

// ④ 被动异常透传 + 低分压 verdict。
func TestPassiveDimensionAnomaliesSurfaceAndDegrade(t *testing.T) {
	in := passiveTestInput()
	in.PassiveResult = &PassiveVerdict{
		OverallScore: 20,
		Confidence:   1.0,
		Anomalies: []string{
			AnomalyPassiveITTMismatch,
			AnomalyPassiveTTFTAnomaly,
		},
	}
	res := Evaluate(in, DefaultConfig())

	seen := map[string]bool{}
	for _, a := range res.Anomalies {
		seen[a] = true
	}
	if !seen[AnomalyPassiveITTMismatch] || !seen[AnomalyPassiveTTFTAnomaly] {
		t.Fatalf("被动异常未透传，got %v", res.Anomalies)
	}
	// 身份 100（自报一致）+ 被动 20 ⇒ 综合 = (100*wI + 20*wP)/(wI+wP)。
	want := 100*SubScoreWeights[DimIdentity] + 20*SubScoreWeights[DimPassive]
	want /= SubScoreWeights[DimIdentity] + SubScoreWeights[DimPassive]
	if diff := res.Overall - want; diff < -0.01 || diff > 0.01 {
		t.Fatalf("Overall = %v, want %v（按有数据维度归一）", res.Overall, want)
	}
	if res.Overall >= 100-0.01 {
		t.Fatal("被动 20 分却综合 ~100 —— 低分没有进综合分")
	}
	// verdict 只允许 suspected 及以上（不可能 verified：总分 < VerifiedMinScore）。
	if res.Verdict != VerdictUnknown && res.Verdict != VerdictSuspected {
		t.Fatalf("Verdict = %v, want unknown/suspected（被动低分 + 单轮）", res.Verdict)
	}
}
