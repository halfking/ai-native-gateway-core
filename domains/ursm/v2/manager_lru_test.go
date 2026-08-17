package v2

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
)

// newMirrorManager builds a Manager in authoritative mode with the LRU mirror
// enabled, against a fresh miniredis. Helper for the M2 LRU tests.
func newMirrorManager(t *testing.T) (*Manager, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 100
	cfg.LRUMirrorSoftTTL = 200 * time.Millisecond
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	return mgr, mr, rdb
}

// stubHotSource is a test HotConfigSource (会话优化 v4 T5). Zero entries
// behave exactly like a missing settings_kv row: boot defaults win.
type stubHotSource struct {
	ints  map[string]int
	bools map[string]bool
}

func (s stubHotSource) GetInt(key string, defaultValue int) int {
	if v, ok := s.ints[key]; ok {
		return v
	}
	return defaultValue
}

func (s stubHotSource) GetBool(key string, defaultValue bool) bool {
	if v, ok := s.bools[key]; ok {
		return v
	}
	return defaultValue
}

// seedNode writes a node Hash into miniredis so PipelineNodeViews returns it.
func seedNode(t *testing.T, mr *miniredis.Miniredis, credID int, model string, gen int64, available bool, latEWMA int, sr5m float64) {
	t.Helper()
	key := "ursm:v2:node:t:" + strconv.Itoa(credID) + ":" + model
	avail := "1"
	if !available {
		avail = "0"
	}
	mr.HSet(key, "available", avail)
	mr.HSet(key, "generation", strconv.FormatInt(gen, 10))
	mr.HSet(key, "source_priority", "10")
	mr.HSet(key, "lat_ewma_ms", strconv.Itoa(latEWMA))
	mr.HSet(key, "sr_5m", strconv.FormatFloat(sr5m, 'f', -1, 64))
	mr.HSet(key, "fail_streak", "0")
}

// TestFilterAndScore_LRUMissThenHit verifies the M2 fast path: the first call
// misses the LRU, reads Redis, and backfills; the second call hits the LRU.
//
// 会话优化 v4 §14.3 (2026-08-18): the gear contract changed — a full mirror
// hit is no longer an unconditional fail-open when Redis dies. Two gears are
// pinned here:
//   - target gear (default): mirror hit + Redis dead → protective rejection;
//   - grace gear (llmgw_ursm_mirror_grace_enabled=true): mirror hit within
//     soft TTL serves degraded read-only routing with zero Redis IO.
//
// NOTE: PipelineNodeViews currently populates availability/fail_streak/
// generation/cool_until but NOT latency/SR fields beyond what the node hash
// carries. The cache mirrors whatever the store returns today. So this test
// asserts on availability + the gear behavior, not on latency values.
func TestFilterAndScore_LRUMissThenHit(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 7, "gpt-x", 1, true, 120, 0.95)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 7, RawModel: "gpt-x", TenantID: "t", BaseURLMs: 200}}

	// First call: LRU miss → Redis read → backfill.
	views, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.True(t, views[0].Available, "first read must come from Redis")

	// Target gear: kill Redis — the full-hit call must now be protectively
	// rejected (P0-1 residual closure; the mirror may not serve state of
	// unknown freshness when the authoritative store is unreachable).
	mr.Close()
	_, err = mgr.FilterAndScoreReady(context.Background(), seeds, true)
	require.Error(t, err, "default gear: mirror hit + Redis down must protectively reject")
	assert.Contains(t, err.Error(), "redis unavailable",
		"rejection must name Redis so the router falls back, not serve stale mirror state")

	// Grace gear: same dead-Redis situation, but llmgw_ursm_mirror_grace_enabled
	// flips the manager to the availability-over-consistency gear. The fresh
	// (within soft TTL) mirror entry serves degraded read-only routing.
	mgr.SetHotConfig(stubHotSource{bools: map[string]bool{HotKeyMirrorGrace: true}})
	views2, err := mgr.FilterAndScoreReady(context.Background(), seeds, true)
	require.NoError(t, err, "grace gear: fresh mirror hit must serve without Redis IO")
	require.Len(t, views2, 1)
	assert.True(t, views2[0].Available, "grace gear: mirror-cached availability must survive Redis death")
}

// TestFilterAndScore_LRUSoftExpireRefetchesRedis verifies a soft-expired LRU
// entry is NOT trusted and falls back to Redis (so a cooled/disabled node is
// observed within the soft-TTL window, not served stale forever).
func TestFilterAndScore_LRUSoftExpireRefetchesRedis(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	// soft TTL is 200ms (see newMirrorManager).
	seedNode(t, mr, 9, "m", 1, true, 50, 0.9)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 9, RawModel: "m", TenantID: "t"}}
	_, err := mgr.FilterAndScore(context.Background(), seeds) // populate LRU
	require.NoError(t, err)

	// Wait past soft TTL, then flip the node to unavailable in Redis.
	time.Sleep(250 * time.Millisecond)
	mr.HSet("ursm:v2:node:t:9:m", "available", "0")
	mr.HSet("ursm:v2:node:t:9:m", "generation", "2")

	views, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.False(t, views[0].Available,
		"soft-expired entry must refetch Redis and observe the new available=false")
}

// TestNodeMirror_StaleGenRejected verifies the generation-monotonic contract
// (spec Decision 2 / apply_decision.lua:25): a backfill with an OLDER
// generation must not overwrite a newer LRU entry. Without this, a slow Redis
// read returning a stale snapshot could regress a freshly-cooled node.
func TestNodeMirror_StaleGenRejected(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 11, "m", 5, false, 10, 0.5) // gen=5, unavailable

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 11, RawModel: "m", TenantID: "t"}}
	_, err := mgr.FilterAndScore(context.Background(), seeds) // LRU gets gen=5, unavailable
	require.NoError(t, err)

	// Now try to backfill a STALE entry (gen=3, available=true) directly via
	// ApplyFromAPI. This must be REJECTED — the LRU must keep gen=5/unavailable.
	mgr.nodeMirror.ApplyFromAPI(apiNodeView(11, "m", 3, true))

	// Kill Redis so only the LRU can answer, and enable the grace gear so the
	// mirror is allowed to serve (会话优化 v4 §14.3: the default gear would
	// protectively reject a mirror-only answer while Redis is down, which is
	// pinned separately in TestFilterAndScore_LRUMissThenHit).
	mr.Close()
	mgr.SetHotConfig(stubHotSource{bools: map[string]bool{HotKeyMirrorGrace: true}})
	views, err := mgr.FilterAndScoreReady(context.Background(), seeds, true)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.False(t, views[0].Available,
		"stale gen=3 backfill must not overwrite gen=5 — newer (unavailable) state wins")
}

// TestFilterAndScore_LRUDisabledWhenSizeZero verifies LRUMirrorSize==0
// disables the mirror (every read hits Redis), preserving the off-switch
// required by the spec's Compatibility & Rollback section.
func TestFilterAndScore_LRUDisabledWhenSizeZero(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	cfg := DefaultConfig()
	cfg.Mode = api.ModeAuthoritative
	cfg.LRUMirrorSize = 0 // mirror disabled
	mgr := New(Dependencies{Redis: rdb, Config: cfg})
	_ = mgr.SetReady(context.Background(), true)
	require.Nil(t, mgr.nodeMirror, "LRUMirrorSize==0 must not construct a mirror")

	seedNode(t, mr, 13, "m", 1, true, 80, 0.8)
	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 13, RawModel: "m", TenantID: "t"}}
	views, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	assert.True(t, views[0].Available)

	// With no mirror, killing Redis must break the read (proves it was hitting Redis).
	mr.Close()
	_, err = mgr.FilterAndScore(context.Background(), seeds)
	require.Error(t, err, "no mirror → Redis death must surface (mirror was disabled)")
}

// TestFilterAndScoreReady_RejectsMirrorWhenNotReady pins the audit invariant
// (docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.3):
// the NodeMirror LRU is a read accelerator, NOT a bypass of the recovery
// gate. When the caller passes ready=false, the manager must return
// "ursm.v2: not ready" even if every requested seed is already cached —
// otherwise a stale recovery snapshot could keep serving traffic after the
// pipeline has been closed for incidents.
//
// 2026-07-28 audit regression: there is a second, duplicate `if !ready` check
// at manager.go:~line 240 guarding the miss path. That check is currently
// dead code (line 230 already returned), but it is an intentional safeguard
// — this test makes sure the safeguard's contract (ready=false always
// rejects) cannot be removed by accident during a refactor.
func TestFilterAndScoreReady_RejectsMirrorWhenNotReady(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 21, "m", 1, true, 50, 0.9)

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 21, RawModel: "m", TenantID: "t"}}

	// First populate the LRU with ready=true. The mirror must be warm now.
	_, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	require.NotNil(t, mgr.nodeMirror, "mirror must be enabled for this test")

	// Kill Redis so ONLY the LRU could answer. The contract under test is:
	// even with the mirror populated, ready=false MUST reject.
	mr.Close()

	_, err = mgr.FilterAndScoreReady(context.Background(), seeds, false)
	require.Error(t, err, "ready=false must reject even when the LRU has the answer")
	require.Contains(t, err.Error(), "not ready",
		"rejection reason must be explicit so router.go can fall back to LegacyStateBackend")

	// And ready=true on the same data still works in the GRACE gear (the
	// default gear protectively rejects mirror-only answers while Redis is
	// down — §14.3, pinned in TestFilterAndScore_LRUMissThenHit). The grace
	// gear keeps the afb13c9ea contract observable: readiness, not mirror
	// contents, is what gates the answer.
	mgr.SetHotConfig(stubHotSource{bools: map[string]bool{HotKeyMirrorGrace: true}})
	views, err := mgr.FilterAndScoreReady(context.Background(), seeds, true)
	require.NoError(t, err, "ready=true must consult the LRU when Redis is down (grace gear)")
	require.Len(t, views, 1)
	assert.True(t, views[0].Available,
		"mirror-cached availability must survive when ready=true (grace gear)")
}

func TestPlanReadyObservedDoesNotPopulateMirror(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	seedNode(t, mr, 23, "m", 1, true, 50, 0.9)
	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 23, RawModel: "m", TenantID: "t"}}

	before := statesource.Snapshot()
	ordered, err := mgr.PlanReadyObserved(context.Background(), seeds, "t", "m", true)
	require.NoError(t, err)
	require.Len(t, ordered, 1)
	assert.Equal(t, before, statesource.Snapshot(), "observe-only planning must not record routing sources")
	_, cached := mgr.nodeMirror.PeekForTenant("t", 23, "m")
	assert.False(t, cached, "observe-only planning must not backfill production mirror state")
}

func TestManagerCloseIsIdempotent(t *testing.T) {
	mgr, _, _ := newMirrorManager(t)
	mgr.Close()
	mgr.Close()
}

func apiNodeView(credID int, model string, gen int64, available bool) api.NodeView {
	return api.NodeView{
		CredentialID: credID,
		RawModel:     model,
		Available:    available,
		Generation:   gen,
		SrcPriority:  10,
	}
}

// TestMirrorServesHealthStatusBridge pins the manager half of UT-UR-12: the
// "health" hash field written by record_request.lua flows through
// PipelineNodeViews → cache.NodeView (ApplyFromAPI) → mirrorToAPIView, so a
// full mirror hit surfaces the same HealthStatus a fresh Redis read would.
//
// BOUNDARY: the field is display-only — availability filtering (Available)
// still decides routing eligibility and must not consult HealthStatus.
func TestMirrorServesHealthStatusBridge(t *testing.T) {
	mgr, mr, _ := newMirrorManager(t)
	key := "ursm:v2:node:t:31:hm"
	mr.HSet(key, "available", "1")
	mr.HSet(key, "generation", "4")
	mr.HSet(key, "source_priority", "10")
	mr.HSet(key, "health", "quarantined")
	mr.HSet(key, "lat_ewma_ms", "111")
	mr.HSet(key, "sr_5m", "0.9")

	seeds := []CandidateSeed{{ProviderID: 1, CredentialID: 31, RawModel: "hm", TenantID: "t"}}

	// Miss → Redis read (HealthStatus read + backfilled into the mirror).
	views, err := mgr.FilterAndScore(context.Background(), seeds)
	require.NoError(t, err)
	require.Len(t, views, 1)
	assert.Equal(t, "quarantined", views[0].HealthStatus, "Redis read must surface the bridged health field")

	// Full mirror hit (Redis alive, default gear PING passes): the mirror
	// view carries the same HealthStatus.
	views2, _, err := mgr.FilterAndScoreReadyWithSource(context.Background(), seeds, true)
	require.NoError(t, err)
	require.Len(t, views2, 1)
	assert.Equal(t, "quarantined", views2[0].HealthStatus, "mirror hit must surface the bridged health field")
	assert.True(t, views2[0].Available,
		"routing eligibility stays decided by available=1 — the quarantined DISPLAY state must not filter the node")
}
