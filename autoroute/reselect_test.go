package autoroute

// reselect_test.go —— 宽退路池钉测（2026-11-17 auto 模式可用性轮 · 根修 5）。
//
// ★ 守的是什么：
//
// auto 的决策池只有 48h 热门 top-3（recommend_v2.go 的 getHotTop3Canonicals）。
// 选中模型一旦此刻不可路由，退路也只有那 2-3 个同类模型 ⇒ 211 真机实测
// 两轮 auto 都以 503 收场，而平台上明明有 871 个此刻可路由的模型。
//
// ★★ 第一版的退路池建在同一张 48h 热度榜上，而那张榜在生产上**恒空**
// （`session_turns_hot.canonical_id` 近 48h 5338 行**全为 NULL**）⇒
// 退路池也空 ⇒ 重选无链可走 ⇒ 仍然 503。⇒ 现在退路池改为
// **索引快照 ∩ 实时可用过滤**，不再依赖任何一张可能空掉的统计表。

import (
	"context"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedReselectIndex(candidates ...Candidate) *Index {
	idx := NewIndex()
	idx.entries = candidates
	return idx
}

// filterAllowing keeps only the canonicals named in keep.
func filterAllowing(keep ...string) func(context.Context, *pgxpool.Pool, []Candidate) ([]Candidate, error) {
	return func(_ context.Context, _ *pgxpool.Pool, all []Candidate) ([]Candidate, error) {
		out := make([]Candidate, 0, len(all))
		for _, c := range all {
			for _, k := range keep {
				if c.CanonicalName == k {
					out = append(out, c)
					break
				}
			}
		}
		return out, nil
	}
}

// TestReselectCandidateNames_UsesLiveFilterAndSkipsChosen pins the ladder
// contract: the names come from the **live availability filter** (not from a
// statistics table), the chosen model is excluded, and duplicates collapse.
func TestReselectCandidateNames_UsesLiveFilterAndSkipsChosen(t *testing.T) {
	idx := seedReselectIndex(
		Candidate{CredentialID: 1, CanonicalID: 1, CanonicalName: "model-a"},
		Candidate{CredentialID: 2, CanonicalID: 2, CanonicalName: "model-b"},
		Candidate{CredentialID: 3, CanonicalID: 3, CanonicalName: "model-c"},
	)
	// The live filter says model-b has no routable binding.
	idx.availabilityFilter = filterAllowing("model-a", "model-c")

	got := reselectCandidateNames(context.Background(), idx, "model-a", ReselectPoolLimit)
	want := []string{"model-c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reselect ladder = %v, want %v", got, want)
	}
}

// TestReselectCandidateNames_DeduplicatesCanonical pins that several
// bindings of the same canonical collapse into one ladder entry.
func TestReselectCandidateNames_DeduplicatesCanonical(t *testing.T) {
	idx := seedReselectIndex(
		Candidate{CredentialID: 1, CanonicalID: 1, CanonicalName: "model-a"},
		Candidate{CredentialID: 2, CanonicalID: 1, CanonicalName: "model-a"},
		Candidate{CredentialID: 3, CanonicalID: 2, CanonicalName: "model-b"},
	)
	idx.availabilityFilter = filterAllowing("model-a", "model-b")

	got := reselectCandidateNames(context.Background(), idx, "chosen", ReselectPoolLimit)
	want := []string{"model-a", "model-b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reselect ladder = %v, want %v (duplicates must collapse)", got, want)
	}
}

// TestReselectCandidateNames_FilterErrorFailsOpen pins the fail-safe: a
// failing live filter must NOT produce an empty ladder — an empty ladder
// kills the request, while a wider one is re-verified downstream anyway.
func TestReselectCandidateNames_FilterErrorFailsOpen(t *testing.T) {
	idx := seedReselectIndex(
		Candidate{CredentialID: 1, CanonicalID: 1, CanonicalName: "model-a"},
	)
	idx.availabilityFilter = func(context.Context, *pgxpool.Pool, []Candidate) ([]Candidate, error) {
		return nil, context.DeadlineExceeded
	}

	got := reselectCandidateNames(context.Background(), idx, "chosen", ReselectPoolLimit)
	want := []string{"model-a"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reselect ladder = %v, want %v (must fail open)", got, want)
	}
}

// TestReselectCandidateNames_HonoursLimit pins that the ladder width is
// bounded — the reselect path walks it one model at a time and must not turn
// into an unbounded serial DB scan.
func TestReselectCandidateNames_HonoursLimit(t *testing.T) {
	var candidates []Candidate
	for i := 1; i <= 50; i++ {
		candidates = append(candidates, Candidate{
			CredentialID:  int64(i),
			CanonicalID:   i,
			CanonicalName: "model-" + string(rune('a'+i%26)) + string(rune('0'+i/10)),
		})
	}
	idx := seedReselectIndex(candidates...)
	idx.availabilityFilter = func(_ context.Context, _ *pgxpool.Pool, all []Candidate) ([]Candidate, error) {
		return all, nil
	}

	got := reselectCandidateNames(context.Background(), idx, "chosen", 8)
	if len(got) != 8 {
		t.Fatalf("ladder width = %d, want 8", len(got))
	}
}

// TestReselectPoolLimitIsWide pins the ladder width itself.
//
// Why this needs its own test: the other tests pass an explicit limit, so
// they cannot see a change to the constant that the production call sites
// actually use. Without this test, narrowing ReselectPoolLimit back to 3
// (i.e. back to the decision pool) would leave every other test green — a
// real false negative caught by mutation M5.
func TestReselectPoolLimitIsWide(t *testing.T) {
	if ReselectPoolLimit < 16 {
		t.Fatalf("ReselectPoolLimit = %d, want >= 16 — the reselect ladder must be far wider than the 3-model decision pool", ReselectPoolLimit)
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

	d := NewDecider(&v2TestClassifier{task: TaskCode}, nil, idx, NewMemoryProfileStore())
	dec, err := d.Decide(context.Background(), ClassificationSignals{}, 0, "", "", "")
	if err != nil {
		t.Fatalf("Decide err: %v", err)
	}
	if len(dec.ReselectCandidates) == 0 {
		t.Fatal("V1 decision must carry a wide reselect ladder too")
	}
}
