package autoroute

import (
	"context"
	"testing"
	"time"
)

// 2026-09-28 live auto-routing audit.
//
// Defect: requiredTagsForTask is written against a capability vocabulary
// (code / programming / creative / writing / classification / review /
// security / planning / analysis / math / logic) that models_canonical.tags
// does not provide. The live library publishes cap:long-context,
// cap:tool-use, cap:function-call, cap:reasoning, cap:vision, modality:* and
// family:* / version:* — and no code / creative / writing capability tag at
// all (950 canonical models; the operator-annotation `strengths` column is
// 0/950 populated, so there is no richer signal to fall back on).
//
// Consequence measured on the running gateway with the 240-case E2E suite:
// 124/240 cases (code 49, creative 31, intent_classification 20, code_audit
// 12, vision 12) reported fallback_used=true with a single candidate whose
// match score was 0 — every one of them resolving to the same popularity pick.
// Those 124 are also exactly the cases that returned HTTP 429, because a
// one-candidate pool has no failover ladder left when that model rate-limits.

func TestTaskVocabularyRepresented_MatchesLiveTaxonomy(t *testing.T) {
	// Tags exactly as they appear in the live library.
	liveCapable := []Candidate{
		{CanonicalName: "reasoner", Tags: []string{"cap:reasoning", "modality:text"}},
		{CanonicalName: "coder", Tags: []string{"cap:tool-use", "cap:function-call", "modality:text"}},
		{CanonicalName: "reader", Tags: []string{"cap:long-context", "modality:text"}},
		{CanonicalName: "seer", Tags: []string{"cap:vision", "modality:multimodal"}},
	}

	tests := []struct {
		task      TaskType
		want      bool
		whyForNow string
	}{
		{TaskReasoning, true, "cap:reasoning is published"},
		{TaskAgent, true, "cap:tool-use + cap:function-call are published"},
		{TaskFunctionCall, true, "cap:function-call + cap:tool-use are published"},
		{TaskVision, true, "cap:vision + modality:multimodal are published"},
		{TaskChat, true, "no tags required — trivially represented"},
		{TaskCode, false, "no code/programming capability tag exists in the library"},
		{TaskCodeAudit, false, "no code/review/security capability tag exists"},
		{TaskCreative, false, "no creative/writing capability tag exists"},
		{TaskIntentClassification, false, "no classification capability tag exists"},
	}

	for _, tc := range tests {
		if got := TaskVocabularyRepresented(tc.task, liveCapable); got != tc.want {
			t.Errorf("%s: TaskVocabularyRepresented = %v, want %v (%s)",
				tc.task, got, tc.want, tc.whyForNow)
		}
	}
}

func TestTaskVocabularyRepresented_EmptyPoolAndUntaggedModels(t *testing.T) {
	// An empty pool and a pool of untagged models both report "absent" for a
	// tag-bearing task, so callers can rely on the single bool either way.
	if TaskVocabularyRepresented(TaskCode, nil) != false {
		t.Error("empty pool should report an unrepresented vocabulary")
	}
	untagged := []Candidate{{CanonicalName: "m1"}, {CanonicalName: "m2"}}
	if TaskVocabularyRepresented(TaskCode, untagged) != false {
		t.Error("untagged pool should report an unrepresented vocabulary")
	}
}

// The absent-vocabulary score must be the neutral "unknown" value, never 0.
// Returning 0 is what made a taxonomy gap indistinguishable from a real
// capability verdict and triggered the popularity collapse.
func TestTaskMatchScore_AbsentVocabularyIsNotZero(t *testing.T) {
	noCodeTags := []string{"cap:reasoning", "modality:text", "family:minimax"}
	if got := TaskMatchScore(TaskCode, noCodeTags); got != 0 {
		t.Errorf("raw TaskMatchScore(code) = %v, want 0 (library has no code tag)", got)
	}
	if TaskMatchScoreUnknown == 0 {
		t.Fatal("TaskMatchScoreUnknown must be non-zero: 0 reads as a capability verdict")
	}
	if TaskMatchScoreUnknown != 0.5 {
		t.Errorf("TaskMatchScoreUnknown = %v, want 0.5 (same neutral score chat already uses)", TaskMatchScoreUnknown)
	}
}

// Regression pin for the headline defect: a code task over a live-shaped pool
// must keep its scored candidates instead of collapsing to one popularity pick.
func TestRecommendV2_AbsentVocabularyKeepsScoredPool(t *testing.T) {
	now := time.Now()
	idx := &Index{
		entries: []Candidate{
			{CanonicalID: 1, CanonicalName: "alpha", CredentialID: 11, Tier: "primary", PopularityScore: 90,
				Tags: []string{"cap:reasoning", "modality:text"}, SuccessRate: 0.95, P95LatencyMs: 900,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 2, CanonicalName: "beta", CredentialID: 12, Tier: "primary", PopularityScore: 80,
				Tags: []string{"cap:tool-use", "modality:text"}, SuccessRate: 0.93, P95LatencyMs: 1100,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 3, CanonicalName: "gamma", CredentialID: 13, Tier: "primary", PopularityScore: 70,
				Tags: []string{"cap:long-context", "modality:text"}, SuccessRate: 0.91, P95LatencyMs: 1200,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
		},
		lastRefresh: now,
	}

	got := idx.RecommendV2(context.Background(), TaskCode, ClassificationSignals{EstimatedTokens: 500}, ProfileSmart, "", 3)

	if len(got) == 0 {
		t.Fatal("code task returned no candidates")
	}
	if isFallbackWinner(got) {
		t.Fatalf("code task collapsed to the 48h popularity fallback: %+v", got[0].Candidate.CanonicalName)
	}
	if len(got) < 2 {
		t.Fatalf("expected the scored pool to survive (>=2 candidates so a rate-limited winner can fail over), got %d: %+v",
			len(got), got[0].Candidate.CanonicalName)
	}
	for _, sc := range got {
		if sc.Breakdown.MatchScore == 0 {
			t.Errorf("candidate %q kept a 0 match score; absent vocabulary must be neutralised, not left at 0",
				sc.Candidate.CanonicalName)
		}
	}
}

// The collapse must still fire when it is genuinely warranted: the library
// *does* speak this task's language, but nothing in the pool reaches the
// threshold. That is the "nothing suitable" case the guard exists to preserve.
func TestRecommendV2_DiscriminatingLowMatchStillFallsBack(t *testing.T) {
	now := time.Now()
	// TaskCodeAudit requires code/review/security. The pool carries none of
	// them, so this task's vocabulary is absent too — use a task whose
	// vocabulary IS present but only partially satisfied instead, so
	// vocabularyPresent is true while the winner still scores below 30.
	// reasoning requires reasoning+math+logic; cap:reasoning alone → 1/3.
	idx := &Index{
		entries: []Candidate{
			{CanonicalID: 1, CanonicalName: "weak", CredentialID: 21, Tier: "primary", PopularityScore: 90,
				Tags: []string{"cap:reasoning", "modality:text"}, SuccessRate: 0.90, P95LatencyMs: 1500,
				ProviderCategory: "official", ReleasedAt: &now},
			{CanonicalID: 2, CanonicalName: "weaker", CredentialID: 22, Tier: "primary", PopularityScore: 80,
				Tags: []string{"cap:reasoning", "modality:text"}, SuccessRate: 0.88, P95LatencyMs: 1600,
				ProviderCategory: "official", ReleasedAt: &now},
		},
		lastRefresh: now,
	}

	// sanity: the vocabulary is present, so the guard is allowed to fire
	if !TaskVocabularyRepresented(TaskReasoning, idx.entries) {
		t.Fatal("precondition: cap:reasoning must be recognised as present")
	}

	got := idx.RecommendV2(context.Background(), TaskReasoning, ClassificationSignals{EstimatedTokens: 500}, ProfileSmart, "", 3)
	if len(got) == 0 {
		t.Fatal("reasoning task returned no candidates")
	}
	// 1/3 → MatchScore 33.3, which is >= 30, so no collapse. Documented here
	// because it is the boundary: the neutralisation must not have moved it.
	if isFallbackWinner(got) {
		t.Errorf("match score %.1f is at/above the 30 threshold — collapse was not expected",
			got[0].Breakdown.MatchScore)
	}
}

// R73 审计 M-1 根修回归钉：词表代表只在 hot-top3 子池之外时，坍缩 guard
// 不得据全量词表判定把全 0 子池坍缩成 48h 热度单模型（多样性 3→1）。
// 旧实现里本用例必坍缩：vocabularyPresent 按全量 available 判 true，
// 而打分发生在 untagged 的 hot-top3 子池，winner MatchScore=0 <30。
func TestRecommendV2_SubpoolVocabularyOnlyOutsideHotTop3KeepsScoredPool(t *testing.T) {
	now := time.Now()
	idx := &Index{
		entries: []Candidate{
			// hot-top3：无任何 reasoning 词表（真实热榜形态：chat 通用模型霸榜）
			{CanonicalID: 1, CanonicalName: "hot-a", CredentialID: 11, Tier: "primary", PopularityScore: 100,
				SuccessRate: 0.95, P95LatencyMs: 900, ProviderCategory: "official",
				UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 2, CanonicalName: "hot-b", CredentialID: 12, Tier: "primary", PopularityScore: 90,
				SuccessRate: 0.93, P95LatencyMs: 1000, ProviderCategory: "official",
				UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			{CanonicalID: 3, CanonicalName: "hot-c", CredentialID: 13, Tier: "primary", PopularityScore: 80,
				SuccessRate: 0.91, P95LatencyMs: 1100, ProviderCategory: "official",
				UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
			// 词表代表：热度低，恒落在 hot-top3 之外
			{CanonicalID: 4, CanonicalName: "reasoner", CredentialID: 14, Tier: "primary", PopularityScore: 10,
				Tags: []string{"cap:reasoning", "modality:text"}, SuccessRate: 0.90, P95LatencyMs: 1200,
				ProviderCategory: "official", UnitPriceInPer1M: 1, UnitPriceOutPer1M: 2, ReleasedAt: &now},
		},
		lastRefresh:      now,
		hotCanonicals:    []int{1, 2, 3},
		hotCanonicalsTS:  now,
		hotCanonicalsTTL: 2 * time.Minute,
	}

	// 前置：全量池词表在位（正是旧实现坍缩的触发条件）。
	if !TaskVocabularyRepresented(TaskReasoning, idx.entries) {
		t.Fatal("precondition: full pool must carry the reasoning vocabulary")
	}

	got := idx.RecommendV2(context.Background(), TaskReasoning, ClassificationSignals{EstimatedTokens: 500}, ProfileSmart, "", 3)
	if len(got) == 0 {
		t.Fatal("reasoning task returned no candidates")
	}
	if got[0].Breakdown.MatchScore >= 30 {
		t.Fatalf("precondition broken: winner match score %.1f is not sub-30, guard not exercised", got[0].Breakdown.MatchScore)
	}
	if isFallbackWinner(got) {
		t.Fatalf("subpool with vocabulary only outside hot-top3 collapsed to the 48h fallback: %q",
			got[0].Candidate.CanonicalName)
	}
	if len(got) < 2 {
		t.Fatalf("expected the scored subpool to survive (>=2 candidates for failover), got %d", len(got))
	}
}
