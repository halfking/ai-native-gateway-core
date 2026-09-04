// Package v2 facade. Manager is the wiring point for URSM v2 in the
// executor: it composes the v2 store, recovery gate, and rollout
// controller, and exposes the minimal surface the executor needs to
// make a sidecar write when a request succeeds or fails.
package v2

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	stdsync "sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/cache"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/recovery"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/resource"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/rollout"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/store"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/sync"
	"github.com/kaixuan/llm-gateway-go/metrics"
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
	scope    Scope
	fp       resource.FP
	conc     resource.Concurrency
	rpm      resource.RPM
	log      Logger
	// hotMu guards hotSrc — the live settings_kv source wired by
	// SetHotConfig (会话优化 v4 T5 / P1-6). nil until the integrator wires
	// one; every hot-configurable read goes through effectiveConfig(), so
	// boot values stay in effect until then.
	hotMu  stdsync.RWMutex
	hotSrc HotConfigSource
	// nodeMirror is the process-local LRU read accelerator for node views
	// (M2, spec Decision 2). nil when LRUMirrorSize==0 (mirror disabled).
	// Always a read-only replica of Redis; never written before a successful
	// Redis Lua write.
	nodeMirror       *cache.NodeMirror
	invalidationStop context.CancelFunc
	invalidationWG   stdsync.WaitGroup
	closeOnce        stdsync.Once
	// invalidationMu (MEDIUM, 2026-08-29) guards invalidationStop +
	// invalidationWG coordination between startInvalidationSubscriber
	// and Close. Without it, a concurrent Close + restart could
	// overwrite an already-cleared stop func with a new one or
	// double-Add on the WG.
	invalidationMu stdsync.Mutex
}

// SetHotConfig wires the live settings_kv source (hotconfig.Config
// satisfies HotConfigSource). Values are read per use — following the
// requestjourney/config.go live-source pattern — so a settings_kv change
// takes effect on the next call without any reload loop inside this
// package. Passing nil detaches the source and reverts to boot config.
func (m *Manager) SetHotConfig(src HotConfigSource) {
	if m == nil {
		return
	}
	m.hotMu.Lock()
	m.hotSrc = src
	m.hotMu.Unlock()
}

// effectiveConfig returns the boot config with live hot-config overrides
// layered on top (see LoadHot). Called on the few paths that consume
// hot-configurable parameters: RecordRequest (cool/node-TTL/backoff cap),
// ApplyProbe* (node TTL), and the mirror gear decision.
func (m *Manager) effectiveConfig() Config {
	if m == nil {
		return DefaultConfig()
	}
	m.hotMu.RLock()
	src := m.hotSrc
	m.hotMu.RUnlock()
	if src == nil {
		return m.cfg
	}
	return LoadHot(m.cfg, src)
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
		scope:    newScope(cfg.StrictCanary, cfg.CanaryTenants, cfg.CanaryCredentials, cfg.CanaryModels),
		rollout: rollout.New(rollout.Config{
			Mode:              cfg.Mode,
			CanaryPercent:     cfg.CanaryPercent,
			CanaryTenants:     cfg.CanaryTenants,
			CanaryModels:      cfg.CanaryModels,
			ShadowSampleRate:  cfg.ShadowSampleRate,
			ShadowDoubleWrite: cfg.ShadowDoubleWrite,
		}),
		fp:   d.FP,
		conc: d.Conc,
		rpm:  d.RPM,
		log:  log,
	}
	// Key schema mode is boot-only and shared by the store and the
	// recovery gate so coverage validation and warmup counting agree with
	// the write path (doc 14 §3).
	m.store.SetKeySchemaMode(cfg.KeySchemaMode)
	m.recovery.SetKeySchemaMode(cfg.KeySchemaMode)
	// M2: enable the process LRU mirror when configured (default 100k / 30s).
	// LRUMirrorSize==0 disables it (every read hits Redis).
	if cfg.LRUMirrorSize > 0 {
		m.nodeMirror = cache.NewNodeMirrorWithPrefix(cfg.LRUMirrorSize, cfg.LRUMirrorSoftTTL, cfg.RedisKeyPrefix)
		if cfg.Mode != api.ModeOff {
			m.startInvalidationSubscriber(d.Redis)
		}
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

// StrictCanary reports whether this manager enforces an exact canary scope.
func (m *Manager) StrictCanary() bool {
	return m != nil && m.scope.Strict()
}

// AllowsIdentity is the shared candidate identity gate used by probe and
// routing callers. Non-strict modes intentionally preserve existing behavior.
func (m *Manager) AllowsIdentity(tenant string, credentialID int, rawModel string) bool {
	return m == nil || m.scope.Allows(tenant, credentialID, rawModel)
}

// ShouldUseV2 delegates to the rollout controller.
func (m *Manager) ShouldUseV2(tenant, model, requestID string) bool {
	if m == nil {
		return false
	}
	return m.rollout.ShouldUseV2(tenant, model, requestID)
}

func (m *Manager) ShadowDoubleWrite() bool {
	if m == nil {
		return false
	}
	return m.rollout.ShadowDoubleWrite()
}

func (m *Manager) ShadowSampleRate() float64 {
	if m == nil {
		return 0
	}
	return m.rollout.ShadowSampleRate()
}

func (m *Manager) ShouldSampleShadow(tenant, model, requestID string) bool {
	if m == nil {
		return false
	}
	return m.rollout.ShouldSampleShadow(tenant, model, requestID)
}

// Ready returns the v2 recovery gate state. False means the v2
// pipeline is not authoritative yet; callers should treat v2 as off.
func (m *Manager) Ready(ctx context.Context) bool {
	ready, _ := m.ReadyWithError(ctx)
	return ready
}

func (m *Manager) ReadyWithError(ctx context.Context) (bool, error) {
	if m == nil {
		return false, fmt.Errorf("ursm.v2: nil manager")
	}
	return m.recovery.ReadyWithError(ctx)
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

// WarmupFromCoverage validates every expected tenant-aware migration key before
// opening the authoritative gate. It is for first cutover, not incident recovery.
func (m *Manager) WarmupFromCoverage(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	return m.recovery.WarmupFromCoverage(ctx)
}

// ValidateCoverage reports whether every key in the cutover migration manifest
// exists with the minimum runtime fields required by the v2 read path.
func (m *Manager) ValidateCoverage(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	return m.recovery.ValidateCoverage(ctx)
}

// RestoreIfClosed reopens a closed gate after Redis recovery. Authoritative
// mode validates the full cutover manifest again; other modes retain the
// legacy existing-key recovery contract.
func (m *Manager) RestoreIfClosed(ctx context.Context) (int, error) {
	if m == nil {
		return 0, nil
	}
	if m.Ready(ctx) {
		return 0, nil
	}
	if m.Mode() == api.ModeAuthoritative {
		return m.recovery.WarmupFromCoverage(ctx)
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
	views, _, err := m.filterAndScore(ctx, seeds, m.Ready(ctx))
	return views, err
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
	views, _, err := m.filterAndScore(ctx, seeds, ready)
	return views, err

}

// FilterAndScoreReadyWithSource is the S-3 (routing_state_source)
// companion of FilterAndScoreReady. The returned source enum reports
// what actually served the read for this request:
//
//   - StateSourceNodeMirrorHit  — every seed resolved from the LRU
//     mirror (no Redis IO on this call).
//   - StateSourceNodeMirrorMiss — at least one seed had no mirror
//     entry, so a Redis pipeline read ran. The call is still
//     authoritative if it returned no error.
//   - StateSourceNodeMirrorStale — every seed resolved from the
//     mirror but at least one entry was soft-expired (counts as a
//     miss on the LRU side; the helper reports it separately so
//     operators can size soft-TTL from the dashboard).
//
// In addition to returning the source to the caller, this method
// records the inner NodeMirror source into the shared
// statesource counter so the per-process metric surface stays
// authoritative even when callers (router tests, audit dumps) only
// consume the outer (authoritative/canary/off) label. The outer
// label is the router's responsibility — see router.go.
//
// Off-mode short-circuits at the caller (FilterAndScoreReady) and
// never reaches this method. The ready==false path returns
// ("", error) — the caller must NOT record a source in that case
// because authoritative state was not consulted at all.
func (m *Manager) FilterAndScoreReadyWithSource(ctx context.Context, seeds []CandidateSeed, ready bool) ([]api.NodeView, statesource.RoutingStateSource, error) {
	if m == nil {
		return nil, "", fmt.Errorf("ursm.v2: nil manager")
	}
	if m.Mode() == api.ModeOff {
		return nil, "", nil
	}
	views, src, err := m.filterAndScore(ctx, seeds, ready)
	// Record the inner source for the live metric surface. We do this
	// only on the success path — when filterAndScore errored, the
	// outer router records StateSourceFallback and the inner label
	// would be a misleading double-count. Skip on a non-empty source
	// (the empty value means "we never reached a decision point").
	if err == nil && src != "" {
		statesource.RecordRoutingStateSource(src)
	}
	return views, src, err
}

func (m *Manager) filterAndScore(ctx context.Context, seeds []CandidateSeed, ready bool) ([]api.NodeView, statesource.RoutingStateSource, error) {
	// Readiness is the authoritative gate and must win before mirror access or
	// request-shape validation. This keeps all modes fail-closed on a stale gate.
	if !ready {
		return nil, "", fmt.Errorf("ursm.v2: not ready")
	}
	if m.Mode() == api.ModeAuthoritative {
		for _, seed := range seeds {
			if seed.TenantID == "" {
				return nil, "", fmt.Errorf("ursm.v2: tenant_id is required")
			}
		}
	}
	// M2 (2026-07-27, spec Decision 2): serve the hot path from the process
	// LRU mirror first. The mirror is a read-only replica, backfilled only
	// AFTER a Redis read; applyToLRU enforces the generation-monotonic
	// contract so a stale snapshot can never overwrite a newer LRU entry.
	//
	// 会话优化 v4 §14.3 mirror-first gear contract (2026-08-18, closes the
	// P0-1 residual): a full mirror hit is NO LONGER an unconditional
	// fail-open path that skips Redis entirely. Two explicit gears:
	//
	//   target gear (default, MirrorGraceEnabled=false, consistency-first):
	//     a full mirror hit is served only after a cheap Redis liveness
	//     verify (PING). If Redis is unreachable the call is protectively
	//     rejected — the data plane stops routing rather than serving
	//     mirror state of unknown freshness. Redis HA (sentinel/cluster) +
	//     the ready gate (MarkClosedDebounced/RestoreIfClosed) are the
	//     availability story for this gear.
	//
	//   optional grace gear (MirrorGraceEnabled=true via settings_kv key
	//     llmgw_ursm_mirror_grace_enabled, availability-over-consistency):
	//     a full mirror hit within the soft TTL (default 30s) may serve
	//     degraded read-only routing with zero Redis IO; soft-expired
	//     entries already fall through to the Redis read path below (and
	//     are rejected when that fails). Source is still reported as
	//     StateSourceNodeMirrorHit so dashboards can size the gear.
	//
	// The afb13c9ea ready-gate fix is unchanged: a false readiness snapshot
	// ALWAYS rejects, mirror contents notwithstanding — no gear may bypass
	// the recovery gate.
	views := make([]api.NodeView, len(seeds))
	missIndices := make([]int, 0, len(seeds))
	// Per-seed source tally for the S-3 helper. We classify each seed
	// into one of three buckets:
	//   mirrorHit  — mirror had a fresh entry,
	//   mirrorStale — mirror had an entry but it was past soft-TTL,
	//   mirrorMiss  — mirror had no entry at all (or mirror disabled).
	// The aggregate is decided below.
	var mirrorHit, mirrorStale, mirrorMiss int
	if m.nodeMirror != nil {
		for i, s := range seeds {
			if mv, ok := m.nodeMirror.GetForTenant(s.TenantID, s.CredentialID, s.RawModel); ok {
				views[i] = mirrorToAPIView(mv, s)
				mirrorHit++
				continue
			}
			// GetForTenant already returns false for soft-expired entries,
			// so distinguish "never seen" from "seen but expired" by
			// checking the underlying entry. PeekForTenant does NOT
			// honour soft-TTL (it's an introspection helper), which is
			// exactly what we need here.
			if _, present := m.nodeMirror.PeekForTenant(s.TenantID, s.CredentialID, s.RawModel); present {
				missIndices = append(missIndices, i)
				mirrorStale++
				continue
			}
			missIndices = append(missIndices, i)
			mirrorMiss++
		}
	} else {
		for range seeds {
			mirrorMiss++
		}
		for i := range seeds {
			missIndices = append(missIndices, i)
		}
	}

	if len(missIndices) == 0 {
		if !m.effectiveConfig().MirrorGraceEnabled {
			// Target gear: verify Redis liveness before serving a
			// mirror-only answer. One PING bounds the check; failure is a
			// protective rejection (wraps store.ErrRedisUnavailable so
			// callers can errors.Is it exactly like the pipeline path).
			if err := m.verifyRedisForMirrorServe(ctx); err != nil {
				return nil, "", fmt.Errorf("ursm.v2: mirror serve rejected, redis unavailable: %w: %v", store.ErrRedisUnavailable, err)
			}
		}
		// Grace gear skips the verify: entries served here are within the
		// mirror soft TTL by construction (GetForTenant honours it), so the
		// degrade window is bounded by LRUMirrorSoftTTL (default 30s).
		scoreAndSort(views, seeds, m.cfg.ScoringWeights)
		// Every seed resolved from the mirror (no soft-expired entries).
		return views, statesource.StateSourceNodeMirrorHit, nil
	}

	// At least one seed missed the mirror: the Redis pipeline below is the
	// authoritative read, and its failure is already a protective rejection.
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
		// Wrap store.ErrRedisUnavailable explicitly (会话优化 v4 §14.3):
		// the FilterAndScore doc contract promises errors.Is(err,
		// store.ErrRedisUnavailable) on Redis failures — previously only the
		// nil-client path satisfied it, network errors surfaced bare.
		return nil, "", fmt.Errorf("ursm.v2: pipeline: %w: %v", store.ErrRedisUnavailable, err)
	}
	for j, idx := range missIndices {
		views[idx] = fetched[j]
		backfillSeedIdentity(&views[idx], seeds[idx])
		// Backfill the mirror from the authoritative Redis read. applyToLRU
		// is generation-safe (rejects stale writes), so this never lets an
		// older snapshot overwrite a newer LRU entry.
		if m.nodeMirror != nil {
			m.nodeMirror.ApplyFromAPI(views[idx])
		}
	}

	scoreAndSort(views, seeds, m.cfg.ScoringWeights)
	// Decide the aggregate source for this request:
	//   - hit       : every seed served from mirror, no miss/stale
	//   - stale     : at least one seed was soft-expired (no "never seen")
	//   - miss      : at least one seed was never in the mirror
	switch {
	case mirrorHit == 0 && mirrorStale == 0:
		return views, statesource.StateSourceNodeMirrorMiss, nil
	case mirrorMiss == 0:
		// hit > 0 and stale > 0 and miss == 0 → still "stale" because
		// some seeds forced a Redis read; surface the more informative
		// label.
		return views, statesource.StateSourceNodeMirrorStale, nil
	default:
		return views, statesource.StateSourceNodeMirrorMiss, nil
	}
}

// mirrorServeVerifyTimeout bounds the Redis liveness PING on the
// full-mirror-hit path (target gear). Kept short: it runs on the request
// hot path, and its only purpose is distinguishing "Redis reachable" from
// "Redis down" — a slow-but-alive Redis still answers PING in well under
// this budget.
const mirrorServeVerifyTimeout = 100 * time.Millisecond

// verifyRedisForMirrorServe implements the §14.3 target-gear check: before
// a mirror-only answer may be served, Redis must be demonstrably
// reachable. The error wraps store.ErrRedisUnavailable so callers can
// errors.Is it identically to the pipeline-read failure path.
func (m *Manager) verifyRedisForMirrorServe(ctx context.Context) error {
	if m == nil || m.store == nil || m.store.RawClient() == nil {
		return store.ErrRedisUnavailable
	}
	pingCtx, cancel := context.WithTimeout(ctx, mirrorServeVerifyTimeout)
	defer cancel()
	if err := m.store.RawClient().Ping(pingCtx).Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: mirror liveness ping: %v", store.ErrRedisUnavailable, err)
	}
	return nil
}

func isRedisTransportUnavailable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, store.ErrRedisUnavailable) {
		return true
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}
	return false
}

// FilterAndScoreOutageFallback serves a degraded read-only routing decision
// from the process-local NodeMirror when Redis is unreachable (availability
// gear, 2026-09-04). It is the router's last resort in ModeAuthoritative:
// the normal paths either rejected via ErrRedisUnavailable (pipeline read /
// mirror-serve PING) or observed Ready==false because the gate read itself
// errored against a dead Redis. Without this gear every request 503s for the
// duration of a Redis outage, which is the exact availability hole this
// method closes.
//
// Contract:
//   - Redis must be demonstrably unreachable (PING). A reachable Redis
//     returns an error instead: a deliberate gate closure must never be
//     bypassed by this gear, and a recovery race should let the next request
//     take the normal path.
//   - Entries are served regardless of the mirror soft TTL but must be
//     within the boot-configured OutageGrace window
//     (URSM_V2_OUTAGE_GRACE_SECONDS, default 30m, 0 disables this gear).
//   - PARTIAL service: seeds without a usable mirror entry are dropped from
//     the result; the router filters the candidate list against the returned
//     views. A completely cold mirror errors out (no worse than today).
//   - Read-only: no state writes happen here; RecordRequest remains
//     best-effort against Redis and simply logs during the outage.
func (m *Manager) FilterAndScoreOutageFallback(ctx context.Context, seeds []CandidateSeed) ([]api.NodeView, error) {
	if m == nil || m.store == nil {
		return nil, store.ErrRedisUnavailable
	}
	grace := m.effectiveConfig().OutageGrace
	if grace <= 0 {
		return nil, fmt.Errorf("ursm.v2: outage fallback disabled (URSM_V2_OUTAGE_GRACE_SECONDS<=0): %w", store.ErrRedisUnavailable)
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("ursm.v2: outage fallback requires candidates")
	}
	for _, s := range seeds {
		if s.TenantID == "" {
			return nil, fmt.Errorf("ursm.v2: outage fallback requires tenant_id")
		}
	}
	if err := m.verifyRedisForMirrorServe(ctx); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !isRedisTransportUnavailable(err) {
			return nil, fmt.Errorf("ursm.v2: redis liveness check failed: %w", err)
		}
	} else {
		return nil, fmt.Errorf("ursm.v2: redis reachable, outage fallback not applicable")
	}
	if m.nodeMirror == nil {
		return nil, fmt.Errorf("ursm.v2: outage fallback unavailable without node mirror: %w", store.ErrRedisUnavailable)
	}
	views := make([]api.NodeView, 0, len(seeds))
	kept := make([]CandidateSeed, 0, len(seeds))
	for _, s := range seeds {
		mv, ok := m.nodeMirror.GetForTenantWithinOutageWindow(s.TenantID, s.CredentialID, s.RawModel, grace)
		if !ok {
			continue
		}
		views = append(views, mirrorToAPIView(mv, s))
		kept = append(kept, s)
	}
	if len(views) == 0 {
		return nil, fmt.Errorf("ursm.v2: outage fallback has no mirror entries within %s: %w", grace, store.ErrRedisUnavailable)
	}
	scoreAndSort(views, kept, m.cfg.ScoringWeights)
	return views, nil
}

func backfillSeedIdentity(v *api.NodeView, s CandidateSeed) {
	v.ProviderID = s.ProviderID
	v.CredentialID = s.CredentialID
	v.RawModel = s.RawModel
	v.CanonicalName = s.Canonical
	v.TenantID = s.TenantID
	v.PriceIn = s.PriceIn
	v.PriceOut = s.PriceOut
	v.BillingMode = s.BillingMode
	v.Trust = s.Trust
	v.BaseURLMs = s.BaseURLMs
}

// scoreAndSort applies the price/latency/stability scoring and orders views
// by ascending Score. Extracted so the LRU fast path and the Redis miss path
// share identical scoring.
func scoreAndSort(views []api.NodeView, seeds []CandidateSeed, weights ScoringWeights) {
	// Lower score wins. Price and latency are direct costs; stability is a
	// failure-rate penalty so a higher success rate ranks better. An empty SR5m
	// window is neutral (0.5), matching the Redis protocol contract, while
	// malformed/out-of-range telemetry is made safe before it can affect order.
	for i := range views {
		s := seeds[i]
		v := &views[i]
		price := s.PriceIn + s.PriceOut
		lat := v.LatEWMA
		if lat == 0 {
			lat = s.BaseURLMs
		}
		successRate := safeSR5m(v.SR5m, v.Samples5m)
		v.Score = weights.Price*price + weights.Latency*float64(lat) + weights.Stability*(1-successRate)*1000
	}
	sort.SliceStable(views, func(i, j int) bool { return views[i].Score < views[j].Score })
}

func safeSR5m(sr float64, samples int) float64 {
	if samples <= 0 || math.IsNaN(sr) || math.IsInf(sr, 0) {
		return 0.5
	}
	if sr < 0 {
		return 0
	}
	if sr > 1 {
		return 1
	}
	return sr
}

// mirrorToAPIView expands a cached NodeView (the score-relevant subset) into
// an api.NodeView for the scoring loop. Fields not held in the cache (SR1m,
// other Samples*, LatP50/P95) are left zero — they are not read by the scorer,
// only by observers, and a soft-expired entry would have missed the LRU
// anyway (so we never score off a stale entry).
//
// 会话优化 v4 T5 / P1-5 (UT-UR-12): HealthStatus now round-trips through
// the mirror (cache.NodeView.HealthStatus ← Redis "health" field via
// ApplyFromAPI). It is carried for DISPLAY ONLY — nothing in
// scoreAndSort or the availability filter consults it; routing eligibility
// stays decided by Available/CoolUntil per the §14.3/FR-4 boundary.
func mirrorToAPIView(mv cache.NodeView, s CandidateSeed) api.NodeView {
	return api.NodeView{
		ProviderID:    s.ProviderID,
		CredentialID:  mv.CredentialID,
		RawModel:      mv.RawModel,
		CanonicalName: s.Canonical,
		TenantID:      s.TenantID,
		Available:     mv.Available,
		Reason:        mv.Reason,
		HealthStatus:  mv.HealthStatus,
		FailStreak:    mv.FailStreak,
		CoolUntil:     mv.CoolUntil,
		LatEWMA:       mv.LatEWMA,
		SR5m:          mv.SR5m,
		Samples5m:     mv.Samples5m,
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

// PlanReadyWithSource is the S-3 (routing_state_source) companion of
// PlanReady. It returns the inner NodeMirror source so the router can
// record BOTH the outer routing decision (canary / authoritative /
// off / fallback) AND the inner read source (hit / miss / stale /
// fallback) on the canary path. The authoritative path also
// benefits: today the router only sees the outer authoritative label
// while the inner label is silently auto-recorded by the manager.
//
// The inner source is auto-recorded by FilterAndScoreReadyWithSource
// (same as the authoritative path), so the router may opt to record
// only the outer label; the inner counter is already updated by the
// time this method returns. The returned source is propagated for
// callers that want to log it / use it in structured records.
//
// Returns ("", nil) when v2 is off (the manager short-circuits before
// the read path runs); the router must not record a source in that
// case.
func (m *Manager) PlanReadyWithSource(ctx context.Context, seeds []CandidateSeed, tenant, canonical string, ready bool) ([]CandidateSeed, statesource.RoutingStateSource, error) {
	return m.planReadyWithSource(ctx, seeds, tenant, canonical, ready, true)
}

// PlanReadyObserved runs an observe-only v2 plan directly against Redis. It
// neither records production routing-source metrics nor reads/backfills the
// process NodeMirror, so shadow traffic cannot change production cache state or
// LRU recency.
func (m *Manager) PlanReadyObserved(ctx context.Context, seeds []CandidateSeed, tenant, canonical string, ready bool) ([]CandidateSeed, error) {
	if m == nil || m.store == nil {
		return nil, fmt.Errorf("ursm.v2: nil manager/store")
	}
	if m.Mode() == api.ModeOff {
		return nil, nil
	}
	if !ready {
		return nil, fmt.Errorf("ursm.v2: not ready")
	}

	queries := make([]store.NodeQuery, 0, len(seeds))
	for _, seed := range seeds {
		queries = append(queries, store.NodeQuery{
			TenantID: seed.TenantID, CredentialID: seed.CredentialID, RawModel: seed.RawModel,
		})
	}
	views, err := m.store.PipelineNodeViews(ctx, m.cfg.RedisKeyPrefix, queries)
	if err != nil {
		return nil, fmt.Errorf("ursm.v2: observed pipeline: %w", err)
	}
	for i := range views {
		backfillSeedIdentity(&views[i], seeds[i])
	}
	scoreAndSort(views, seeds, m.cfg.ScoringWeights)

	idx := make(map[string]int, len(seeds))
	for i, seed := range seeds {
		idx[seedKey(seed.ProviderID, seed.CredentialID, seed.RawModel)] = i
	}
	out := make([]CandidateSeed, 0, len(views))
	for _, view := range views {
		if !view.Available {
			continue
		}
		if i, ok := idx[seedKey(view.ProviderID, view.CredentialID, view.RawModel)]; ok {
			out = append(out, seeds[i])
		}
	}
	return out, nil
}

func (m *Manager) planReadyWithSource(ctx context.Context, seeds []CandidateSeed, tenant, canonical string, ready, recordSource bool) ([]CandidateSeed, statesource.RoutingStateSource, error) {
	if m == nil {
		return nil, "", nil
	}
	if m.Mode() == api.ModeOff {
		return nil, "", nil
	}
	views, src, err := m.filterAndScore(ctx, seeds, ready)
	if err != nil {
		// Mirror the PlanReady warn-and-fallback contract, but on
		// the S-3 path we also record the inner source as
		// StateSourceFallback so the routing_state_source metric
		// surface stays closed on the canary path. The router
		// records the outer Canary label separately; the inner
		// counter is the manager's responsibility.
		m.log.Warn("ursm.v2: Plan filter failed, falling back", "error", err, "seed_count", len(seeds))
		if recordSource {
			statesource.RecordRoutingStateSource(statesource.StateSourceFallback)
		}
		return nil, statesource.StateSourceFallback, err
	}
	if len(views) == 0 {
		return nil, src, nil
	}
	// Build a lookup keyed by (CredentialID, RawModel) so we can map back to
	// the input seeds while preserving FilterAndScore's ordering (and its
	// availability filter — FilterAndScore returns one view per input seed
	// with Available=false on missing Redis data).
	idx := make(map[string]int, len(seeds))
	for i, s := range seeds {
		idx[seedKey(s.ProviderID, s.CredentialID, s.RawModel)] = i
	}
	out := make([]CandidateSeed, 0, len(views))
	for _, v := range views {
		if !v.Available {
			continue
		}
		i, ok := idx[seedKey(v.ProviderID, v.CredentialID, v.RawModel)]
		if !ok {
			continue
		}
		out = append(out, seeds[i])
	}
	// Record the inner source for the live metric surface (same
	// contract as FilterAndScoreReadyWithSource on the authoritative
	// path). On the empty-source path (off-mode short-circuit), we
	// skip — the router must not see a misleading inner label.
	if recordSource && src != "" {
		statesource.RecordRoutingStateSource(src)
	}
	if len(out) == 0 {
		return nil, src, nil
	}
	return out, src, nil
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
		idx[seedKey(s.ProviderID, s.CredentialID, s.RawModel)] = i
	}
	out := make([]CandidateSeed, 0, len(views))
	for _, v := range views {
		if !v.Available {
			continue
		}
		i, ok := idx[seedKey(v.ProviderID, v.CredentialID, v.RawModel)]
		if !ok {
			continue
		}
		out = append(out, seeds[i])
	}
	return out
}

// seedKey joins the full provider/credential/model identity into a single
// string for use as a lookup map key. Provider is required because the same
// credential/model pair can legitimately be exposed by different providers.
func seedKey(providerID, credentialID int, rawModel string) string {
	return strconv.Itoa(providerID) + "|" + strconv.Itoa(credentialID) + "|" + rawModel
}

func (m *Manager) invalidateNode(tenant string, credentialID int, rawModel string) {
	if m == nil || m.store == nil {
		return
	}
	if m.nodeMirror != nil {
		m.nodeMirror.InvalidateForTenant(tenant, credentialID, rawModel)
	}
	payload := store.NodeInvalidationPayload{TenantID: tenant, CredentialID: credentialID, RawModel: rawModel}.String()
	publishCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.store.RawClient().Publish(publishCtx, store.NodeInvalidationChannel(m.cfg.RedisKeyPrefix), payload).Err(); err != nil {
		m.log.Warn("ursm.v2: publish node invalidation failed", "error", err, "cid", credentialID)
	}
}

func (m *Manager) startInvalidationSubscriber(rdb *redis.Client) {
	if m == nil || m.nodeMirror == nil || rdb == nil {
		return
	}
	// MEDIUM (2026-08-29): guard against double-start. The previous
	// implementation overwrote m.invalidationStop on every invocation,
	// which would orphan the first subscriber's cancel func and (with
	// Add+Done balance errors) eventually panic the WG. We now bail
	// when a subscriber is already live.
	m.invalidationMu.Lock()
	if m.invalidationStop != nil {
		m.invalidationMu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.invalidationStop = cancel
	m.invalidationWG.Add(1)
	m.invalidationMu.Unlock()
	go func() {
		defer m.invalidationWG.Done()
		// Reconnect with exponential backoff when the subscriber drops for a
		// non-shutdown reason (Redis restart/failover, connection reset). A
		// permanent exit here would silently break the LRU mirror invalidation
		// contract until the next process restart, letting the mirror serve
		// stale routing state. The loop still exits immediately on ctx cancel.
		backoff := 100 * time.Millisecond
		for ctx.Err() == nil {
			pubsub := rdb.Subscribe(context.Background(), store.NodeInvalidationChannel(m.cfg.RedisKeyPrefix))
			closed := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					_ = pubsub.Close()
				case <-closed:
				}
			}()
			// Receive until a connection error or ctx cancellation.
			dropErr := error(nil)
			for {
				msg, err := pubsub.ReceiveMessage(ctx)
				if err != nil {
					dropErr = err
					break
				}
				parsed, ok := store.ParseNodeInvalidation(msg.Payload)
				if !ok {
					continue
				}
				m.nodeMirror.InvalidateForTenant(parsed.TenantID, parsed.CredentialID, parsed.RawModel)
			}
			_ = pubsub.Close()
			close(closed)
			if ctx.Err() != nil {
				return
			}
			m.log.Warn("ursm.v2: node invalidation subscriber reconnecting", "error", dropErr)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < time.Second {
				backoff *= 2
			}
		}
	}()
}

// Close releases the local invalidation subscriber. It is safe to call more
// than once and does not alter Redis state. When the LRU mirror is disabled
// (LRUMirrorSize==0) the subscriber is never started and Close is a no-op.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.invalidationMu.Lock()
	hasSubscriber := m.invalidationStop != nil
	m.invalidationMu.Unlock()
	if !hasSubscriber {
		return
	}
	m.closeOnce.Do(func() {
		// Pull the stop func under the lock so a concurrent
		// startInvalidationSubscriber cannot race against Close and
		// orphan the cancel. After Wait returns we clear the field so
		// the next caller sees the post-close state immediately.
		m.invalidationMu.Lock()
		stop := m.invalidationStop
		m.invalidationStop = nil
		m.invalidationMu.Unlock()
		stop()
		m.invalidationWG.Wait()
	})
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
//     path (ModeOff always; ModeShadow unless double-write is enabled;
//     ModeCanary unless the canary hash / tenant / model matches),
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
	if ev.TenantID == "" && (m.Mode() == api.ModeAuthoritative || m.StrictCanary()) {
		return fmt.Errorf("ursm.v2: tenant_id is required")
	}
	if !m.scope.Allows(ev.TenantID, ev.CredentialID, ev.RawModel) {
		return fmt.Errorf("%w: tenant=%q credential_id=%d model=%q", ErrOutOfScope, ev.TenantID, ev.CredentialID, ev.RawModel)
	}
	// P0-3 (audit §7.1 R-7.1): shadow double-write is opt-in via
	// ShadowDoubleWrite. Default false → ShouldUseV2 returns false →
	// "skipped" metric. When ShadowDoubleWrite is true AND ModeShadow,
	// ShouldUseV2 returns true → "recorded" metric. Recording the
	// skip/record/failed outcome lets operators run the 7-day drift
	// comparison (legacy credentialstate vs URSM v2 totals).
	if !m.rollout.ShouldUseV2(ev.TenantID, ev.RawModel, ev.RequestID) {
		metrics.Global().RecordURSMv2ShadowResult("skipped")
		return nil
	}
	timeout := time.Duration(m.cfg.RecordTimeoutMs) * time.Millisecond
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	// 会话优化 v4 T5 / P1-6: hot-configurable parameters are resolved per
	// call (settings_kv overrides over boot defaults, see LoadHot).
	live := m.effectiveConfig()
	// M3 (2026-07-28): record_request.lua now reads manual_hold directly
	// inside the script (single atomic Redis op), so we no longer pre-read
	// manual_hold here. This:
	//   - closes the prior TOCTOU window where ApplyAdmin could flip
	//     manual_hold between Go-side HGet and Lua Run;
	//   - saves one hot-path RTT (2 IO → 1 IO per record);
	//   - admin priority still dominates via the lua-internal manual_hold
	//     read.
	// Keys are tenant-aware. Authoritative requests with an empty tenant were
	// rejected above, so this path cannot silently write the legacy key.
	// The node/window keys form one logical key set and are built by the
	// store-layer authority (NodeKeySetForTenant), never assembled here.
	keys := store.NodeKeySetForTenant(m.cfg.RedisKeyPrefix, ev.TenantID, ev.CredentialID, ev.RawModel)
	dedupKey := ev.DedupKey
	if dedupKey == "" {
		dedupKey = ev.RequestID
	}
	if ev.Terminal && dedupKey != "" {
		dedupKey += ":terminal"
	}
	if _, err := m.store.RecordRequestKeySet(rctx, keys,
		store.RecordOutcome{
			Success:      ev.Success,
			ErrorKind:    ev.ErrorKind,
			NowMs:        time.Now().UnixMilli(),
			LatencyMs:    ev.LatencyMs,
			RequestID:    ev.RequestID,
			DedupKey:     dedupKey,
			NodeTTL:      live.NodeTTL,
			Window5mTTL:  m.cfg.Window5mTTL,
			Window30mTTL: m.cfg.Window30mTTL,
			AdminHold:    false, // deprecated; handled in lua
			// 会话优化 v4 T5 / P1-6: cool seconds / backoff cap are
			// hot-configurable (settings_kv llmgw_ursm_cool_seconds /
			// llmgw_ursm_backoff_cap_seconds) and read per call.
			CoolSeconds:       live.CoolSeconds,
			FailStreakLimit:   3,
			BackoffCapSeconds: live.BackoffCapSeconds,
			// 2026-08-10: free-tier transient tolerance — see
			// record_request.lua / reducer.go transientErrors.
			BillingMode: ev.BillingMode,
			// 会话优化 v4 T5 / P1-5: rich health enum from the executor
			// (nodehealth bridge); empty keeps the stored value.
			HealthStatus: ev.HealthStatus,
		}); err != nil {
		metrics.Global().RecordURSMv2ShadowResult("failed")
		m.log.Warn("ursm.v2: record failed", "error", err, "cid", ev.CredentialID)
		return fmt.Errorf("ursm.v2: record: %w", err)
	}
	m.invalidateNode(ev.TenantID, ev.CredentialID, ev.RawModel)
	metrics.Global().RecordURSMv2ShadowResult("recorded")
	return nil
}
