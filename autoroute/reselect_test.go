package autoroute

// reselect_test.go —— 宽退路池钉测（2026-11-17 auto 模式可用性轮 · 根修 5）。
//
// ★ 守的是什么：
//
// auto 的决策池只有 48h 热门 top-3（recommend_v2.go 的 getHotTop3Canonicals）。
// 选中模型一旦此刻不可路由，退路也只有那 2-3 个同类模型 ⇒ 211 真机实测
// 三笔 auto 全 503，而平台上明明有 871 个此刻可路由的模型。
//
// ⇒ 退路池必须比决策池宽（48h 热度榜 LIMIT 32），且：去重、排除已选模型、
// 顺序稳定（热度序）。

import (
	"context"
	"reflect"
	"testing"
)

func seedReselectIndex(candidates ...Candidate) *Index {
	idx := NewIndex()
	idx.entries = candidates
	return idx
}

// TestReselectCandidateNames_SkipsChosenAndDuplicates pins the ladder
// contract: the chosen model never appears in its own ladder, and the same
// canonical id listed twice yields one entry.
func TestReselectCandidateNames_SkipsChosenAndDuplicates(t *testing.T) {
	idx := seedReselectIndex(
		Candidate{CanonicalID: 10, CanonicalName: "model-a"},
		Candidate{CanonicalID: 20, CanonicalName: "model-b"},
		Candidate{CanonicalID: 30, CanonicalName: "model-c"},
	)
	idx.hotReselectLoader = func(context.Context, int) []int {
		return []int{10, 20, 30, 10}
	}

	got := reselectCandidateNames(context.Background(), idx, "model-b", ReselectPoolLimit)
	want := []string{"model-a", "model-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reselect ladder = %v, want %v", got, want)
	}
}

// TestReselectCandidateNames_EmptyWhenNoLadder pins the fail-safe: with no
// ladder source (no pool / query failed) the result is empty, so the caller
// keeps the pre-existing narrow behaviour instead of losing the ladder.
func TestReselectCandidateNames_EmptyWhenNoLadder(t *testing.T) {
	idx := seedReselectIndex(Candidate{CanonicalID: 10, CanonicalName: "model-a"})
	idx.hotReselectLoader = func(context.Context, int) []int { return nil }

	if got := reselectCandidateNames(context.Background(), idx, "chosen", ReselectPoolLimit); len(got) != 0 {
		t.Fatalf("expected an empty ladder, got %v", got)
	}
}

// TestReselectCandidateNames_HonoursLimit pins that the ladder width is
// bounded — the reselect path walks it one model at a time and must not turn
// into an unbounded serial DB scan.
func TestReselectCandidateNames_HonoursLimit(t *testing.T) {
	var candidates []Candidate
	for i := 1; i <= 50; i++ {
		candidates = append(candidates, Candidate{CanonicalID: i, CanonicalName: "model-" + string(rune('a'+i%26)) + string(rune('0'+i/10))})
	}
	idx := seedReselectIndex(candidates...)
	idx.hotReselectLoader = func(context.Context, int) []int {
		out := make([]int, 0, 50)
		for i := 1; i <= 50; i++ {
			out = append(out, i)
		}
		return out
	}

	got := reselectCandidateNames(context.Background(), idx, "chosen", 8)
	if len(got) != 8 {
		t.Fatalf("ladder width = %d, want 8", len(got))
	}
}

// TestDecideV2_FillsReselectCandidates pins that the decision actually
// carries the wide ladder — the field exists on Decision, but if the
// decider stopped filling it the gateway would silently fall back to the
// 3-model narrow chain and the user's "model unavailable" would return.
func TestDecideV2_FillsReselectCandidates(t *testing.T) {
	old := GetFeatureFlags()
	SetGlobalFeatureFlagsForTest(&FeatureFlags{UseChannelQualityRouting: true})
	defer SetGlobalFeatureFlagsForTest(old)

	idx := newV2Index([]Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "winner", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "backup", Tags: []string{"code"}, SuccessRate: 0.90},
	})
	idx.hotReselectLoader = func(context.Context, int) []int { return []int{1, 2} }

	d := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())
	dec, err := d.DecideV2(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("DecideV2 err: %v", err)
	}
	if len(dec.ReselectCandidates) == 0 {
		t.Fatal("decision must carry a wide reselect ladder")
	}
	for _, m := range dec.ReselectCandidates {
		if m == dec.ChosenModel {
			t.Fatalf("chosen model %q must not appear in its own ladder %v", dec.ChosenModel, dec.ReselectCandidates)
		}
	}
	// The audit view must stay untouched by the new field.
	if len(dec.CandidatesTopN) > 3 {
		t.Fatalf("audit view grew to %d entries — the wire/JSONB shape must stay stable", len(dec.CandidatesTopN))
	}
}

// TestDecideV1_FillsReselectCandidates pins the same contract for the V1
// path (not the default path, but the field semantics must not diverge).
func TestDecideV1_FillsReselectCandidates(t *testing.T) {
	idx := newV2Index([]Candidate{
		{CredentialID: 1, CanonicalID: 1, CanonicalName: "winner", Tags: []string{"code"}, SuccessRate: 0.99},
		{CredentialID: 2, CanonicalID: 2, CanonicalName: "backup", Tags: []string{"code"}, SuccessRate: 0.90},
	})
	idx.hotReselectLoader = func(context.Context, int) []int { return []int{1, 2} }

	d := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())
	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if len(dec.ReselectCandidates) == 0 {
		t.Fatal("V1 decision must carry a wide reselect ladder too")
	}
}

// TestReselectPoolLimitIsWide pins the ladder width itself.
//
// Why this needs its own test: TestReselectCandidateNames_HonoursLimit
// passes an explicit limit, so it cannot see a change to the constant that
// the production call sites actually use. Without this test, narrowing
// ReselectPoolLimit back to 3 (i.e. back to the decision pool) would leave
// every other test green — a real false negative caught by mutation M5.
func TestReselectPoolLimitIsWide(t *testing.T) {
	if ReselectPoolLimit < 16 {
		t.Fatalf("ReselectPoolLimit = %d, want >= 16 — the reselect ladder must be far wider than the 3-model decision pool", ReselectPoolLimit)
	}
}
