// Step 5 round 1 (S-3): router-side integration test for the
// routing_state_source observability hook added per
// docs/superpowers/specs/2026-07-27-request-flow-audit-design.md
// §8.2 + §11.4. Asserts that the four router state paths
// (authoritative hit/miss/stale, authoritative fallback, canary, off)
// each call RecordRoutingStateSource exactly once with the correct
// outer label, and that the authoritative path does NOT consult the
// legacy StateManager (spec §8.2: 旧 StateManager 不得参与 authoritative
// 健康判定).
//
// These tests use the statesource package's ResetForTest hook so they
// can be deterministic and free of inter-test counter leakage.
package executors

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// seedV2Node writes a node hash so PipelineNodeViews returns it. Mirrors
// manager_lru_test.go's seedNode helper but kept local to avoid
// exporting it from the v2 package.
func seedV2Node(t *testing.T, mr *miniredis.Miniredis, credID int, model string, gen int64, available bool) {
	t.Helper()
	key := "ursm:v2:node:t:" + strconv.Itoa(credID) + ":" + model
	avail := "0"
	if available {
		avail = "1"
	}
	mr.HSet(key,
		"available", avail,
		"generation", strconv.FormatInt(gen, 10),
		"source_priority", "10",
		"lat_ewma_ms", "50",
		"sr_5m", "0.9",
		"fail_streak", "0",
	)
}

// buildV2Manager builds a v2 Manager in authoritative mode with a
// miniredis-backed Redis, ready for PlanCandidates to consult.
func buildV2Manager(t *testing.T, mode api.RolloutMode) (*ursmv2.Manager, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = mode
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 30 * time.Second
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	return mgr, mr
}

// candidateSet returns two routable, active candidates so the
// FilterAndScore path has something to enumerate. Duplicate
// deduplication happens in PlanCandidatesWithContext.
func candidateSet() []provider.Candidate {
	return []provider.Candidate{
		{CredentialID: 1, ProviderID: 1, RawModel: "m", Tier: 1, Routable: true, LifecycleStatus: "active"},
		{CredentialID: 2, ProviderID: 1, RawModel: "m", Tier: 1, Routable: true, LifecycleStatus: "active"},
	}
}

// TestPlanCandidates_AuthoritativeHit_RecordsAuthoritative pins the
// happy path: URSM v2 authoritative + Ready + filter success. The
// router must record StateSourceAuthoritative exactly once.
func TestPlanCandidates_AuthoritativeHit_RecordsAuthoritative(t *testing.T) {
	statesource.ResetForTest()
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-1",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceAuthoritative]; got != 1 {
		t.Fatalf("authoritative count = %d, want 1; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceFallback]; got != 0 {
		t.Fatalf("fallback must be untouched on success, got %d", got)
	}
	if got := snap[statesource.StateSourceCanary]; got != 0 {
		t.Fatalf("canary must be untouched, got %d", got)
	}
	if got := snap[statesource.StateSourceOff]; got != 0 {
		t.Fatalf("off must be untouched, got %d", got)
	}
}

// TestPlanCandidates_AuthoritativeFilterError_RecordsFallback pins
// the error path: kill Redis to force FilterAndScore to error, the
// router must record StateSourceFallback exactly once.
func TestPlanCandidates_AuthoritativeFilterError_RecordsFallback(t *testing.T) {
	statesource.ResetForTest()
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	// Kill Redis AFTER seeding so the first filter call surfaces
	// the failure (LRU is empty, so the filter must consult Redis).
	mr.Close()

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-2",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceFallback]; got != 1 {
		t.Fatalf("fallback count = %d, want 1; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceAuthoritative]; got != 0 {
		t.Fatalf("authoritative must be untouched on error path, got %d", got)
	}
}

// TestPlanCandidates_AuthoritativeNotReady_RecordsFallback pins the
// ready==false edge of the authoritative path: the router must
// record StateSourceFallback (recovery gate not open).
func TestPlanCandidates_AuthoritativeNotReady_RecordsFallback(t *testing.T) {
	statesource.ResetForTest()
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	_ = mgr.SetReady(context.Background(), false)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-3",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceFallback]; got != 1 {
		t.Fatalf("fallback count = %d, want 1 (ready=false is a fallback); full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceAuthoritative]; got != 0 {
		t.Fatalf("authoritative must be untouched on not-ready path, got %d", got)
	}
}

// TestPlanCandidates_Canary_RecordsCanary pins the canary path:
// ModeCanary + ShouldUseV2==true → router records StateSourceCanary.
// We force the canary gate to 100% by rebuilding the manager with
// CanaryPercent=100 so the deterministic per-request v2 selection
// is reachable.
func TestPlanCandidates_Canary_RecordsCanary(t *testing.T) {
	statesource.ResetForTest()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 30 * time.Second
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-4",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceCanary]; got != 1 {
		t.Fatalf("canary count = %d, want 1; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceAuthoritative]; got != 0 {
		t.Fatalf("canary must not record authoritative, got %d", got)
	}
}

// TestPlanCandidates_Canary_RecordsInnerSource pins the canary inner
// NodeMirror source observability (Step 5 round 1, spec §8.2). The
// canary path must call FilterAndScoreReadyWithSource and either
// the router or the manager must record the inner source label
// (hit/miss/stale/fallback) so operators can size the LRU and
// quantify the NodeMirror read ratio. The outer Canary label is
// still recorded.
//
// We force the canary gate to 100% (CanaryPercent=100) and warm the
// LRU with a fresh Redis entry so the cold read reports "miss".
func TestPlanCandidates_Canary_RecordsInnerSource(t *testing.T) {
	statesource.ResetForTest()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 30 * time.Second
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-canary-inner",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceCanary]; got != 1 {
		t.Fatalf("canary outer count = %d, want 1; full snapshot = %+v", got, snap)
	}
	// Inner source must be recorded exactly once. The LRU is cold
	// on the first read, so the inner must be "miss" (not "hit").
	if got := snap[statesource.StateSourceNodeMirrorMiss]; got != 1 {
		t.Fatalf("inner NodeMirror miss count = %d, want 1 on cold LRU; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceNodeMirrorHit]; got != 0 {
		t.Fatalf("inner NodeMirror hit must be 0 on cold LRU, got %d", got)
	}
	if got := snap[statesource.StateSourceNodeMirrorStale]; got != 0 {
		t.Fatalf("inner NodeMirror stale must be 0 on cold LRU, got %d", got)
	}
	if got := snap[statesource.StateSourceAuthoritative]; got != 0 {
		t.Fatalf("canary must not record authoritative, got %d", got)
	}
}

// TestPlanCandidates_Canary_RecordsInnerHitOnWarmMirror pins the
// inner hit path: with the LRU already warm (mirrored from the
// first request), the second canary request must report a hit.
func TestPlanCandidates_Canary_RecordsInnerHitOnWarmMirror(t *testing.T) {
	statesource.ResetForTest()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 30 * time.Second
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	// First request: cold LRU → records miss.
	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-canary-warm-1",
	)
	// Second request: warm LRU → must record hit.
	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-canary-warm-2",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceCanary]; got != 2 {
		t.Fatalf("canary outer count = %d, want 2; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceNodeMirrorHit]; got != 1 {
		t.Fatalf("inner NodeMirror hit count = %d, want 1 on warm LRU; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceNodeMirrorMiss]; got != 1 {
		t.Fatalf("inner NodeMirror miss count = %d, want 1 (only the first cold read); full snapshot = %+v", got, snap)
	}
}

// TestPlanCandidates_Canary_RecordsInnerStaleAfterSoftTTL pins the
// stale path: with a short soft-TTL, the second request after the
// TTL elapses must report stale (not hit).
func TestPlanCandidates_Canary_RecordsInnerStaleAfterSoftTTL(t *testing.T) {
	statesource.ResetForTest()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 150 * time.Millisecond
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	// First request: cold LRU → miss; backfills mirror.
	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-canary-stale-1",
	)
	// Sleep past soft-TTL so the mirror entry is soft-expired.
	time.Sleep(200 * time.Millisecond)
	// Second request: stale entry → must report stale (or miss per
	// spec §8.2 — both are acceptable fallbacks; the contract is
	// "not hit").
	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-canary-stale-2",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceCanary]; got != 2 {
		t.Fatalf("canary outer count = %d, want 2; full snapshot = %+v", got, snap)
	}
	// First call: cold LRU → miss. Second call: soft-expired
	// entry → stale. We must NOT see any "hit" — the second call's
	// entry is past soft-TTL.
	if got := snap[statesource.StateSourceNodeMirrorHit]; got != 0 {
		t.Fatalf("inner NodeMirror hit must be 0 (cold + soft-expired), got %d; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceNodeMirrorMiss]; got != 1 {
		t.Fatalf("inner NodeMirror miss count = %d, want 1 (first cold read); full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceNodeMirrorStale]; got != 1 {
		t.Fatalf("inner NodeMirror stale count = %d, want 1 (second read after soft-TTL); full snapshot = %+v", got, snap)
	}
}

// TestPlanCandidates_Canary_RecordsInnerFallbackOnRedisDown pins the
// fallback path: with Redis killed after seeding, the canary v2
// path must report StateSourceFallback (the manager's API contract
// for an error on the canary inner read).
func TestPlanCandidates_Canary_RecordsInnerFallbackOnRedisDown(t *testing.T) {
	statesource.ResetForTest()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := ursmv2.DefaultConfig()
	cfg.Mode = api.ModeCanary
	cfg.CanaryPercent = 100
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 30 * time.Second
	mgr := ursmv2.New(ursmv2.Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	// Kill Redis so the canary read errors. The router must
	// record StateSourceFallback for the inner label.
	_ = mgr.SetReady(context.Background(), true)
	mr.Close()

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-canary-fallback",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceCanary]; got != 1 {
		t.Fatalf("canary outer count = %d, want 1; full snapshot = %+v", got, snap)
	}
	if got := snap[statesource.StateSourceFallback]; got != 1 {
		t.Fatalf("inner fallback count = %d, want 1 on Redis-down; full snapshot = %+v", got, snap)
	}
}

// TestPlanCandidates_Off_RecordsOff pins the off / nil-manager path.
// The router records StateSourceOff.
func TestPlanCandidates_Off_RecordsOff(t *testing.T) {
	statesource.ResetForTest()
	r := NewRouter(nil, nil)
	// r.URSMv2 == nil is the production default today (URSM_V2_MODE=off).
	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-5",
	)

	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceOff]; got != 1 {
		t.Fatalf("off count = %d, want 1; full snapshot = %+v", got, snap)
	}
	for _, k := range []statesource.RoutingStateSource{
		statesource.StateSourceAuthoritative,
		statesource.StateSourceCanary,
		statesource.StateSourceFallback,
	} {
		if got := snap[k]; got != 0 {
			t.Fatalf("off path must not record %s, got %d", k, got)
		}
	}
}

// TestPlanCandidates_Authoritative_StateManagerUnused pins the spec
// §8.2 invariant: 旧 StateManager 不得参与 authoritative 健康判定.
// We wire a StateManager that BLOCKS every candidate by default. If
// the router ever consults it on the authoritative path, the
// failure would manifest as a `spy.calls > 0` reading. The strict
// spec §8.2 强不变量 is therefore re-stated as:
//   spy.calls == 0
// (the previous assertion `spy.calls > 0 && !spy.sawAtLeastOneAvailable()`
// was a weak check because the spy defaulted to available=true and
// the right-hand side was a no-op; the assertion effectively
// degenerated to "calls > 0 implies fail" but never tested the
// invariant itself.)
func TestPlanCandidates_Authoritative_StateManagerUnused(t *testing.T) {
	statesource.ResetForTest()
	mgr, mr := buildV2Manager(t, api.ModeAuthoritative)
	seedV2Node(t, mr, 1, "m", 1, true)
	seedV2Node(t, mr, 2, "m", 1, true)

	// Legacy StateManager that BLOCKS every candidate by default.
	// If the router ever consults it on the authoritative path,
	// spy.calls increments — the spec §8.2 强不变量 forbids that.
	spy := &spyStateProvider{enabled: true}

	r := NewRouter(nil, nil)
	r.URSMv2 = mgr
	r.StateManager = spy

	_ = r.PlanCandidatesWithContext(
		context.Background(), candidateSet(), nil, &provider.Policy{}, nil, "t", "m", "req-6",
	)
	// The source counter must report authoritative (not fallback)
	// because the v2 filter was the authority for this request.
	snap := statesource.Snapshot()
	if got := snap[statesource.StateSourceAuthoritative]; got != 1 {
		t.Fatalf("authoritative count = %d, want 1; full snapshot = %+v", got, snap)
	}
	// Spec §8.2 强不变量: authoritative 模式下 StateManager 必须
	// 零调用. 任何 spy.calls > 0 都是 spec §8.2 违规.
	if spy.calls != 0 {
		t.Fatalf("authoritative path consulted StateManager %d times; spec §8.2 forbids any live calls", spy.calls)
	}
}

// spyStateProvider counts IsAvailable calls and tracks per-cred
// availability. Used by the S-3 spec-§8.2 invariant test. The spy
// defaults to BLOCKING (available=false, reason="blocked") so that
// any live consultation by the router would be detected by the
// routing outcome (zero candidates) rather than masked by a
// permissive legacy default.
type spyStateProvider struct {
	enabled bool
	calls   int
	seen    map[string]bool
}

func (s *spyStateProvider) Enabled() bool { return s.enabled }
func (s *spyStateProvider) IsAvailable(_ context.Context, credID int, model string) (bool, string) {
	s.calls++
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	key := itoaCredModel(credID, model)
	s.seen[key] = true
	// Block by default: any consultation by the router will
	// reject this candidate. The spec §8.2 strong invariant is
	// that the authoritative path never invokes this method at
	// all (call count == 0), so the block default is just a
	// safety net for observability.
	return false, "blocked"
}
func (s *spyStateProvider) GetState(_ context.Context, _ int, _ string) (*credentialstate.State, error) {
	return nil, nil
}
func itoaCredModel(credID int, model string) string {
	return strconv.Itoa(credID) + ":" + model
}
