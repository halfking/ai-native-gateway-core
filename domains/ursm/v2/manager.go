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
	return &Manager{
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
	if !m.Ready(ctx) {
		return nil, fmt.Errorf("ursm.v2: not ready")
	}
	if m.Mode() == api.ModeOff {
		return nil, nil
	}
	queries := make([]store.NodeQuery, 0, len(seeds))
	for _, s := range seeds {
		queries = append(queries, store.NodeQuery{CredentialID: s.CredentialID, RawModel: s.RawModel})
	}
	views, err := m.store.PipelineNodeViews(ctx, m.cfg.RedisKeyPrefix, queries)
	if err != nil {
		return nil, fmt.Errorf("ursm.v2: pipeline: %w", err)
	}
	// 评分：price 0.4 + latency 0.4 + stability 0.2 (SR5m)；lat 缺失回退 baseURLMs
	for i := range views {
		s := seeds[i]
		v := &views[i]
		price := s.PriceIn + s.PriceOut
		lat := v.LatEWMA
		if lat == 0 {
			lat = s.BaseURLMs
		}
		weights := m.cfg.ScoringWeights
		v.Score = weights.Price*price + weights.Latency*float64(lat) + weights.Stability*v.SR5m*1000
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Score < views[j].Score })
	return views, nil
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
	if m.Mode() == api.ModeOff {
		return nil
	}
	// Note: tenant/canonical are accepted for future filters (e.g. per-tenant
	// overrides) but T16 does not use them — the rollout gate lives in
	// rollout.ShouldUseV2, and T16 callers do not yet plumb tenant through.
	_ = tenant
	_ = canonical

	views, err := m.FilterAndScore(ctx, seeds)
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
		ProviderID: s.ProviderID, CredentialID: s.CredentialID, RawModel: s.RawModel, Available: true,
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
	if _, err := m.store.RecordRequest(rctx,
		store.NodeKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel),
		store.WindowKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel, "1m"),
		store.WindowKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel, "5m"),
		store.WindowKey(m.cfg.RedisKeyPrefix, ev.CredentialID, ev.RawModel, "30m"),
		store.RecordOutcome{
			Success:      ev.Success,
			ErrorKind:    ev.ErrorKind,
			NowMs:        time.Now().UnixMilli(),
			LatencyMs:    ev.LatencyMs,
			RequestID:    ev.RequestID,
			NodeTTL:      m.cfg.NodeTTL,
			Window5mTTL:  m.cfg.Window5mTTL,
			Window30mTTL: m.cfg.Window30mTTL,
			AdminHold:    false,
		}); err != nil {
		m.log.Warn("ursm.v2: record failed", "error", err, "cid", ev.CredentialID)
		return fmt.Errorf("ursm.v2: record: %w", err)
	}
	return nil
}
