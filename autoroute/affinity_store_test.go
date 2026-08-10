package autoroute

import (
	"context"
	"testing"
	"time"
)

// newTestAffinityStore builds a store with a hand-seeded snapshot, bypassing
// the DB so the resolution logic can be tested in isolation.
func newTestAffinityStore(mode AffinityMode, recs []AffinityRecord) *AffinityStore {
	s := NewAffinityStore(nil, mode, AffinityDefaultExploreRatio)
	snap := &affinitySnapshot{
		byKey:    make(map[affinityKey]AffinityRecord),
		ranked:   make(map[string][]AffinityRecord),
		LoadedAt: time.Now(),
	}
	for _, r := range recs {
		snap.byKey[affinityKey{string(r.TaskType), string(r.Profile), r.TenantID, r.CanonicalID}] = r
		rk := rankedKey(string(r.TaskType), string(r.Profile), r.TenantID)
		snap.ranked[rk] = append(snap.ranked[rk], r)
	}
	for k := range snap.ranked {
		sortByAffinityDesc(snap.ranked[k])
	}
	snap.RowCount = len(snap.byKey)
	s.snapshot.Store(snap)
	return s
}

// A nil store and an unpopulated store must both behave as "no opinion", never
// panic. Affinity is an optimisation and must not be able to break routing.
func TestAffinityStore_NilAndEmptyAreNeutral(t *testing.T) {
	var nilStore *AffinityStore
	if got, found := nilStore.Lookup(TaskCode, ProfileSmart, "", 1); got != AffinityNeutral || found {
		t.Errorf("nil store: got (%.2f,%v), want (%.2f,false)", got, found, AffinityNeutral)
	}
	if nilStore.Mode() != AffinityOff {
		t.Errorf("nil store mode = %q, want off", nilStore.Mode())
	}
	if nilStore.Applies("req") {
		t.Error("nil store must never apply affinity")
	}
	if got := nilStore.Ranking(TaskCode, ProfileSmart, ""); got != nil {
		t.Errorf("nil store ranking = %v, want nil", got)
	}
	if n, _ := nilStore.Stats(); n != 0 {
		t.Errorf("nil store rowcount = %d, want 0", n)
	}
	// Reload on a nil pool must be a no-op, not an error.
	if err := nilStore.Reload(context.Background()); err != nil {
		t.Errorf("nil store Reload returned %v, want nil", err)
	}

	empty := NewAffinityStore(nil, AffinityOn, 0)
	if got, found := empty.Lookup(TaskCode, ProfileSmart, "", 1); got != AffinityNeutral || found {
		t.Errorf("empty store: got (%.2f,%v), want neutral/false", got, found)
	}
}

func TestAffinityStore_ModeOffIgnoresData(t *testing.T) {
	recs := []AffinityRecord{{
		TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 7,
		Affinity: 88, SampleCount: 500, LastSampledAt: time.Now(),
	}}
	s := newTestAffinityStore(AffinityOff, recs)
	if got, found := s.Lookup(TaskCode, ProfileSmart, "", 7); got != AffinityNeutral || found {
		t.Errorf("mode=off must ignore learned data, got (%.2f,%v)", got, found)
	}
}

func TestAffinityStore_LookupAppliesFloorAndClamp(t *testing.T) {
	now := time.Now()
	recs := []AffinityRecord{
		{ // well-evidenced but extreme: must be clamped to 90
			TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 1,
			Affinity: 99, SampleCount: 1000, LastSampledAt: now,
		},
		{ // too few samples: must read neutral despite a strong stored value
			TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 2,
			Affinity: 95, SampleCount: AffinityMinSamples - 1, LastSampledAt: now,
		},
		{ // stale: must have decayed toward neutral
			TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 3,
			Affinity: 90, SampleCount: 500,
			LastSampledAt: now.Add(-AffinityStaleAfter - 60*24*time.Hour),
		},
	}
	s := newTestAffinityStore(AffinityOn, recs)

	got, found := s.Lookup(TaskCode, ProfileSmart, "", 1)
	if !found || got != AffinityNeutral+AffinityMaxDeviation {
		t.Errorf("clamp: got (%.2f,%v), want (%.2f,true)", got, found, AffinityNeutral+AffinityMaxDeviation)
	}

	got, found = s.Lookup(TaskCode, ProfileSmart, "", 2)
	if got != AffinityNeutral || found {
		t.Errorf("min-sample floor: got (%.2f,%v), want neutral/false", got, found)
	}

	got, _ = s.Lookup(TaskCode, ProfileSmart, "", 3)
	if got >= 90 {
		t.Errorf("stale row was not decayed: got %.2f", got)
	}
}

func TestAffinityStore_UnknownCandidateIsNeutral(t *testing.T) {
	s := newTestAffinityStore(AffinityOn, []AffinityRecord{{
		TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 1,
		Affinity: 80, SampleCount: 100, LastSampledAt: time.Now(),
	}})

	// A model never seen on this task: cold start must be neutral, so a new
	// model is neither promoted nor punished before it has evidence.
	if got, found := s.Lookup(TaskCode, ProfileSmart, "", 999); got != AffinityNeutral || found {
		t.Errorf("unknown model: got (%.2f,%v), want neutral/false", got, found)
	}
	// Right model, different task: affinity is task-scoped and must not leak.
	if got, found := s.Lookup(TaskReasoning, ProfileSmart, "", 1); got != AffinityNeutral || found {
		t.Errorf("task isolation: got (%.2f,%v), want neutral/false", got, found)
	}
	// Right model+task, different profile: also scoped.
	if got, found := s.Lookup(TaskCode, ProfileCostFirst, "", 1); got != AffinityNeutral || found {
		t.Errorf("profile isolation: got (%.2f,%v), want neutral/false", got, found)
	}
	// Invalid canonical id must not be looked up at all.
	if got, found := s.Lookup(TaskCode, ProfileSmart, "", 0); got != AffinityNeutral || found {
		t.Errorf("canonical_id=0: got (%.2f,%v), want neutral/false", got, found)
	}
}

// Resolves design-doc open question #1: a thinly-evidenced tenant row must not
// overrule the platform row.
func TestAffinityStore_TenantFallbackNeedsEvidence(t *testing.T) {
	now := time.Now()
	recs := []AffinityRecord{
		{ // platform: strong evidence, low score
			TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 5, TenantID: "",
			Affinity: 30, SampleCount: 1000, LastSampledAt: now,
		},
		{ // tenant-a: enough evidence, high score → should win
			TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 5, TenantID: "tenant-a",
			Affinity: 85, SampleCount: AffinityTenantMinSamples, LastSampledAt: now,
		},
		{ // tenant-b: too little evidence → platform should win
			TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 5, TenantID: "tenant-b",
			Affinity: 85, SampleCount: AffinityTenantMinSamples - 1, LastSampledAt: now,
		},
	}
	s := newTestAffinityStore(AffinityOn, recs)

	if got, _ := s.Lookup(TaskCode, ProfileSmart, "tenant-a", 5); got != 85 {
		t.Errorf("well-evidenced tenant row should win: got %.2f, want 85", got)
	}
	if got, _ := s.Lookup(TaskCode, ProfileSmart, "tenant-b", 5); got != 30 {
		t.Errorf("thin tenant row must fall back to platform: got %.2f, want 30", got)
	}
	if got, _ := s.Lookup(TaskCode, ProfileSmart, "tenant-unknown", 5); got != 30 {
		t.Errorf("unknown tenant must use platform: got %.2f, want 30", got)
	}
	if got, _ := s.Lookup(TaskCode, ProfileSmart, "", 5); got != 30 {
		t.Errorf("no tenant must use platform: got %.2f, want 30", got)
	}
}

func TestAffinityStore_AppliesRespectsModeAndExplore(t *testing.T) {
	// Shadow must never apply, however the hash falls.
	shadow := NewAffinityStore(nil, AffinityShadow, 0)
	for i := 0; i < 100; i++ {
		if shadow.Applies("req-" + itoa(i)) {
			t.Fatal("shadow mode must never apply affinity")
		}
	}

	// mode=on with ratio 0 always applies.
	on := NewAffinityStore(nil, AffinityOn, 0)
	if !on.Applies("req-1") {
		t.Error("mode=on, ratio=0 should apply")
	}

	// mode=on with ratio 1 never applies (everything explores).
	all := NewAffinityStore(nil, AffinityOn, 1.0)
	if all.Applies("req-1") {
		t.Error("ratio=1 means always explore, so affinity must not apply")
	}

	// With a partial ratio both outcomes must occur across many requests,
	// otherwise the explore bucket is silently broken.
	part := NewAffinityStore(nil, AffinityOn, 0.5)
	var applied, explored int
	for i := 0; i < 2000; i++ {
		if part.Applies("r-" + itoa(i)) {
			applied++
		} else {
			explored++
		}
	}
	if applied == 0 || explored == 0 {
		t.Errorf("expected a mix of applied/explored, got applied=%d explored=%d", applied, explored)
	}
}

func TestNewAffinityStore_RejectsBadExploreRatio(t *testing.T) {
	for _, bad := range []float64{-0.5, 1.5, 42} {
		s := NewAffinityStore(nil, AffinityOn, bad)
		if s.ExploreRatio() != AffinityDefaultExploreRatio {
			t.Errorf("ratio %v should fall back to default, got %v", bad, s.ExploreRatio())
		}
	}
	// Valid values are preserved.
	s := NewAffinityStore(nil, AffinityOn, 0.25)
	if s.ExploreRatio() != 0.25 {
		t.Errorf("valid ratio was altered: got %v", s.ExploreRatio())
	}
}

func TestAffinityStore_RankingOrder(t *testing.T) {
	now := time.Now()
	recs := []AffinityRecord{
		{TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 1, CanonicalModel: "mid", Affinity: 60, SampleCount: 100, Confidence: 0.7, LastSampledAt: now},
		{TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 2, CanonicalModel: "best", Affinity: 85, SampleCount: 200, Confidence: 0.8, LastSampledAt: now},
		{TaskType: TaskCode, Profile: ProfileSmart, CanonicalID: 3, CanonicalModel: "worst", Affinity: 20, SampleCount: 50, Confidence: 0.6, LastSampledAt: now},
	}
	s := newTestAffinityStore(AffinityOn, recs)

	got := s.Ranking(TaskCode, ProfileSmart, "")
	if len(got) != 3 {
		t.Fatalf("ranking length = %d, want 3", len(got))
	}
	want := []string{"best", "mid", "worst"}
	for i, w := range want {
		if got[i].CanonicalModel != w {
			t.Errorf("rank %d = %q, want %q", i, got[i].CanonicalModel, w)
		}
	}
	// Descending order must hold pairwise.
	for i := 1; i < len(got); i++ {
		if got[i-1].Affinity < got[i].Affinity {
			t.Errorf("ranking not descending at %d: %.2f < %.2f", i, got[i-1].Affinity, got[i].Affinity)
		}
	}
}

// At equal affinity, the better-evidenced row should rank higher.
func TestAffinityStore_RankingTieBreaksOnEvidence(t *testing.T) {
	now := time.Now()
	recs := []AffinityRecord{
		{TaskType: TaskChat, CanonicalID: 1, CanonicalModel: "thin", Affinity: 70, Confidence: 0.3, SampleCount: 12, LastSampledAt: now},
		{TaskType: TaskChat, CanonicalID: 2, CanonicalModel: "solid", Affinity: 70, Confidence: 0.9, SampleCount: 900, LastSampledAt: now},
	}
	s := newTestAffinityStore(AffinityOn, recs)
	got := s.Ranking(TaskChat, "", "")
	if len(got) != 2 || got[0].CanonicalModel != "solid" {
		t.Errorf("tie should favour stronger evidence, got %+v", modelNames(got))
	}
}

func TestSortByAffinityDesc_StableAndTotal(t *testing.T) {
	// Empty and single-element inputs must not panic.
	sortByAffinityDesc(nil)
	sortByAffinityDesc([]AffinityRecord{{Affinity: 1}})

	rows := []AffinityRecord{
		{CanonicalModel: "a", Affinity: 10},
		{CanonicalModel: "b", Affinity: 90},
		{CanonicalModel: "c", Affinity: 50},
		{CanonicalModel: "d", Affinity: 90, Confidence: 0.9},
		{CanonicalModel: "e", Affinity: 0},
	}
	sortByAffinityDesc(rows)
	for i := 1; i < len(rows); i++ {
		if rows[i-1].Affinity < rows[i].Affinity {
			t.Fatalf("not sorted at %d: %v", i, modelNames(rows))
		}
	}
	// The 90/0.9 row must precede the 90/0.0 row.
	if rows[0].CanonicalModel != "d" {
		t.Errorf("expected 'd' first (higher confidence at equal affinity), got %v", modelNames(rows))
	}
}

func TestAffinityStore_Stats(t *testing.T) {
	s := newTestAffinityStore(AffinityOn, []AffinityRecord{
		{TaskType: TaskCode, CanonicalID: 1, Affinity: 60},
		{TaskType: TaskCode, CanonicalID: 2, Affinity: 70},
	})
	n, loadedAt := s.Stats()
	if n != 2 {
		t.Errorf("rowcount = %d, want 2", n)
	}
	if loadedAt.IsZero() {
		t.Error("loadedAt should be set on a populated snapshot")
	}
}

func modelNames(rows []AffinityRecord) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.CanonicalModel)
	}
	return out
}
