// Package v2 facade. Manager is the wiring point for URSM v2 in the
// executor: it composes the v2 store, recovery gate, and rollout
// controller, and exposes the minimal surface the executor needs to
// make a sidecar write when a request succeeds or fails.
package v2

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/resource"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/rollout"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/sync"
)

// Dependencies wires the v2 facade. Redis is required; the resource
// pools and Logger are optional. Config is filled with DefaultConfig()
// when RedisKeyPrefix is empty.
type Dependencies struct {
	Redis  *redis.Client
	FP     resource.FP
	Conc   resource.Concurrency
	RPM    resource.RPM
	Config Config
	Logger Logger
}

// Logger is the minimal surface v2 needs. A no-op logger is used when
// Logger is nil.
type Logger interface {
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
}

type nopLogger struct{}

func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Error(string, ...any) {}

// Manager is the URSM v2 facade. It is safe to leave unconfigured
// (URSMv2 == nil on Executor): the executor sidecar guards each call
// on `e.URSMv2 != nil`.
type Manager struct {
	cfg      Config
	store    *store.Store
	recovery *recovery.Manager
	rollout  *rollout.Controller
	fp       resource.FP
	conc     resource.Concurrency
	rpm      resource.RPM
	log      Logger
	// nodeMirror is the process-local LRU read accelerator for node views
	// (M2, spec Decision 2). nil when LRUMirrorSize==0 (mirror disabled).
	// Always a read-only replica of Redis; never written before a successful
	// Redis Lua write.
	nodeMirror *cache.NodeMirror
}

// New constructs a Manager. If d.Config.RedisKeyPrefix is empty the
// facade falls back to DefaultConfig() — the caller still retains
// whatever Mode / Canary settings they passed in via the same struct
// only when the prefix is non-empty; otherwise the entire default
// config is used. (This is the T8 spec behavior; we'll revisit the
// "field-by-field defaults" question in a later task.)
func New(d Dependencies) *Manager {
	if d.Config.RedisKeyPrefix == "" {
		d.Config = DefaultConfig()
	}
	cfg := d.Config
	log := d.Logger
	if log == nil {
		log = nopLogger{}
	}
	m := &Manager{
		cfg:      cfg,
		store:    store.New(d.Redis),
		recovery: recovery.New(d.Redis, cfg.RedisKeyPrefix),
		rollout: rollout.New(rollout.Config{
			Mode:          cfg.Mode,
			CanaryPercent: cfg.CanaryPercent,
			CanaryTenants: cfg.CanaryTenants,
			CanaryModels:  cfg.CanaryModels,
		}),
		fp:   d.FP,
		conc: d.Conc,
		rpm:  d.RPM,
		log:  log,
	}
	// M2: enable the process LRU mirror when configured (default 100k / 30s).
	// LRUMirrorSize==0 disables it (every read hits Redis).
	if cfg.LRUMirrorSize > 0 {
		m.nodeMirror = cache.NewNodeMirror(cfg.LRUMirrorSize, cfg.LRUMirrorSoftTTL)
	}
	return m
}

// Mode returns the active rollout mode.
func (m *Manager) Mode() api.RolloutMode {
	if m == nil {
		return api.ModeOff
	}
	return m.rollout.Mode()
}

// ShouldUseV2 delegates to the rollout controller.
func (m *Manager) ShouldUseV2(tenant, model, requestID string) bool {
	if m == nil {
		return false
	}
	return m.rollout.ShouldUseV2(tenant, model, requestID)
}

// Ready returns the v2 recovery gate state. False means the v2
// pipeline is not authoritative yet; callers should treat v2 as off.
func (m *Manager) Ready(ctx context.Context) bool {
	if m == nil {
		return false
	}
	return m.recovery.Ready(ctx)
}

// SetReady toggles the v2 recovery gate. Used by recovery / boot flows
// and exercised by the manager tests.
func (m *Manager) SetReady(ctx context.Context, ready bool) error {
	if m == nil {
		return nil
	}
	return m.recovery.SetReady(ctx, ready)
}

// MarkClosedDebounced is the cluster-coordinated variant of
// EnterRecovery. It is wired to bg/systemmonitor's healthCheckLoop so
// that persistent Redis health failures automatically close the v2
// authoritative gate and stamp incident metadata (epoch counter,
// reason, started_at). The debounce window ensures the epoch counter
// increments at most once per window per failure event regardless of
// how many gateway instances observe the failure simultaneously.
//
// See recovery.Manager.MarkClosedDebounced for the full contract and
// docs/architecture/2026-07-28-routing-state-anomaly-audit.md §4.1 for
// the audit motivation.
func (m *Manager) MarkClosedDebounced(ctx context.Context, reason string, debounceTTL time.Duration) (bool, error) {
	if m == nil {
		return false, nil
	}
	return m.recovery.MarkClosedDebounced(ctx, reason, debounceTTL)
}

// WarmupFromExistingKeys re-opens the recovery gate from the keys
// already in Redis. It is the audit follow-up #6 counterpart to
// MarkClosedDebounced (follow-up #1): together they implement the full
// incident lifecycle — auto-close on persistent failure, auto-reopen
// on Redis recovery — without operator intervention.
//
// See recovery.Manager.WarmupFromExistingKeys for the full contract.
func (m *Manager) WarmupFromExistingKeys(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	return m.recovery.WarmupFromExistingKeys(ctx)
}

// RestoreIfClosed is the convenience wrapper for the
// fallback→healthy transition path: opens the gate iff it is
// currently closed, returning the observed key count for audit.
//
// See recovery.Manager.RestoreIfClosed for the full contract.
func (m *Manager) RestoreIfClosed(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	return m.recovery.RestoreIfClosed(ctx)
}

// RecoveryStats returns the observability snapshot for the recovery
// gate — last error (with timestamp) and last successful reopen
// (with timestamp + observed key count). Safe on a nil receiver;
// returns the zero value when no operations have happened.
//
// Audit follow-up #4: closes the gap that the recovery gate has no
// metrics surface. Admin endpoints and Prometheus exporters consume
// this snapshot for "time since last recovery" and "last error"
// dashboards.
func (m *Manager) RecoveryStats() recovery.Stats {
	if m == nil {
		return recovery.Stats{}
	}
	return m.recovery.Stats()
}

// LastRecoveryKeyCount returns the key count observed on the most
// recent successful reopen. Safe on a nil receiver.
func (m *Manager) LastRecoveryKeyCount() int {
	if m == nil {
		return 0
	}
	return m.recovery.LastRecoveryKeyCount()
}

// CandidateSeed is the input to FilterAndScore. The router hands one
// seed per candidate it is considering; the manager resolves each seed
// against the v2 store to produce a NodeView. Fields beyond
// CredentialID and RawModel are forwarded by the executor — the v2
// scoring step (Task 14) will use PriceIn/PriceOut/BillingMode/Trust
// and BaseURLMs to compute the final score.
type CandidateSeed struct {
	ProviderID   int
	CredentialID int
	RawModel     string
	Canonical    string
	TenantID     string
	PriceIn      float64
	PriceOut     float64
	BillingMode  string
	Trust        float64
	BaseURLMs    int
}

// FilterAndScore resolves candidate seeds against the v2 store.
//
// Behavior:
//   - nil receiver → error (defensive),
//   - not ready → error (the v2 pipeline is not authoritative yet),
//   - ModeOff → returns (nil, nil) — off mode skips work,
//   - non-off + ready → calls store.PipelineNodeViews and returns the
//     views. Missing Redis keys default Available=false (T4 contract),
//     which is the protection-rejection invariant: a candidate with
//     no observed telemetry is treated as not available.
//
// Pipeline errors are wrapped with %w so callers can errors.Is /
// errors.As against store.ErrRedisUnavailable. Scoring logic lands in
// Task 14; T12 only establishes the read path with the ready gate.
func (m *Manager) FilterAndScore(ctx context.Context, seeds []CandidateSeed) ([]api.NodeView, error) {
	if m == nil {
		return nil, fmt.Errorf("ursm.v2: nil manager")
	}
	if m.Mode() == api.ModeOff {
		return nil, nil
	}
	return m.filterAndScore(ctx, seeds, m.Ready(ctx))
}

// FilterAndScoreReady evaluates seeds using a Ready result captured by the
// caller. Routers use this to keep backend selection and URSM filtering on the
// same recovery snapshot; a Ready flip cannot split one request between v2 and
// the legacy state manager.
func (m *Manager) FilterAndScoreReady(ctx context.Context, seeds []CandidateSeed, ready bool) ([]api.NodeView, error) {
	if m == nil {
		return nil, fmt.Errorf("ursm.v2: nil manager")
	}
	if m.Mode() == api.ModeOff {
		return nil, nil
	}
	return m.filterAndScore(ctx, seeds, ready)

}

func (m *Manager) filterAndScore(ctx context.Context, seeds []CandidateSeed, ready bool) ([]api.NodeView, error) {
	// M2 (2026-07-27, spec Decision 2 + Decision 3 fail-open): serve the hot
	// path from the process LRU mirror first. If EVERY seed hits the mirror,
	// the caller's Ready snapshot still governs whether this result may be used
	// as authoritative routing state.
	// path from the process LRU mirror first. If EVERY seed hits the mirror,
	// we return WITHOUT consulting Redis or the Ready gate — this is the
	// "Redis 不可达 → LRU 镜像" fail-open path. Only on a miss do we require
	// Ready + Redis. The mirror is a read-only replica, backfilled only AFTER
	// a Redis read; applyToLRU enforces the generation-monotonic contract so a
	// stale snapshot can never overwrite a newer LRU entry.
	views := make([]api.NodeView, len(seeds))
	missIndices := make([]int, 0, len(seeds))
	if m.nodeMirror != nil {
		for i, s := range seeds {
			if mv, ok := m.nodeMirror.GetForTenant(s.TenantID, s.CredentialID, s.RawModel); ok {
				views[i] = mirrorToAPIView(mv, s)
				continue
			}
			missIndices = append(missIndices, i)
		}
	} else {
		for i := range seeds {
			missIndices = append(missIndices, i)
		}
	}

	// A false readiness snapshot must always fall back, including when the
	// process mirror contains every requested node. The mirror is only a
	// read accelerator; it cannot bypass the recovery gate.
	if !ready {
		return nil, fmt.Errorf("ursm.v2: not ready")
	}
	if len(missIndices) == 0 {
		scoreAndSort(views, seeds, m.cfg.ScoringWeights)
		return views, nil
	}

	// A Redis miss must use the caller's request snapshot. A false snapshot
	// rejects the miss, while a mirror-only request remains fail-open above.
	// authoritative read). A false snapshot must never be bypassed by the LRU.
	if !ready {
		return nil, fmt.Errorf("ursm.v2: not ready")
	}
	missQueries := make([]store.NodeQuery, 0, len(missIndices))
	for _, idx := range missIndices {
		missQueries = append(missQueries, store.NodeQuery{
			TenantID:     seeds[idx].TenantID,
			CredentialID: seeds[idx].CredentialID,
			RawModel:     seeds[idx].RawModel,
		})
	}
	fetched, err := m.store.PipelineNodeViews(ctx, m.cfg.RedisKeyPrefix, missQueries)
	if err != nil {
		return nil, fmt.Errorf("ursm.v2: pipeline: %w", err)
	}
	for j, idx := range missIndices {
		views[idx] = fetched[j]
		// Backfill the mirror from the authoritative Redis read. applyToLRU
		// is generation-safe (rejects stale writes), so this never lets an
		// older snapshot overwrite a newer LRU entry.
		if m.nodeMirror != nil {
			fetched[j].TenantID = seeds[idx].TenantID
			m.nodeMirror.ApplyFromAPI(fetched[j])
		}
	}

	scoreAndSort(views, seeds, m.cfg.ScoringWeights)
	return views, nil
}

// scoreAndSort applies the price/latency/stability scoring and orders views
// by ascending Score. Extracted so the LRU fast path and the Redis miss path
// share identical scoring.
func scoreAndSort(views []api.NodeView, seeds []CandidateSeed, weights ScoringWeights) {
	// 评分：price 0.4 + latency 0.4 + stability 0.2 (SR5m)；lat 缺失回退 baseURLMs
	for i := range views {
		s := seeds[i]
		v := &views[i]
		price := s.PriceIn + s.PriceOut
		lat := v.LatEWMA
		if lat == 0 {
			lat = s.BaseURLMs
		}
		v.Score = weights.Price*price + weights.Latency*float64(lat) + weights.Stability*v.SR5m*1000
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Score < views[j].Score })
}

// mirrorToAPIView expands a cached NodeView (the score-relevant subset) into
// an api.NodeView for the scoring loop. Fields not held in the cache (SR1m,
// Samples*, LatP50/P95, HealthStatus) are left zero — they are not read by
// the scorer, only by observers, and a soft-expired entry would have missed
// the LRU anyway (so we never score off a stale entry).
func mirrorToAPIView(mv cache.NodeView, s CandidateSeed) api.NodeView {
	return api.NodeView{
		ProviderID:    s.ProviderID,
		CredentialID:  mv.CredentialID,
		RawModel:      mv.RawModel,
		CanonicalName: s.Canonical,
		TenantID:      s.TenantID,
		Available:     mv.Available,
		Reason:        mv.Reason,
		FailStreak:    mv.FailStreak,
		CoolUntil:     mv.CoolUntil,
		LatEWMA:       mv.LatEWMA,
		SR5m:          mv.SR5m,
	}
}

// Plan returns the input seeds filtered by v2 availability (Available==true)
// and ordered by ascending Score. It is the canary/authoritative entry point
// the executor's PlanCandidates calls when delegating routing to v2.
//
// Design notes (URSM v2 plan T16, 2026-07-21):
//   - Works in CandidateSeed space (not provider.Candidate) so the v2 manager
//     stays decoupled from the executor's richer candidate shape. The
//     executor is responsible for building seeds from []provider.Candidate
//     and mapping the returned seeds back to the upstream candidates.
//   - Returns nil on nil receiver, on ModeOff, when not Ready, or when
//     FilterAndScore errors — the executor falls through to its existing
//     ordering slice on nil. This preserves the "v2 is strictly advisory
//     until authoritative" invariant.
//   - Score is set by FilterAndScore (price 0.4 + latency 0.4 + stability 0.2).
//     Order is ascending: lower score = higher priority (matches FilterAndScore).
func (m *Manager) Plan(ctx context.Context, seeds []CandidateSeed, tenant, canonical string) []CandidateSeed {
	if m == nil {
		return nil
	}
	// M3 (2026-07-28): skip the Ready Redis IO when the rollout controller
	// would short-circuit anyway. plan() also checks ModeOff but Plan()
	// already needs the Ready result, so guarding here saves a round-trip
	// in the off-mode default — which is the production rollout state today.
	if m.Mode() == api.ModeOff {
		return nil
	}
	return m.plan(ctx, seeds, tenant, canonical, m.Ready(ctx))
}

// PlanReady is the request-snapshot variant of Plan. The caller supplies the
// already captured recovery-gate result so Plan cannot observe a different
// Ready value from the rest of the routing decision.
func (m *Manager) PlanReady(ctx context.Context, seeds []CandidateSeed, tenant, canonical string, ready bool) []CandidateSeed {
	if m == nil {
		return nil
	}
	return m.plan(ctx, seeds, tenant, canonical, ready)
}

func (m *Manager) plan(ctx context.Context, seeds []CandidateSeed, tenant, canonical string, ready bool) []CandidateSeed {
	if m == nil {
		return nil
	}
	if m.Mode() == api.ModeOff {
		return nil
	}
	// Note: tenant/canonical are accepted for future filters and are kept in
	// the request seed by the router.

	views, err := m.FilterAndScoreReady(ctx, seeds, ready)

	if err != nil {
		m.log.Warn("ursm.v2: Plan filter failed, falling back", "error", err, "seed_count", len(seeds))
		return nil
	}
	if len(views) == 0 {
		return nil
	}
	// Build a lookup keyed by (CredentialID, RawModel) so we can map back to
	// the input seeds while preserving FilterAndScore's ordering (and its
	// availability filter — FilterAndScore returns one view per input seed
	// with Available=false on missing Redis data).
	idx := make(map[string]int, len(seeds))
	for i, s := range seeds {
		idx[seedKey(s.CredentialID, s.RawModel)] = i
	}
	out := make([]CandidateSeed, 0, len(views))
	for _, v := range views {
		if !v.Available {
			continue
		}
		i, ok := idx[seedKey(v.CredentialID, v.RawModel)]
		if !ok {
			continue
		}
		out = append(out, seeds[i])
	}
	return out
}

// seedKey joins a credential/model pair into a single string for use as a
// lookup map key. Cheap and avoids fmt.Sprintf allocations on the hot path.
func seedKey(credentialID int, rawModel string) string {
	return strconv.Itoa(credentialID) + "|" + rawModel
}

// SetRedisForTest swaps the underlying redis client used by the v2
// store. It is a test-only helper used by integration tests that need
// to simulate a Redis restart (the store is rebuilt against a fresh
// miniredis) without reconstructing the whole Manager. The recovery
// gate is *not* swapped; tests that exercise the ready gate after a
// restart should construct a fresh recovery.Manager against the new
// client rather than relying on the original m.recovery. The name
// ends in "ForTest" so a future linter can flag production callers.
func (m *Manager) SetRedisForTest(rdb *redis.Client) {
	if m == nil || m.store == nil {
		return
	}
	m.store = m.store.WithRedis(rdb)
}

// SetSeedForTest is a test-only helper that writes a Seed for the given
// (CredentialID, RawModel) pair via the config syncer. It is intentionally
// narrow: only enough fields to make FilterAndScore's read path happy
// (Available=true is the only state that matters here). Production code
// must not call this; the syncer is wired through the boot/recovery flow.
func (m *Manager) SetSeedForTest(ctx context.Context, s CandidateSeed) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("ursm.v2: nil manager/store")
	}
	syncer := sync.NewSyncer(m.store.RawClient(), m.cfg.RedisKeyPrefix)
	return syncer.UpsertNodeSeed(ctx, sync.Seed{
		ProviderID: s.ProviderID, CredentialID: s.CredentialID, RawModel: s.RawModel,
		TenantID: s.TenantID, Available: true,
	})
}

// RecordRequest is the executor sidecar entry point. It is a no-op
// when:
//
//   - the receiver is nil (defensive — protects against bad wiring),
//   - the store is not configured,
//   - the rollout controller decides this request is not on the v2
//     path (ModeOff always; ModeShadow by design; ModeCanary
//     unless the canary hash / tenant / model matches),
//
// On the success/failure path the call is forwarded to the v2 store
// via the RecordRequest Lua script. The context is detached
// (context.WithoutCancel) so a cancelled request context does not
// abort the sidecar write; the configured RecordTimeoutMs bounds the
// wait.
//
// The reducer package is wired in Task 9 — kept out of T8 to keep
// this change focused on the sidecar write.
func (m *Manager) RecordRequest(ctx context.Context, ev api.RequestOutcome) error {
	if m == nil || m.store == nil {
		return nil
	}
	if !m.rollout.ShouldUseV2(ev.TenantID, ev.RawModel, ev.RequestID) {
		return nil
	}
	timeout := time.Duration(m.cfg.RecordTimeoutMs) * time.Millisecond
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	// M3 (2026-07-28): record_request.lua now reads manual_hold directly
	// inside the script (single atomic Redis op), so we no longer pre-read
	// manual_hold here. This:
	//   - closes the prior TOCTOU window where ApplyAdmin could flip
	//     manual_hold between Go-side HGet and Lua Run;
	//   - saves one hot-path RTT (2 IO → 1 IO per record);
	//   - admin priority still dominates via the lua-internal manual_hold
	//     read.
	// Keys are tenant-aware (afb13c9ea, 2026-07-28) — empty TenantID falls
	// back to the legacy non-tenant key.
	nodeKey := store.NodeKeyForTenant(m.cfg.RedisKeyPrefix, ev.TenantID, ev.CredentialID, ev.RawModel)
	window1m := store.WindowKeyForTenant(m.cfg.RedisKeyPrefix, ev.TenantID, ev.CredentialID, ev.RawModel, "1m")
	window5m := store.WindowKeyForTenant(m.cfg.RedisKeyPrefix, ev.TenantID, ev.CredentialID, ev.RawModel, "5m")
	window30m := store.WindowKeyForTenant(m.cfg.RedisKeyPrefix, ev.TenantID, ev.CredentialID, ev.RawModel, "30m")
	if _, err := m.store.RecordRequest(rctx,
		nodeKey,
		window1m,
		window5m,
		window30m,
		store.RecordOutcome{
			Success:      ev.Success,
			ErrorKind:    ev.ErrorKind,
			NowMs:        time.Now().UnixMilli(),
			LatencyMs:    ev.LatencyMs,
			RequestID:    ev.RequestID,
			NodeTTL:      m.cfg.NodeTTL,
			Window5mTTL:  m.cfg.Window5mTTL,
			Window30mTTL: m.cfg.Window30mTTL,
			AdminHold:    false, // deprecated; handled in lua
			// 2026-07-24: 使用配置的冷却时间，与 circuit breaker 冷却时间对齐
			CoolSeconds:     m.cfg.CoolSeconds,
			FailStreakLimit: 3,
		}); err != nil {
		m.log.Warn("ursm.v2: record failed", "error", err, "cid", ev.CredentialID)
		return fmt.Errorf("ursm.v2: record: %w", err)
	}
	return nil
}
