package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/credential"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/secret"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/singleflight"
)

// Suspicious-exit metrics. Registered once at package init so the
// default Prometheus registry surfaces them via the gateway's existing
// /metrics handler with no further wiring.
//
// outcome labels:
//
//	"dispatched"  — Redis state was suspicious and we fired an async
//	                 DB UPDATE to flip it to "recovering".
//	"noop"         — Redis was empty / state wasn't suspicious.
//	"db_error"     — dispatched, but the async DB UPDATE failed.
//	"cache_error"  — DB UPDATE succeeded, but the cache re-write failed.
//	"no_writer"    — Redis matched suspicious but the async hook was nil
//	                 (test-only path).
//
// suspiciousExitDBDurationSeconds is a histogram of the synchronous DB
// UPDATE inside the async goroutine. The bucket layout is tuned for the
// observed working envelope (50ms p99 target, 1s hard timeout):
//
//	5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms, 1s, 2.5s, 5s
//
// Operators should alert on p99 > 250ms for sustained periods — anything
// beyond that means the async DB UPDATE is in danger of hitting the 1s
// hard timeout and missing the suspicious→recovering transition.
var (
	suspiciousExitOnce       sync.Once
	suspiciousExits          *prometheus.CounterVec
	suspiciousExitDBDuration prometheus.Histogram
)

func registerSuspiciousExitMetrics() {
	suspiciousExitOnce.Do(func() {
		suspiciousExits = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "llmgw_suspicious_exits_total",
				Help: "Total routing-layer suspicious-exit dispatches and async-write outcomes.",
			},
			[]string{"outcome"},
		)
		suspiciousExitDBDuration = prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Name: "llmgw_suspicious_exit_db_duration_seconds",
				Help: "Wall-clock duration of the async model_probe_state UPDATE during suspicious-exit dispatch.",
				Buckets: []float64{
					0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
				},
			},
		)
		prometheus.MustRegister(suspiciousExits, suspiciousExitDBDuration)
	})
}

func init() { registerSuspiciousExitMetrics() }

func recordSuspiciousExit(outcome string) {
	if suspiciousExits == nil {
		return
	}
	suspiciousExits.WithLabelValues(outcome).Inc()
}

func recordSuspiciousExitDBDuration(seconds float64) {
	if suspiciousExitDBDuration == nil {
		return
	}
	suspiciousExitDBDuration.Observe(seconds)
}

type Candidate struct {
	CredentialID     int     `json:"credential_id"`
	ProviderID       int     `json:"provider_id"`
	BaseURL          string  `json:"base_url"`
	Protocol         string  `json:"protocol"`
	CatalogCode      string  `json:"catalog_code"`
	Tier             int     `json:"tier"`
	Weight           int     `json:"weight"`
	RawModel         string  `json:"model_name"`               // upstream name: COALESCE(outbound_model_name, raw_model_name)
	OfferRawModel    string  `json:"raw_model_name,omitempty"` // mo.raw_model_name for transform templates
	StandardizedName string  `json:"standardized_name"`
	SuccessRate      float64 `json:"success_rate"`
	P95LatencyMs     int     `json:"p95_latency_ms"`
	P50LatencyMs     int     `json:"p50_latency_ms"` // 2026-07-20: 用于 concurrency-aware latency scoring
	ConcurrencyLimit *int    `json:"concurrency_limit"`
	// FpSlotLimit is the fingerprint slot pool size — how many distinct
	// virtual user identities this credential can simulate. Conceptually
	// INDEPENDENT from ConcurrencyLimit (which controls in-flight request
	// count). 0 = unlimited fingerprint pool. Used by credentialfpslot
	// Manager.Acquire as the pool size when picking a stable identity.
	FpSlotLimit *int `json:"fp_slot_limit,omitempty"`
	// 2026-07-15: per-credential client-side RPM cap (migration 407).
	// nil/0 = unlimited (default for paid credentials). Free-pool
	// credentials auto-populate from the free-pool template rpmLimit.
	// Enforced by domains/credential/limiter.go in AcquireAll.
	RPMLimit *int `json:"rpm_limit,omitempty"`
	// 479: 并发/限流模式与队列参数（见 docs/会话优化v2/57）。
	// concurrency: 用 ConcurrencyLimit 做 in-flight 上限; rpm: 用 RPMLimit 做令牌桶;
	// tpm: 用 TPMLimit 做令牌桶(发送前预估 token); disabled: 不限流。
	// 由 domains/dispatch 的凭据队列调速器消费。
	ConcurrencyMode      string   `json:"concurrency_mode,omitempty"`
	TPMLimit             *int     `json:"tpm_limit,omitempty"`
	MaxQueueDepth        *int     `json:"max_queue_depth,omitempty"`
	MaxQueueWaitMS       *int     `json:"max_queue_wait_ms,omitempty"`
	BalanceUSD           *float64 `json:"balance_usd"`
	CircuitState         string   `json:"circuit_state"`
	AvailabilityState    string   `json:"availability_state"`
	QuotaState           string   `json:"quota_state"`
	LifecycleStatus      string   `json:"lifecycle_status"`
	Routable             bool     `json:"runtime_routable"`
	BlockReason          *string  `json:"runtime_block_reason"`
	PriceInPer1M         *float64 `json:"unit_price_in_per_1m"`
	PriceOutPer1M        *float64 `json:"unit_price_out_per_1m"`
	CacheReadPricePer1M  *float64 `json:"cache_read_price_per_1m"`
	CacheWritePricePer1M *float64 `json:"cache_write_price_per_1m"`
	SupportsPromptCache  bool     `json:"supports_prompt_cache"`
	CacheMode            string   `json:"cache_mode"`
	ManualPriority       int      `json:"manual_priority"`
	ActiveSessions       int      `json:"active_sessions"`
	ConsecutiveFailures  int      `json:"consecutive_failures"`
	CompositeScore       float64  `json:"composite_score"`
	Currency             string   `json:"currency"`
	BillingMode          string   `json:"billing_mode"`
	// ContextWindow is the upstream model's context window in tokens. Precedence
	// (migration 523): credential×model override (credential_model_bindings
	// .context_window_override) > canonical override (models_canonical
	// .context_window_override) > canonical base (models_canonical.context_window).
	// Used by the Q1/Q2/Q3 client-side context trim path
	// (transformation.CompressMessagesIfNeeded). nil means "unknown" — in which
	// case the trim path is a no-op.
	ContextWindow *int   `json:"context_window,omitempty"`
	APIKey        string `json:"-"`
	// APIKeys holds additional decrypted keys for multi-key rotation (beyond the
	// primary APIKey). nil/empty for single-key credentials. Index 0 in the
	// rotator corresponds to APIKey (primary); indices 1..N correspond here.
	APIKeys []string `json:"-"`
	// KeyRotator, when non-nil, enables per-credential multi-key round-robin
	// with health tracking. The executor calls ResolveKey before BuildRequest
	// and rewrites APIKey accordingly. nil for single-key credentials.
	KeyRotator *credential.KeyRotator `json:"-"`
	// QualityFixMode mirrors providers.quality_fix_mode (017_quality_fix_mode.sql).
	// Empty string is treated as "off" by every consumer; relay/stream.go
	// and routing/executor_chat.go both short-circuit when the value is
	// blank. Allowed values: "" (off), "off", "detect_only", "fix".
	QualityFixMode string `json:"quality_fix_mode,omitempty"`
	// RecentSuccessRate / RecentSamples are the live last-N success rate from
	// request_logs (see recent_success_rate() in db/migrations/035). nil when
	// there are no recent samples; in that case the router falls back to the
	// static SuccessRate. Used by router.loadScore as a soft quality signal so
	// a failing-but-not-yet-excluded credential is de-prioritized before it
	// hits the hard-exclude threshold. Added 2026-06-22 (defect ③ soft layer).
	RecentSuccessRate *float64 `json:"recent_success_rate,omitempty"`
	RecentSamples     int      `json:"recent_samples,omitempty"`
}

func (c *Candidate) CalcCost(promptTokens, completionTokens int, cacheReadTokens, cacheWriteTokens *int) float64 {
	pIn := float64(0)
	if c.PriceInPer1M != nil {
		pIn = *c.PriceInPer1M
	}
	pOut := float64(0)
	if c.PriceOutPer1M != nil {
		pOut = *c.PriceOutPer1M
	}
	if pIn == 0 && pOut == 0 {
		return 0
	}
	promptCost := float64(promptTokens) * pIn
	if c.CacheReadPricePer1M != nil && cacheReadTokens != nil && *cacheReadTokens > 0 {
		promptCost -= float64(*cacheReadTokens) * pIn
		promptCost += float64(*cacheReadTokens) * *c.CacheReadPricePer1M
	}
	if c.CacheWritePricePer1M != nil && cacheWriteTokens != nil && *cacheWriteTokens > 0 {
		promptCost -= float64(*cacheWriteTokens) * pIn
		promptCost += float64(*cacheWriteTokens) * *c.CacheWritePricePer1M
	}
	return (promptCost + float64(completionTokens)*pOut) / 1_000_000.0
}

func (c *Candidate) IsAvailable() bool {
	return c.UnavailableReason() == ""
}

// UnavailableReason returns a human-readable reason string when the
// candidate cannot be used, or "" if it is fully available.
//
// 2026-07-05 V25 fix: Changed from short-circuit evaluation to accumulating
// all failure reasons. This improves debuggability when multiple failure
// conditions are present (e.g., routing_blocked + quota_exhausted + circuit_open).
//
// Before V25: returned first reason only
// After V25: returns all reasons joined with "; "
func (c *Candidate) UnavailableReason() string {
	var reasons []string

	if !c.Routable {
		if c.BlockReason != nil && *c.BlockReason != "" {
			reasons = append(reasons, "routing_blocked:"+*c.BlockReason)
		} else {
			reasons = append(reasons, "routing_blocked")
		}
	}
	if c.LifecycleStatus != "" && c.LifecycleStatus != "active" {
		reasons = append(reasons, "lifecycle:"+c.LifecycleStatus)
	}
	switch c.AvailabilityState {
	case "suspended":
		reasons = append(reasons, "availability:suspended")
	case "auth_failed":
		reasons = append(reasons, "availability:auth_failed")
	case "cooling":
		reasons = append(reasons, "availability:cooling")
	case "rate_limited":
		reasons = append(reasons, "availability:rate_limited")
	case "unreachable":
		reasons = append(reasons, "availability:unreachable")
	}
	switch c.QuotaState {
	case "balance_exhausted":
		reasons = append(reasons, "quota:balance_exhausted")
	case "permanently_exhausted":
		reasons = append(reasons, "quota:permanently_exhausted")
	case "periodic_exhausted":
		reasons = append(reasons, "quota:periodic_exhausted")
	}
	if c.BalanceUSD != nil && *c.BalanceUSD <= 0 {
		reasons = append(reasons, "balance:zero")
	}

	if len(reasons) == 0 {
		return ""
	}
	// Join multiple reasons with "; " for better debuggability
	return strings.Join(reasons, "; ")
}

type Policy struct {
	AlgorithmVersion        int `json:"algorithm_version"`
	RetryPerCredential      int `json:"retry_per_credential"`
	TierFallbackMax         int `json:"tier_fallback_max"`
	CircuitOpenSeconds      int `json:"circuit_open_seconds"`
	CircuitFailureThreshold int `json:"circuit_failure_threshold"`
	CircuitMaxOpenSeconds   int `json:"circuit_max_open_seconds"`
	// StickyTTLSeconds is the sticky-session time-to-live in **seconds**.
	// The DB column is named `sticky_ttl_seconds` and the JSON tag matches
	// the on-the-wire name; the field name itself was previously
	// StickyTTLMilliseconds (causing the value to be interpreted as
	// milliseconds and the effective TTL to collapse to ~60s via the
	// minute-floor in executor.go).  Fix 2026-06-13: rename the field to
	// match the unit carried across the wire, then multiply by
	// time.Second at the use site.
	StickyTTLSeconds       int `json:"sticky_ttl_seconds"`
	TransientFailThreshold int `json:"transient_fail_threshold"`
}

func DefaultPolicy() *Policy {
	return &Policy{
		AlgorithmVersion:        2,
		RetryPerCredential:      1,
		TierFallbackMax:         3,
		CircuitOpenSeconds:      300,
		CircuitFailureThreshold: 5,
		CircuitMaxOpenSeconds:   1800,
		StickyTTLSeconds:        1800, // 30 minutes
		TransientFailThreshold:  2,
	}
}

type resolveResponse struct {
	ClientModel    string   `json:"client_model"`
	CanonicalName  string   `json:"canonical_name"`
	CanonicalID    *int     `json:"canonical_id"`
	ResolutionPath string   `json:"resolution_path"`
	RawModels      []string `json:"raw_models"`
	PlanOrder      []struct {
		CredentialID int    `json:"credential_id"`
		ProviderID   int    `json:"provider_id"`
		RawModel     string `json:"raw_model"`
		Tier         int    `json:"tier"`
	} `json:"plan_order"`
	Candidates []json.RawMessage `json:"candidates"`
}

type cacheEntry[T any] struct {
	value   T
	expires time.Time
}

// decryptFailureCacheTTL bounds how long we remember a decryption failure
// for a (credential_id) so repeated upstream requests don't keep re-trying
// the same broken secret. Short enough that an operator fixing the secret
// (rotation, keyring reload, ciphertext migration) sees traffic resume
// within one minute without manual intervention.
const decryptFailureCacheTTL = 1 * time.Minute

// decryptFailureCacheMax prevents the negative cache from growing unbounded
// across thousands of credentials. When the cap is reached we evict the
// oldest entries — failure-cache entries are short-lived (1 minute) so
// this only matters in pathological fleets with many broken secrets at once.
const decryptFailureCacheMax = 1024

const (
	candidateCacheTTL        = 30 * time.Second
	candidateCacheStaleGrace = 30 * time.Second
	candidateGenerationTries = 3
)

var errCandidateCacheInvalidated = errors.New("candidate cache invalidated during lookup")

type candidateGenerationInvalidatedError struct {
	queried uint64
	current uint64
}

func (e *candidateGenerationInvalidatedError) Error() string {
	return fmt.Sprintf("%v: queried generation %d, current generation %d", errCandidateCacheInvalidated, e.queried, e.current)
}

func (e *candidateGenerationInvalidatedError) Unwrap() error {
	return errCandidateCacheInvalidated
}

type candidateFlightResult struct {
	response   *resolveResponse
	generation uint64
}

func candidateFlightKey(key string, generation uint64) string {
	return fmt.Sprintf("cand:%s:g%d", key, generation)
}

func candidateGenerationChanged(queried, current uint64) bool {
	return queried != current
}

func candidateGenerationRetryError(tries int, cause error) error {
	return fmt.Errorf("candidate lookup invalidated after %d attempts: %w", tries, cause)
}

type Client struct {
	dbPool              *pgxpool.Pool
	redis               *redis.Client
	fernetKey           []byte
	keyring             *secret.Keyring
	asyncExitSuspicious func(credentialID int, rawModel string)

	// keyRotator holds per-credential multi-key rotation state (per-key health +
	// round-robin). nil when the DB/keyring isn't configured (single-key mode).
	// Shared across all candidate enrichments so health persists across requests.
	keyRotator *credential.KeyRotator

	mu             sync.RWMutex
	candCache      map[string]cacheEntry[*resolveResponse]
	candGeneration uint64
	polCache       cacheEntry[*Policy]
	keyCache       map[int]cacheEntry[string]
	// keyGeneration prevents a reveal that started before an operator rotates a
	// primary key from putting stale plaintext back into keyCache afterwards.
	keyGeneration map[int]uint64
	// keyCacheNeg memoises "this credential's API key failed to decrypt"
	// for decryptFailureCacheTTL seconds. Previously every request within
	// the 5-minute positive window re-fetched ciphertext from PG and
	// re-tried DecryptAny, generating a steady stream of
	// "enrichWithAPIKeys: reveal failed" warnings and DB scans. With the
	// negative cache we only retry once a minute — the same window as the
	// upstream call health-probe (bg/credential_recovery.go), which is
	// granular enough that an operator-rotated secret is picked up promptly.
	keyCacheNeg map[int]negativeCacheEntry

	sf singleflight.Group
}

var defaultClient *Client

func NewClient() *Client {
	c := &Client{
		candCache:     make(map[string]cacheEntry[*resolveResponse]),
		keyCache:      make(map[int]cacheEntry[string]),
		keyGeneration: make(map[int]uint64),
	}
	c.asyncExitSuspicious = c.defaultAsyncExitSuspicious
	defaultClient = c
	return c
}

// InvalidateAllCandidateCache clears all cached candidates.
// Call this after credential state changes (quota exhaustion, suspension, etc.)
// to ensure routing picks up the new state without waiting for cache expiry.
func InvalidateAllCandidateCache() {
	if defaultClient == nil {
		return
	}
	defaultClient.mu.Lock()
	defaultClient.candGeneration++
	defaultClient.candCache = make(map[string]cacheEntry[*resolveResponse])
	defaultClient.mu.Unlock()
	// 2026-07-03: 降级为 Debug —— 此函数在每次永久故障/状态变更时都会被调用，
	// Info 级别会在批量故障场景下刷屏日志。
	slog.Debug("candidate cache invalidated (all)")
}

// InvalidateCandidateCacheForCredential (OPT-5, 2026-07-12) clears only
// the cache entries that include the given credential. This avoids
// invalidating the entire cache when a single credential's state
// changes (e.g. quota exhausted, auth revoked) — the previous
// InvalidateAllCandidateCache caused a thundering-herd against the DB
// for every concurrent request on every other credential.
//
// Cost: O(N) over cache entries × O(K) over PlanOrder per entry. Both N
// and K are bounded (cache holds at most a few hundred entries; PlanOrder
// is the candidate count for one model) so the per-call cost is
// negligible compared to the avoided DB roundtrips.
func InvalidateCandidateCacheForCredential(credentialID int) {
	if defaultClient == nil || credentialID == 0 {
		return
	}
	defaultClient.mu.Lock()
	defer defaultClient.mu.Unlock()
	defaultClient.candGeneration++
	for key, entry := range defaultClient.candCache {
		if entry.value == nil {
			delete(defaultClient.candCache, key)
			continue
		}
		for _, p := range entry.value.PlanOrder {
			if p.CredentialID == credentialID {
				delete(defaultClient.candCache, key)
				break
			}
		}
	}
	slog.Debug("candidate cache invalidated for credential",
		"credential_id", credentialID,
	)
}

// ResetKeyRotatorForCredential clears the shared in-memory multi-key rotation
// state for one credential. Admin key-set changes must call this in addition to
// candidate-cache invalidation; the candidate cache does not own the rotator.
func ResetKeyRotatorForCredential(credentialID int) {
	if defaultClient == nil || credentialID == 0 || defaultClient.keyRotator == nil {
		return
	}
	defaultClient.keyRotator.ResetCredential(credentialID)
	slog.Debug("key rotator reset for credential", "credential_id", credentialID)
}

// InvalidateCredentialKeyCache evicts both primary-key caches for one
// credential. The generation fence makes an already in-flight reveal unable
// to reinsert the pre-rotation plaintext after this function returns.
func InvalidateCredentialKeyCache(credentialID int) {
	if defaultClient == nil || credentialID == 0 {
		return
	}
	defaultClient.mu.Lock()
	if defaultClient.keyGeneration == nil {
		defaultClient.keyGeneration = make(map[int]uint64)
	}
	defaultClient.keyGeneration[credentialID]++
	delete(defaultClient.keyCache, credentialID)
	delete(defaultClient.keyCacheNeg, credentialID)
	defaultClient.mu.Unlock()
	slog.Debug("primary key cache invalidated for credential", "credential_id", credentialID)
}

func (c *Client) Enabled() bool {
	return c.dbPool != nil
}

func (c *Client) SetDB(pool *pgxpool.Pool, secretKey, credentialEncryptionKey string) {
	c.dbPool = pool
	if key, err := secret.FernetKeyFromSecret(secretKey, credentialEncryptionKey); err == nil {
		c.fernetKey = key
	} else if pool != nil {
		slog.Warn("credential fernet key unavailable; reveal will use RPC fallback", "error", err)
	}
	if kr, kerr := secret.KeyringFromEnv(secretKey, credentialEncryptionKey); kerr == nil {
		c.keyring = kr
	} else if pool != nil {
		slog.Warn("credential keyring unavailable; AES-GCM v1 envelopes will fail to decrypt", "error", kerr)
	}
	if pool != nil {
		c.keyRotator = credential.NewKeyRotator()
	}
}

func (c *Client) SetAvailabilityRedis(redisClient *redis.Client) {
	c.redis = redisClient
}

// GetCandidates returns all routable candidates for the given model.
// tenantID: the tenant ID from the request context. Empty string falls back
// to "default" tenant (see loadCandidatesDB) for backward compatibility.
func (c *Client) GetCandidates(ctx context.Context, model, profile, tenantID string) ([]Candidate, *Policy, error) {
	return c.getCandidates(ctx, model, profile, tenantID, "")
}

// GetCandidatesByModality returns routable candidates whose canonical model
// matches the requested modality.
func (c *Client) GetCandidatesByModality(ctx context.Context, model, profile, tenantID, modality string) ([]Candidate, *Policy, error) {
	return c.getCandidates(ctx, model, profile, tenantID, strings.TrimSpace(strings.ToLower(modality)))
}

func (c *Client) getCandidates(ctx context.Context, model, profile, tenantID, modality string) ([]Candidate, *Policy, error) {
	if !c.Enabled() {
		return nil, DefaultPolicy(), fmt.Errorf("provider client not configured")
	}
	routeModel := modelname.NormalizeRouteKey(model)

	// 2026-07-03: Bug #7 fix - include tenantID in cache key
	key := routeModel
	if profile != "" {
		key = routeModel + "|" + profile
	}
	if tenantID != "" {
		key = key + "|" + tenantID
	}
	if modality != "" {
		key = key + "|modality:" + modality
	}

	var invalidatedErr error
	for attempt := 0; attempt < candidateGenerationTries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, DefaultPolicy(), err
		}

		cacheState := "miss"
		c.mu.RLock()
		queryGeneration := c.candGeneration
		entry, cacheOK := c.candCache[key]
		if cacheOK && time.Now().Before(entry.expires) {
			cacheState = "hit"
			c.mu.RUnlock()
			policy, _ := c.getPolicyCached(ctx)
			cands := c.enrichWithAPIKeys(ctx, entry.value)
			if !c.candidateGenerationIsCurrent(queryGeneration) {
				invalidatedErr = c.candidateGenerationInvalidatedError(queryGeneration)
				continue
			}
			if len(cands) == 0 {
				logCandidateDiagnostic("cache_empty",
					"model", routeModel,
					"profile", profile,
					"tenant_id", tenantID,
					"cache_key", key,
					"cache_plan_count", planCount(entry.value),
					"cache_candidate_count", candidateCount(entry.value),
				)
			}
			return cands, policy, nil
		}
		if cacheOK {
			cacheState = "expired"
		}
		c.mu.RUnlock()
		slog.Debug("[candidate_diag] candidate cache lookup",
			"cache_state", cacheState,
			"model", routeModel,
			"profile", profile,
			"tenant_id", tenantID,
			"cache_key", key,
			"generation", queryGeneration,
		)

		resp, shared, err := c.fetchCandidateGeneration(key, queryGeneration, func() (*resolveResponse, error) {
			resp, fetchErr := c.fetchCandidatesDB(ctx, routeModel, profile, tenantID, modality)
			if fetchErr == nil && (planCount(resp) == 0 || candidateCount(resp) == 0) {
				logCandidateDiagnostic("db_empty",
					"model", routeModel,
					"profile", profile,
					"tenant_id", tenantID,
					"cache_key", key,
					"plan_count", planCount(resp),
					"candidate_count", candidateCount(resp),
				)
			}
			return resp, fetchErr
		})
		if errors.Is(err, errCandidateCacheInvalidated) {
			invalidatedErr = err
			continue
		}
		if err != nil {
			// Stale fallback is limited to retryable failures, live contexts, and
			// non-empty entries that are still inside the fresh TTL plus grace.
			c.mu.RLock()
			staleEntry, ok := c.candCache[key]
			c.mu.RUnlock()
			if !ok || !canServeStaleCandidateCache(ctx, err, staleEntry, time.Now()) {
				return nil, DefaultPolicy(), err
			}

			cacheAge := time.Since(staleEntry.expires)
			recordCandidateDiagnostic("db_unavailable")
			slog.Warn("[candidate_diag] database unavailable, serving stale cache",
				"model", routeModel,
				"profile", profile,
				"tenant_id", tenantID,
				"cache_key", key,
				"cache_age", cacheAge,
				"plan_count", planCount(staleEntry.value),
				"candidate_count", candidateCount(staleEntry.value),
				"db_error", err.Error(),
			)

			cands := c.enrichWithAPIKeys(ctx, staleEntry.value)
			if !c.candidateGenerationIsCurrent(queryGeneration) {
				invalidatedErr = c.candidateGenerationInvalidatedError(queryGeneration)
				continue
			}
			if ctx.Err() != nil || len(cands) == 0 {
				logCandidateDiagnostic("stale_cache_empty",
					"model", routeModel,
					"profile", profile,
					"tenant_id", tenantID,
					"cache_age", cacheAge,
				)
				return nil, DefaultPolicy(), err
			}

			policy, _ := c.getPolicyCached(ctx)
			return cands, policy, nil
		}

		policy, _ := c.getPolicyCached(ctx)
		cands := c.enrichWithAPIKeys(ctx, resp)
		if !c.candidateGenerationIsCurrent(queryGeneration) {
			invalidatedErr = c.candidateGenerationInvalidatedError(queryGeneration)
			continue
		}
		if len(cands) == 0 && candidateCount(resp) > 0 {
			logCandidateDiagnostic("enrich_empty",
				"model", routeModel,
				"profile", profile,
				"tenant_id", tenantID,
				"cache_key", key,
				"singleflight_shared", shared,
				"plan_count", planCount(resp),
				"candidate_count", candidateCount(resp),
				"enriched_count", len(cands),
			)
		}
		return cands, policy, nil
	}

	return nil, DefaultPolicy(), candidateGenerationRetryError(candidateGenerationTries, invalidatedErr)
}

func (c *Client) fetchCandidateGeneration(key string, queryGeneration uint64, fetch func() (*resolveResponse, error)) (*resolveResponse, bool, error) {
	v, err, shared := c.sf.Do(candidateFlightKey(key, queryGeneration), func() (any, error) {
		resp, fetchErr := fetch()
		now := time.Now()

		c.mu.Lock()
		defer c.mu.Unlock()
		if candidateGenerationChanged(queryGeneration, c.candGeneration) {
			return nil, &candidateGenerationInvalidatedError{queried: queryGeneration, current: c.candGeneration}
		}
		if fetchErr != nil {
			return nil, fetchErr
		}

		current, currentOK := c.candCache[key]
		if currentOK && candidateResponseNonEmpty(current.value) && !candidateResponseNonEmpty(resp) && staleCandidateCacheUsable(current, now) {
			logCandidateDiagnostic("db_empty_fallback",
				"cache_key", key,
				"cache_age", now.Sub(current.expires),
				"plan_count", planCount(resp),
				"candidate_count", candidateCount(resp),
			)
			return candidateFlightResult{response: current.value, generation: queryGeneration}, nil
		}

		c.candCache[key] = cacheEntry[*resolveResponse]{
			value:   resp,
			expires: now.Add(candidateCacheTTL),
		}
		return candidateFlightResult{response: resp, generation: queryGeneration}, nil
	})
	if err != nil {
		return nil, shared, err
	}

	result := v.(candidateFlightResult)
	if !c.candidateGenerationIsCurrent(result.generation) {
		return nil, shared, c.candidateGenerationInvalidatedError(result.generation)
	}
	return result.response, shared, nil
}

func (c *Client) candidateGenerationIsCurrent(queryGeneration uint64) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.candGeneration == queryGeneration
}

func (c *Client) candidateGenerationInvalidatedError(queryGeneration uint64) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return &candidateGenerationInvalidatedError{queried: queryGeneration, current: c.candGeneration}
}

func planCount(resp *resolveResponse) int {
	if resp == nil {
		return 0
	}
	return len(resp.PlanOrder)
}

func candidateCount(resp *resolveResponse) int {
	if resp == nil {
		return 0
	}
	return len(resp.Candidates)
}

func candidateResponseNonEmpty(resp *resolveResponse) bool {
	return planCount(resp) > 0 && candidateCount(resp) > 0
}

func staleCandidateCacheUsable(entry cacheEntry[*resolveResponse], now time.Time) bool {
	return candidateResponseNonEmpty(entry.value) &&
		!entry.expires.IsZero() && now.Before(entry.expires.Add(candidateCacheStaleGrace))
}

func canServeStaleCandidateCache(ctx context.Context, err error, entry cacheEntry[*resolveResponse], now time.Time) bool {
	return ctx != nil && ctx.Err() == nil && isRetryableDBError(err) && staleCandidateCacheUsable(entry, now)
}

func logCandidateDiagnostic(event string, args ...any) {
	recordCandidateDiagnostic(event)
	slog.Warn("[candidate_diag] "+event, args...)
}

func isRetryableDBError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return false
	}

	errStr := strings.ToLower(err.Error())
	return errors.Is(err, context.DeadlineExceeded) ||
		strings.Contains(errStr, "connection refused") ||
		strings.Contains(errStr, "timeout") ||
		strings.Contains(errStr, "connection reset") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "i/o timeout") ||
		strings.Contains(errStr, "connection closed") ||
		strings.Contains(errStr, "no such host")
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) GetPolicy(ctx context.Context) (*Policy, error) {
	if !c.Enabled() {
		return DefaultPolicy(), nil
	}
	return c.getPolicyCached(ctx)
}

// GetProbeCandidates returns valid provider/credential/model bindings for
// diagnostics when normal routing has no executable candidates. It includes
// transiently unavailable nodes but excludes manual, disabled, and quota-exhausted credentials.
//
// The lookup is restricted to the calling tenant: a request that comes in
// for `tenant=X` will never probe a credential owned by `tenant=default`,
// because that would leak probe traffic and noise into the default
// tenant's request_logs.
func (c *Client) GetProbeCandidates(ctx context.Context, model, profile, tenantID string) ([]Candidate, error) {
	if !c.Enabled() || c.dbPool == nil {
		return nil, fmt.Errorf("routing DB not configured")
	}
	canonical := strings.TrimSpace(modelname.CanonicalizeClientModel(model))
	if canonical == "" {
		return nil, nil
	}
	if tenantID == "" {
		tenantID = "default"
	}
	// 2026-07-17 audit fix (P1): profile was previously a dead parameter.
	// Normalise it the same way resolveModelDB/aliasRawNamesDB do so the
	// alias EXISTS path honours client_profiles (empty matches all, matching
	// the legacy single-profile behaviour).
	profile = strings.TrimSpace(strings.ToLower(profile))
	// 2026-07-17 audit fix (P0): the previous query referenced
	// `ma.canonical_name`, but model_aliases has no such column (only
	// raw_name + canonical_id). PostgreSQL rejected the whole query with
	// "column ma.canonical_name does not exist", so GetProbeCandidates
	// ALWAYS errored and the executor silently fell back to the already-
	// filtered params.Candidates — the no-candidate full-set probe never
	// fired in production.
	//
	// The canonical_name lives on models_canonical, reached via
	// model_aliases.canonical_id OR model_offers.canonical_id. We now match
	// the same 4-path matrix as loadCandidatesByModalityDB
	// (canonical_raw_name / standardized_name / models_canonical via the
	// offer's canonical_id / alias EXISTS) so the probe set lines up with
	// what the router can actually route.
	//
	// The alias JOIN is also downgraded from INNER to LEFT: the previous
	// INNER JOIN dropped every offer that had no model_aliases row, so a
	// model served only via canonical_raw_name (no alias registered) was
	// invisible to probes.
	rows, err := c.dbPool.Query(ctx, `
		SELECT DISTINCT ON (c.id, mo.raw_model_name)
		       c.id::int, p.id::int, p.base_url, p.protocol,
		       COALESCE(mo.outbound_model_name, mo.raw_model_name),
		       mo.raw_model_name, COALESCE(mo.billing_mode, 'per_token')
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		WHERE p.tenant_id = $2
		  AND p.enabled = TRUE
		  AND COALESCE(p.manual_disabled, FALSE) = FALSE
		  AND COALESCE(p.base_url, '') <> ''
		  AND COALESCE(p.protocol, '') <> ''
		  AND COALESCE(c.status, 'active') = 'active'
		  AND COALESCE(c.lifecycle_status, 'active') = 'active'
		  AND COALESCE(c.manual_disabled, FALSE) = FALSE
		  AND COALESCE(c.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted', 'periodic_exhausted')
		  AND COALESCE(mo.unavailable_reason, '') NOT LIKE 'manual%'
		  AND (
		    mo.canonical_raw_name = $1
		    OR mo.standardized_name = $1
		    OR mc.canonical_name = $1
		    OR EXISTS (
		        SELECT 1 FROM model_aliases ma
		        WHERE ma.raw_name = $1
		          AND COALESCE(ma.status, 'active') = 'active'
		          AND (
		              ma.client_profiles IS NULL
		              OR cardinality(ma.client_profiles) = 0
		              OR $3 = ANY(ma.client_profiles)
		              OR $3 = ''
		          )
		          AND (
		              (mo.canonical_id IS NOT NULL AND ma.canonical_id = mo.canonical_id)
		              OR (mo.canonical_id IS NULL AND ma.canonical_id IS NULL)
		          )
		    )
		  )
		ORDER BY c.id, mo.raw_model_name, p.id
	`, canonical, tenantID, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []Candidate
	seen := make(map[string]struct{})
	for rows.Next() {
		var candidate Candidate
		if err := rows.Scan(&candidate.CredentialID, &candidate.ProviderID, &candidate.BaseURL, &candidate.Protocol, &candidate.RawModel, &candidate.OfferRawModel, &candidate.BillingMode); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d|%s", candidate.CredentialID, candidate.OfferRawModel)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// ModelKnown reports whether the model name has any explicitly-registered
// provider offer or alias. It deliberately excludes models_canonical because
// that table is auto-populated on first sighting by resolveModelDB — using
// it would let the auto-insert mask typos in client requests as 503
// "no_candidate" instead of 400 "invalid_model". The check is one cheap
// SQL with three indexed equality predicates on provider_models.standardized_name,
// provider_models.raw_model_name, and model_aliases.raw_name.
func (c *Client) ModelKnown(ctx context.Context, model string) bool {
	if !c.Enabled() || c.dbPool == nil || strings.TrimSpace(model) == "" {
		return false
	}
	lookup := modelname.CanonicalizeClientModel(model)
	if lookup == "" {
		return false
	}
	var found bool
	err := c.dbPool.QueryRow(ctx, modelKnownSQL, lookup).Scan(&found)
	if err != nil {
		return false
	}
	return found
}

const modelKnownSQL = `
		SELECT EXISTS (
			SELECT 1 FROM provider_models
			 WHERE canonical_raw_name = $1
			UNION ALL
			SELECT 1 FROM provider_models
			 WHERE standardized_name = $1
			UNION ALL
			SELECT 1 FROM model_aliases
			 WHERE raw_name = $1
			LIMIT 1
		)
`

func (c *Client) getPolicyCached(ctx context.Context) (*Policy, error) {
	c.mu.RLock()
	if c.polCache.value != nil && time.Now().Before(c.polCache.expires) {
		p := c.polCache.value
		c.mu.RUnlock()
		return p, nil
	}
	c.mu.RUnlock()

	v, err, _ := c.sf.Do("policy", func() (any, error) {
		pol, fetchErr := c.fetchPolicyDB(ctx)
		if fetchErr != nil {
			return DefaultPolicy(), nil
		}
		c.mu.Lock()
		c.polCache = cacheEntry[*Policy]{
			value:   pol,
			expires: time.Now().Add(10 * time.Second),
		}
		c.mu.Unlock()
		return pol, nil
	})
	if err != nil {
		return DefaultPolicy(), nil
	}
	return v.(*Policy), nil
}

func (c *Client) fetchCandidatesDB(ctx context.Context, model, profile, tenantID, modality string) (*resolveResponse, error) {
	if c.dbPool == nil {
		return nil, fmt.Errorf("routing DB not configured")
	}
	res, err := c.resolveModelDB(ctx, model, profile)
	if err != nil {
		return nil, err
	}
	cands, err := c.loadCandidatesByModalityDB(ctx, res.ClientModel, tenantID, modality)
	if err != nil {
		return nil, err
	}
	planOrder := make([]struct {
		CredentialID int    `json:"credential_id"`
		ProviderID   int    `json:"provider_id"`
		RawModel     string `json:"raw_model"`
		Tier         int    `json:"tier"`
	}, 0, len(cands))
	rawCandidates := make([]json.RawMessage, 0, len(cands))
	for _, cand := range cands {
		planOrder = append(planOrder, struct {
			CredentialID int    `json:"credential_id"`
			ProviderID   int    `json:"provider_id"`
			RawModel     string `json:"raw_model"`
			Tier         int    `json:"tier"`
		}{CredentialID: cand.CredentialID, ProviderID: cand.ProviderID, RawModel: cand.RawModel, Tier: cand.Tier})
		b, _ := json.Marshal(cand)
		rawCandidates = append(rawCandidates, b)
	}
	res.PlanOrder = planOrder
	res.Candidates = rawCandidates
	return res, nil
}

func (c *Client) resolveModelDB(ctx context.Context, model, profile string) (*resolveResponse, error) {
	profile = strings.TrimSpace(strings.ToLower(profile))
	if profile == "" {
		profile = ""
	}
	// 2026-07-14: every SQL comparison below uses provider_models.canonical_raw_name
	// (or model_aliases.raw_name / models_canonical.canonical_name), all of which are
	// persisted as lowercase. We compare with the lowercase form of the client's
	// request — modelname.CanonicalizeClientModel — so the SQL queries are now plain
	// equality lookups instead of `lower(col) = lower($1)`.
	rawLookup := modelname.CanonicalizeClientModel(model)

	// 2026-06-19 audit: walk the cross-form variant matrix so a
	// request like "claude-sonnet-4.6" matches a DB canonical
	// "claude-sonnet-4-6" (and the inverse).  The first matching
	// variant wins; we record it in ResolutionPath so admins can
	// see why a resolve landed where it did.
	variants := modelname.NormalizeRouteKeyAliases(model)
	if len(variants) == 0 {
		return &resolveResponse{ClientModel: model, ResolutionPath: "direct", RawModels: []string{rawLookup}}, nil
	}

	var canonicalID *int
	var canonicalName string
	var hitVariant string
	var hitPath string

	// (1) canonical_name match across the variant matrix.
	for _, v := range variants {
		err := c.dbPool.QueryRow(ctx, `
			SELECT id, canonical_name
			FROM models_canonical
			WHERE canonical_name = $1
			  AND COALESCE(status, 'active') = 'active'
		`, modelname.CanonicalizeClientModel(v)).Scan(&canonicalID, &canonicalName)
		if err == nil && canonicalID != nil {
			hitVariant = v
			hitPath = "canonical"
			break
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}
	if canonicalID != nil {
		raw, err := c.aliasRawNamesDB(ctx, *canonicalID, profile)
		if err != nil {
			return nil, err
		}
		return &resolveResponse{
			ClientModel:    model,
			CanonicalName:  canonicalName,
			CanonicalID:    canonicalID,
			ResolutionPath: variantResolutionPath(hitPath, hitVariant, modelname.NormalizeRouteKey(model)),
			RawModels:      uniqueRawModels(append(raw, model)),
		}, nil
	}

	// (2) alias match across the variant matrix.
	for _, v := range variants {
		err := c.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE ma.raw_name = $1
			  AND COALESCE(ma.status, 'active') = 'active'
			  AND COALESCE(mc.status, 'active') = 'active'
			  AND (
			      ma.client_profiles IS NULL
			      OR cardinality(ma.client_profiles) = 0
			      OR $2 = ANY(ma.client_profiles)
			      OR $2 = ''
			  )
			LIMIT 1
		`, modelname.CanonicalizeClientModel(v), profile).Scan(&canonicalID, &canonicalName)
		if err == nil && canonicalID != nil {
			hitVariant = v
			hitPath = "alias"
			break
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}
	if canonicalID != nil {
		raw, err := c.aliasRawNamesDB(ctx, *canonicalID, profile)
		if err != nil {
			return nil, err
		}
		return &resolveResponse{
			ClientModel:    model,
			CanonicalName:  canonicalName,
			CanonicalID:    canonicalID,
			ResolutionPath: variantResolutionPath(hitPath, hitVariant, modelname.NormalizeRouteKey(model)),
			RawModels:      uniqueRawModels(append(raw, model)),
		}, nil
	}

	// (3) full original client_model as a final fallback (covers
	// case-sensitivity edge cases where the operator stored the
	// alias in mixed case).
	if rawLookup != "" && rawLookup != strings.ToLower(modelname.NormalizeRouteKey(model)) {
		err := c.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE ma.raw_name = $1
			  AND COALESCE(ma.status, 'active') = 'active'
			  AND COALESCE(mc.status, 'active') = 'active'
			  AND (
			      ma.client_profiles IS NULL
			      OR cardinality(ma.client_profiles) = 0
			      OR $2 = ANY(ma.client_profiles)
			      OR $2 = ''
			  )
			LIMIT 1
		`, rawLookup, profile).Scan(&canonicalID, &canonicalName)
		if err == nil && canonicalID != nil {
			raw, err := c.aliasRawNamesDB(ctx, *canonicalID, profile)
			if err != nil {
				return nil, err
			}
			return &resolveResponse{ClientModel: model, CanonicalName: canonicalName, CanonicalID: canonicalID, ResolutionPath: "raw_fallback", RawModels: uniqueRawModels(append(raw, model, rawLookup))}, nil
		}
		if err != nil && err != pgx.ErrNoRows {
			return nil, err
		}
	}
	stdName := modelname.NormalizeRouteKey(model)
	if stdName != "" {
		_, _ = c.dbPool.Exec(ctx, `
			INSERT INTO models_canonical (canonical_name, family, source, status)
			VALUES ($1, 'unknown', 'auto_discovered', 'active')
			ON CONFLICT (canonical_name) DO NOTHING
		`, stdName)
	}
	return &resolveResponse{ClientModel: model, CanonicalID: nil, CanonicalName: "", ResolutionPath: "direct", RawModels: []string{stdName}}, nil
}

// variantResolutionPath appends ":variant" to the base path when the
// resolving variant differs from the model's normalized form, so the
// routing layer can tell that a cross-form hit happened.
//
//	"claude-sonnet-4.6" → match in canonical_name as "claude-sonnet-4-6"
//	→ ResolutionPath = "canonical:variant"
//
//	"claude-sonnet-4-6" → match in canonical_name as itself
//	→ ResolutionPath = "canonical"
func variantResolutionPath(base, hitVariant, normalized string) string {
	if base == "" {
		return "direct"
	}
	if hitVariant == "" || strings.EqualFold(hitVariant, normalized) {
		return base
	}
	return base + ":variant"
}

func (c *Client) aliasRawNamesDB(ctx context.Context, canonicalID int, profile string) ([]string, error) {
	rows, err := c.dbPool.Query(ctx, `
		SELECT raw_name
		FROM model_aliases
		WHERE canonical_id = $1
		  AND COALESCE(status, 'active') = 'active'
		  AND (
		      client_profiles IS NULL
		      OR cardinality(client_profiles) = 0
		      OR $2 = ANY(client_profiles)
		      OR $2 = ''
		  )
	`, canonicalID, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// loadCandidatesDB returns all routable offers for the given client model.
//
// Matching is done in SQL (P3 of 2026-06-18-model-match-and-404-plan.md):
//
//	lower(mo.raw_model_name) = $1              -- case-insensitive exact
//	OR lower(mo.standardized_name) = $1        -- provider-prefix-stripped exact
//	OR EXISTS alias match (ma.canonical_id = mo.canonical_id)
//
// The standardized_name path was added on 2026-06-23 to fix a class of
// "no available provider" outages: some providers register their model
// offers with a provider-namespaced raw_model_name (e.g.
// "minimaxai/minimax-m3" on NVIDIA NIM builds) while the standardized_name
// column already holds the prefix-stripped form ("minimax-m3"). Without
// this clause, offers whose raw_model_name carries a provider prefix are
// unreachable whenever the alias table is empty — which happens when the
// taxonomy YAML is absent (the TaxonomySync background worker logs
// "aliases:0") or the alias_sync ON CONFLICT clause errors out. The alias
// table remains the canonical source of cross-form name mapping, but
// standardized_name is a reliable, always-populated fallback that closes
// the gap.
//
// This removes the previous Go-side modelname.MatchModelOffer fuzzy filter
// which had family-specific heuristics (dot↔dash rewrites, feature overlap)
// and could over-match across distinct models (e.g. routing a request for
// "minimax-m3" to a credential offering "minimax-m2.7" when the SQL-level
// alias map was stale).
//
// The alias table is populated by discovery/alias_sync.go and by the
// discovery upsert path (modelcatalog.UpsertCredentialModel). New aliases
// for cross-form names (e.g. "claude-opus-4-6" ↔ "claude-opus-4.6") must be
// inserted there, not by adding more family-specific normalization rules
// here.
func (c *Client) loadCandidatesDB(ctx context.Context, clientModel, tenantID string) ([]Candidate, error) {
	return c.loadCandidatesByModalityDB(ctx, clientModel, tenantID, "")
}

func (c *Client) loadCandidatesByModalityDB(ctx context.Context, clientModel, tenantID, modality string) ([]Candidate, error) {
	if c.dbPool == nil {
		return nil, nil
	}
	// 2026-07-14: provider_models.canonical_raw_name, model_aliases.raw_name,
	// and standardized_name are all persisted lowercase. The matching
	// columns below are equality-only lookups against this canonical key,
	// so we lowercase the client request once at the boundary instead of
	// wrapping each column in lower(col).
	clientModelLower := modelname.CanonicalizeClientModel(clientModel)

	// 2026-07-03: Bug #7 fix - support tenantID parameter
	// If tenantID is empty, use 'default' as fallback (backward compatibility)
	if tenantID == "" {
		tenantID = "default"
	}

	var rows pgx.Rows
	var err error
	const maxAttempts = 3

	for attempt := 0; attempt < maxAttempts; attempt++ {
		rows, err = c.dbPool.Query(ctx, `
		SELECT
			c.id::int AS credential_id,
			p.id::int AS provider_id,
			p.base_url,
			p.protocol,
			COALESCE(p.catalog_code, '') AS catalog_code,
			COALESCE(mo.routing_tier, 2)::int AS tier,
			COALESCE(mo.weight, 100)::int AS weight,
			COALESCE(mo.outbound_model_name, mo.raw_model_name) AS model_name,
			COALESCE(mo.standardized_name, '') AS standardized_name,
			COALESCE(mo.success_rate, 0.9)::float8 AS success_rate,
			COALESCE(mo.p95_latency_ms, 9999)::int AS p95_latency_ms,
			c.concurrency_limit,
			-- 2026-08-11 capacity-weighted LB: load concurrency_limit_auto so
			-- the candidate build can derive Weight from effective concurrency
			-- (auto-tuned limit, capped by the manual hard cap). See
			-- applyCapacityWeightedLB and domains/providerprofile/adapters.go
			-- GetModelScale for the canonical EffLimit formula.
			c.concurrency_limit_auto,
			COALESCE(c.fp_slot_limit, 20) AS fp_slot_limit,  -- 2026-06-24: 5→20
		-- 2026-07-15: per-credential RPM cap (migration 407). NULL/0 = unlimited.
		c.rpm_limit,
		-- 479: 并发/限流模式与队列参数（见 docs/会话优化v2/57）。
		COALESCE(c.concurrency_mode, 'concurrency') AS concurrency_mode,
		c.tpm_limit,
		c.max_queue_depth,
		c.max_queue_wait_ms,
			c.balance_usd::float8,
			COALESCE(c.circuit_state, 'closed') AS circuit_state,
			COALESCE(c.availability_state, 'ready') AS availability_state,
			COALESCE(c.quota_state, 'ok') AS quota_state,
			COALESCE(c.lifecycle_status, 'active') AS lifecycle_status,
			COALESCE(mo.unit_price_in_per_1m, 0)::float8 AS unit_price_in_per_1m,
			COALESCE(mo.unit_price_out_per_1m, 0)::float8 AS unit_price_out_per_1m,
			COALESCE(mo.cache_read_price_per_1m, 0)::float8 AS cache_read_price_per_1m,
			COALESCE(mo.cache_write_price_per_1m, 0)::float8 AS cache_write_price_per_1m,
			-- is_routable comes from the unified VIEW (manual > auto priority).
			-- Spec: 2026-06-12-credential-availability-audit-design §3.1
			COALESCE(v.is_routable, FALSE) AS runtime_routable,
			v.unavailable_reason,
			CASE WHEN cc.capability = 'prompt_caching' AND cc.supported IS TRUE THEN TRUE ELSE FALSE END AS supports_prompt_cache,
			COALESCE(cc.evidence_json->>'cache_mode', '') AS cache_mode,
			COALESCE(mo.manual_priority, 99)::int AS manual_priority,
			COALESCE(mo.active_sessions, 0)::int AS active_sessions,
			COALESCE(mo.consecutive_failures, 0)::int AS consecutive_failures,
			COALESCE(mo.currency, 'USD') AS currency,
			COALESCE(mo.billing_mode, 'per_token') AS billing_mode,
			mo.raw_model_name,
			-- 522: 优先级 凭据×模型级覆盖 > 标准模型级覆盖 > 标准目录默认值。
			COALESCE(mo.context_window_override, mc.context_window_override, mc.context_window) AS context_window,
			-- 2026-06-19 quality fix mode (017_quality_fix_mode.sql).
			-- Read from providers so the routing executor can pass the
			-- per-provider mode through to the relay stream reader and
			-- the non-stream body post-processor. Empty string means
			-- the column was just added and the row hasn't been backfilled
			-- (will be 'off' on next reconnect after the migration).
			COALESCE(NULLIF(p.quality_fix_mode, ''), 'off') AS quality_fix_mode,
			-- 2026-06-22 last-N success gate (035_routing_recent_success_rate.sql).
			-- Live success rate over the most recent 50 request_logs rows for
			-- this (credential, outbound_model). rsr.samples=0 when there are
			-- no recent rows (cold start) — caller keeps the pair. rsr.rate is
			-- used both for the hard-exclude filter below and (via the
			-- RecentSuccessRate field) for soft de-prioritization in the router.
			rsr.rate   AS recent_success_rate,
			rsr.samples AS recent_samples
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN v_routable_credential_models v
		       ON v.credential_id = mo.credential_id
		      AND (v.raw_model_name = mo.raw_model_name OR v.raw_model_name = mo.standardized_name)
		LEFT JOIN credential_capabilities cc ON cc.credential_id = c.id AND cc.capability = 'prompt_caching'
		LEFT JOIN model_aliases ma
		       ON ma.raw_name = mo.canonical_raw_name
		      AND COALESCE(ma.status, 'active') = 'active'
		LEFT JOIN models_canonical mc ON mc.id = COALESCE(mo.canonical_id, ma.canonical_id)
		-- LEFT JOIN model_name_mapping for standardized name lookup fallback
		LEFT JOIN model_name_mapping mnm
		       ON mnm.raw_model_name = mo.canonical_raw_name
		-- Last-N success rate over request_logs. LATERAL so each candidate
		-- row carries its own recent (rate, samples). STABLE function, hits
		-- idx_request_logs_credential_ts (credential_id, ts DESC) so the
		-- LIMIT 50 is a 50-row index descent per candidate — not a window
		-- aggregate over the whole partitioned table.
		CROSS JOIN LATERAL recent_success_rate(c.id, mo.raw_model_name, 50) AS rsr
			WHERE (p.tenant_id = $2 OR p.tenant_id = 'default')
			  AND (
			      $3 = ''
			      OR $3 = 'text'
			      OR ($3 IN ('vision', 'audio') AND COALESCE(mc.modality, 'text') IN ($3, 'multimodal'))
			      OR COALESCE(mc.modality, 'text') = $3
			  )
			  AND COALESCE(mc.status, 'active') != 'disabled'
		  AND COALESCE(c.status, 'active') NOT IN ('disabled')
		  -- v.is_routable is FALSE for any model with manual disable at any layer
		  -- (provider.manual_disabled, credentials.manual_disabled, or cmb.unavailable_reason='manual')
		  AND v.is_routable = TRUE
		  -- 2026-06-22 defect (2): drop (credential, model) pairs whose
		  -- model_probe_state is 'broken_confirmed'. The probe worker marks a
		  -- binding broken_confirmed after 3 consecutive targeted-probe
		  -- failures; without this filter the pair stays routable as long as
		  -- the credential-level availability_state is 'ready', so the router
		  -- keeps re-selecting it (the cred-11/minimax-m3 loop).
		  AND NOT EXISTS (
		      SELECT 1 FROM model_probe_state mps
		      WHERE mps.credential_id = c.id
		        AND mps.raw_model_name = mo.raw_model_name
		        AND mps.state = 'broken_confirmed'
		  )
		  -- 2026-06-22 defect (3) hard gate: exclude pairs whose real recent
		  -- success rate is below 0.5 once we have at least 20 samples. The
		  -- min-sample threshold avoids cold-start false positives (a brand-new
		  -- credential with 1 unlucky failure). Pairs in the 0.5-0.9 band are
		  -- kept but soft-de-prioritized via RecentSuccessRate in the router.
		  -- 2026-07-15: restored to 0.5. The 2026-06-23 temporary 0.3 was
		  -- lowered to absorb the 54% failure spike from a resource leak;
		  -- the leak is fixed and the rolling 50-request window has long
		  -- since rotated past it.
			  AND NOT (
			      -- Free/token-plan credentials intentionally stay routable after
			      -- transient failures; the executor and state manager soft-demote
			      -- them instead of hard-excluding the only route.
			      COALESCE(mo.billing_mode, 'per_token') <> 'free'
			      AND rsr.samples >= 20
			      AND COALESCE(rsr.rate, 1.0) < 0.5
			      -- A single-candidate model needs a recovery chance. Circuit,
			      -- model-probe and permanent-state guards still apply; the
			      -- rolling-rate gate is a failover preference only when a
			      -- sibling offer can actually take traffic.
			      AND EXISTS (
			          SELECT 1
			          FROM model_offers mo_sibling
			                  JOIN credentials c_sibling ON c_sibling.id = mo_sibling.credential_id
			                  JOIN providers p_sibling ON p_sibling.id = c_sibling.provider_id
			                  LEFT JOIN v_routable_credential_models v_sibling
			                         ON v_sibling.credential_id = mo_sibling.credential_id
			                        AND (v_sibling.raw_model_name = mo_sibling.raw_model_name
			                             OR v_sibling.raw_model_name = mo_sibling.standardized_name)
			          WHERE mo_sibling.credential_id <> mo.credential_id
			            AND mo_sibling.available = TRUE
			            AND COALESCE(v_sibling.is_routable, FALSE) = TRUE
			            AND COALESCE(c_sibling.status, 'active') = 'active'
			            AND COALESCE(c_sibling.lifecycle_status, 'active') = 'active'
			            AND COALESCE(c_sibling.manual_disabled, FALSE) = FALSE
			            /* 2026-08-08 audit note: this c_sibling.quota_state predicate
			               deliberately does NOT exclude periodic_exhausted, while
			               GetProbeCandidates (line ~578) DOES exclude it. Intentional:
			               this subquery asks "does ANY sibling binding exist that COULD
			               take traffic" (the sibling EXISTS gate for the lone-candidate
			               fail-open path), not "which sibling should we route to". A
			               periodic-exhausted sibling is still a potential failover
			               target because its window resets in minutes/hours;
			               routing-time selection is filtered separately above. */
			            AND COALESCE(c_sibling.quota_state, 'ok') NOT IN ('permanently_exhausted', 'balance_exhausted')
			            AND COALESCE(p_sibling.enabled, FALSE) = TRUE
			            AND COALESCE(p_sibling.manual_disabled, FALSE) = FALSE
			            AND (
			                mo_sibling.standardized_name = mo.standardized_name
			                OR mo_sibling.canonical_raw_name = mo.canonical_raw_name
			            )
			            /* 2026-08-08 P0 Fix: a sibling that admin has explicitly
			               disabled via the binding-level unavailable_reason='manual'
			               (or via credentials.manual_disabled / providers.manual_disabled)
			               must NOT count as a live failover. Without this guard, the
			               sibling EXISTS subquery returns TRUE while no real sibling
			               can take traffic — the lone routable candidate gets hard-
			               excluded by the recent_success_rate gate below, producing
			               candidates_count=0 and 503 for every Claude/GPT request. */
			            AND COALESCE(mo_sibling.unavailable_reason, '') NOT LIKE 'manual%'
			            AND COALESCE(c_sibling.manual_disabled, FALSE) = FALSE
			            AND COALESCE(p_sibling.manual_disabled, FALSE) = FALSE
			            AND NOT EXISTS (
			                SELECT 1 FROM model_probe_state mps_sibling
			                WHERE mps_sibling.credential_id = mo_sibling.credential_id
			                  AND mps_sibling.raw_model_name = mo_sibling.raw_model_name
			                  AND mps_sibling.state = 'broken_confirmed'
			            )
			      )
			  )

		  AND (
		      -- (1) exact match on the offer's canonical_raw_name (lowercase)
		      mo.canonical_raw_name = $1
		      -- (2) standardized-name match: the offer's standardized_name column
		      -- holds the prefix-stripped lowercase form (set at upsert time).
		      OR mo.standardized_name = $1
		      -- (3) model_name_mapping lookup: centralized raw->standardized mapping
		      OR mnm.standardized_name = $1
		      -- (4) alias match: client_model points to a canonical that this offer belongs to
		      OR EXISTS (
		          SELECT 1 FROM model_aliases ma2
		          WHERE ma2.raw_name = $1
		            AND COALESCE(ma2.status, 'active') = 'active'
		            AND (
		                (mo.canonical_id IS NOT NULL AND ma2.canonical_id = mo.canonical_id)
		                OR (mo.canonical_id IS NULL AND ma2.canonical_id IS NULL)
		            )
		      )
		  )
		ORDER BY
			CASE COALESCE(mo.billing_mode, 'per_token')
				WHEN 'free' THEN 1
				WHEN 'token_plan' THEN 1
				WHEN 'code_plan' THEN 1
				WHEN 'agent_plan' THEN 1
				WHEN 'monthly' THEN 1
				ELSE 2
			END,
			COALESCE(mo.manual_priority, 99),
			COALESCE(mo.routing_tier, 2),
			COALESCE(mo.weight, 100) DESC,
			-- Prefer the live recent rate when present; fall back to the static
			-- (often default 0.9) column. This makes healthy credentials sort
			-- above soft-degraded ones even when the static column is equal.
			COALESCE(rsr.rate, mo.success_rate, 0.9) DESC
		`, clientModelLower, tenantID, modality)

		if err == nil {
			break
		}

		if !isRetryableDBError(err) {
			return nil, fmt.Errorf("query candidates failed: %w (context: model=%s, tenant_id=%s)", err, clientModel, tenantID)
		}

		if attempt == maxAttempts-1 {
			return nil, fmt.Errorf("query candidates failed after %d attempts: %w (context: model=%s, tenant_id=%s)", maxAttempts, err, clientModel, tenantID)
		}

		backoff := time.Duration(50*(attempt+1)) * time.Millisecond
		recordCandidateDiagnostic("db_query_retry")
		slog.Warn("[candidate_diag] db query retry",
			"model", clientModel,
			"tenant_id", tenantID,
			"modality", modality,
			"attempt", attempt+1,
			"max_attempts", maxAttempts,
			"error", err.Error(),
			"backoff_ms", backoff.Milliseconds(),
		)

		if err := waitForRetry(ctx, backoff); err != nil {
			return nil, fmt.Errorf("wait to retry candidate query failed: %w (context: model=%s, tenant_id=%s)", err, clientModel, tenantID)
		}
	}

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Candidate
	for rows.Next() {
		var cand Candidate
		var offerRawModel string
		// concurrencyLimitAuto is the tuner-adjusted limit (see
		// credentialhealth/tuner.go); used below to derive a capacity-weighted
		// Weight when the operator has not set an explicit manual weight.
		var concurrencyLimitAuto *int
		if err := rows.Scan(
			&cand.CredentialID,
			&cand.ProviderID,
			&cand.BaseURL,
			&cand.Protocol,
			&cand.CatalogCode,
			&cand.Tier,
			&cand.Weight,
			&cand.RawModel,
			&cand.StandardizedName,
			&cand.SuccessRate,
			&cand.P95LatencyMs,
			&cand.ConcurrencyLimit,
			&concurrencyLimitAuto,
			&cand.FpSlotLimit,
			&cand.RPMLimit,
			&cand.ConcurrencyMode,
			&cand.TPMLimit,
			&cand.MaxQueueDepth,
			&cand.MaxQueueWaitMS,
			&cand.BalanceUSD,
			&cand.CircuitState,
			&cand.AvailabilityState,
			&cand.QuotaState,
			&cand.LifecycleStatus,
			&cand.PriceInPer1M,
			&cand.PriceOutPer1M,
			&cand.CacheReadPricePer1M,
			&cand.CacheWritePricePer1M,
			&cand.Routable,
			&cand.BlockReason,
			&cand.SupportsPromptCache,
			&cand.CacheMode,
			&cand.ManualPriority,
			&cand.ActiveSessions,
			&cand.ConsecutiveFailures,
			&cand.Currency,
			&cand.BillingMode,
			&offerRawModel,
			&cand.ContextWindow,
			&cand.QualityFixMode,
			&cand.RecentSuccessRate,
			&cand.RecentSamples,
		); err != nil {
			return nil, err
		}
		cand.OfferRawModel = offerRawModel
		applyCapacityWeightedLB(&cand, concurrencyLimitAuto)
		c.maybeExitSuspicious(cand.CredentialID, offerRawModel)
		out = append(out, cand)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// defaultManualWeight is the value the SQL layer fills via COALESCE(mo.weight,
// 100). It doubles as the sentinel for "operator did not set an explicit
// weight", which is how applyCapacityWeightedLB decides whether to override
// the manual weight with a capacity-derived one.
const defaultManualWeight = 100

// capacityWeightFloor / capacityWeightMax clamp the capacity-derived weight so
// a low-capacity credential still gets a non-zero share of first-choice
// attempts (promoteWeightedCandidate skips Weight<=0 candidates) and a
// high-capacity credential cannot monopolise the rotation. The clamp keeps
// the ratio meaningful: a credential with 2× the effective limit gets ~2× the
// first-attempt share, bounded so one giant credential doesn't starve the
// rest of the pool.
const (
	capacityWeightFloor = 1
	capacityWeightMax   = 1000
)

// applyCapacityWeightedLB derives Candidate.Weight from the credential's
// effective concurrency capacity (并发能力), so the router's weighted
// first-choice promotion (promoteWeightedCandidate) distributes session-first
// and error-triggered re-selection across healthy same-level nodes in
// proportion to their capacity — the load-balancing contract the gateway
// promises operators.
//
// Effective limit (mirrors domains/providerprofile/adapters.go GetModelScale):
//   - auto-tuned limit preferred, capped by the manual hard cap
//   - falls back to the manual hard cap when auto is unset
//   - unknown (both nil/0) → leave Weight at the default 100
//
// Override rules:
//   - if the operator set an explicit manual weight (≠ defaultManualWeight),
//     that weight is respected as-is — manual override always wins;
//   - otherwise Weight is set to clamp(effLimit, floor, max);
//   - Weight is never left at 0 (promoteWeightedCandidate treats 0 as
//     "skip", which would silently exclude a capacity-unknown credential).
//
// The derived weight is captured in the 30s candidate cache, so it is reused
// on every cache hit; admin force-enable / state changes invalidate the cache
// and the next rebuild picks up the latest concurrency_limit_auto.
func applyCapacityWeightedLB(cand *Candidate, concurrencyLimitAuto *int) {
	if cand == nil {
		return
	}
	// Respect an explicit operator weight (anything other than the SQL
	// default of 100). This preserves the manual escape hatch.
	if cand.Weight != defaultManualWeight {
		if cand.Weight <= 0 {
			cand.Weight = defaultManualWeight
		}
		return
	}
	manual := 0
	if cand.ConcurrencyLimit != nil {
		manual = *cand.ConcurrencyLimit
	}
	auto := 0
	if concurrencyLimitAuto != nil {
		auto = *concurrencyLimitAuto
	}
	eff := auto
	if eff <= 0 {
		eff = manual
	}
	if manual > 0 && eff > manual {
		eff = manual // never break the operator's hard cap
	}
	if eff <= 0 {
		// Capacity unknown — keep the default so the credential still
		// participates in weighted rotation.
		cand.Weight = defaultManualWeight
		return
	}
	eff = clampInt(eff, capacityWeightFloor, capacityWeightMax)
	cand.Weight = eff
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (c *Client) fetchPolicyDB(ctx context.Context) (*Policy, error) {
	if c.dbPool == nil {
		return nil, fmt.Errorf("policy DB not configured")
	}
	var pol Policy
	err := c.dbPool.QueryRow(ctx, `
		SELECT
			COALESCE(algorithm_version, 2)::int,
			COALESCE(retry_per_credential, 1)::int,
			COALESCE(tier_fallback_max, 3)::int,
			COALESCE(circuit_open_seconds, 300)::int,
			COALESCE(circuit_failure_threshold, 5)::int,
			COALESCE(circuit_max_open_seconds, 1800)::int,
			COALESCE(sticky_ttl_seconds, 1800)::int,
			COALESCE(transient_fail_threshold, 2)::int
		FROM routing_policy
		WHERE tenant_id = 'default'
		ORDER BY id
		LIMIT 1
	`).Scan(
		&pol.AlgorithmVersion,
		&pol.RetryPerCredential,
		&pol.TierFallbackMax,
		&pol.CircuitOpenSeconds,
		&pol.CircuitFailureThreshold,
		&pol.CircuitMaxOpenSeconds,
		&pol.StickyTTLSeconds,
		&pol.TransientFailThreshold,
	)
	if err != nil {
		return nil, err
	}
	return normalizePolicy(&pol), nil
}

func normalizePolicy(pol *Policy) *Policy {
	if pol == nil {
		return DefaultPolicy()
	}
	if pol.AlgorithmVersion == 0 {
		pol.AlgorithmVersion = 2
	}
	if pol.RetryPerCredential == 0 {
		pol.RetryPerCredential = 1
	}
	if pol.TierFallbackMax == 0 {
		pol.TierFallbackMax = 3
	}
	if pol.CircuitOpenSeconds == 0 {
		pol.CircuitOpenSeconds = 300
	}
	if pol.CircuitFailureThreshold == 0 {
		pol.CircuitFailureThreshold = 5
	}
	if pol.CircuitMaxOpenSeconds == 0 {
		pol.CircuitMaxOpenSeconds = 1800
	}
	if pol.StickyTTLSeconds == 0 {
		pol.StickyTTLSeconds = 1800
	}
	if pol.TransientFailThreshold == 0 {
		pol.TransientFailThreshold = 2
	}
	return pol
}

func uniqueRawModels(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		key := strings.ToLower(trimmed)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, trimmed)
	}
	return out
}

const maxRevealGenerationAttempts = 2

// negativeCacheEntry carries enough information for the cached-error
// metric path to attribute the failure to the original cause (e.g.
// unknown_format) while still letting the cache-amplification series
// count hits independently. msg is the original error string for log
// fidelity; reason is the closed-vocabulary metric label computed at
// write time so cached lookups do not need to re-classify.
type negativeCacheEntry struct {
	value   string
	reason  string
	expires time.Time
}

func (c *Client) RevealAPIKey(ctx context.Context, providerID, credentialID int) (string, error) {
	for attempt := 0; attempt < maxRevealGenerationAttempts; attempt++ {
		c.mu.RLock()
		generation := c.keyGeneration[credentialID]
		if entry, ok := c.keyCache[credentialID]; ok && time.Now().Before(entry.expires) {
			c.mu.RUnlock()
			return entry.value, nil
		}
		if neg, ok := c.keyCacheNeg[credentialID]; ok && time.Now().Before(neg.expires) {
			c.mu.RUnlock()
			// 2026-08-17 P0 fix: avoid re-trying a known-broken decryption for
			// decryptFailureCacheTTL. Previously every request within the 5-minute
			// positive window hit PG + DecryptAny + a "reveal failed" warning;
			// on 154 production this produced 60+ identical log lines per minute
			// for credentials whose secret was actually rotated / corrupted.
			slog.Debug("reveal: decrypt failure cached, skipping retry",
				"credential_id", credentialID,
				"provider_id", providerID,
				"cached_err", neg.value,
				"expires_in", time.Until(neg.expires).String(),
			)
			// Two counter increments: cached (amplification visibility) and
			// the original cause reason (root-cause frequency), both with
			// the same provider_id. The reason was computed at cache
			// write time so the error chain does not need to survive into
			// the cached string form.
			recordCredentialRevealCachedHit(providerID, neg.reason)
			return "", fmt.Errorf("%w (credential_id=%d): %s", errRevealCached, credentialID, neg.value)
		}
		c.mu.RUnlock()

		v, err, _ := c.sf.Do(fmt.Sprintf("key:%d:g%d", credentialID, generation), func() (any, error) {
			key, fetchErr := c.fetchReveal(ctx, providerID, credentialID)
			if fetchErr != nil {
				c.cacheRevealFailureIfCurrent(credentialID, generation, fetchErr)
				return "", fetchErr
			}
			c.cacheRevealedKeyIfCurrent(credentialID, generation, key)
			return key, nil
		})

		c.mu.RLock()
		currentGeneration := c.keyGeneration[credentialID]
		c.mu.RUnlock()
		if currentGeneration != generation {
			continue
		}
		if err != nil {
			return "", err
		}
		return v.(string), nil
	}
	return "", fmt.Errorf("%w (credential_id=%d)", errRevealRotation, credentialID)
}

func (c *Client) cacheRevealFailureIfCurrent(credentialID int, generation uint64, fetchErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keyGeneration[credentialID] == generation {
		c.recordNegativeCacheLocked(credentialID, fetchErr.Error(), classifyRevealFailure(fetchErr))
	}
}

func (c *Client) cacheRevealedKeyIfCurrent(credentialID int, generation uint64, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.keyGeneration[credentialID] != generation {
		return
	}
	c.keyCache[credentialID] = cacheEntry[string]{
		value:   key,
		expires: time.Now().Add(5 * time.Minute),
	}
	// Successful reveal invalidates any prior negative entry so the next call
	// doesn't carry forward a stale "broken" signal.
	delete(c.keyCacheNeg, credentialID)
}

// recordNegativeCacheLocked inserts a decrypt-failure entry into
// keyCacheNeg, evicting the oldest entries if the cap is exceeded. Must be
// called with c.mu held for writing. reason is the closed-vocabulary
// metric label for this failure; cached lookups will use it directly
// instead of re-classifying the error string.
func (c *Client) recordNegativeCacheLocked(credentialID int, errMsg, reason string) {
	if c.keyCacheNeg == nil {
		c.keyCacheNeg = make(map[int]negativeCacheEntry)
	}
	if len(c.keyCacheNeg) >= decryptFailureCacheMax {
		// Evict the entry with the earliest expiry; the map is small so a
		// linear scan is cheap and avoids dragging in a heap for a corner
		// case that only fires when there are 1024+ simultaneously broken
		// credentials — by definition a deeper problem than this cache.
		var oldestID int
		var oldestExp time.Time
		first := true
		for id, e := range c.keyCacheNeg {
			if first || e.expires.Before(oldestExp) {
				oldestID = id
				oldestExp = e.expires
				first = false
			}
		}
		delete(c.keyCacheNeg, oldestID)
	}
	c.keyCacheNeg[credentialID] = negativeCacheEntry{
		value:   errMsg,
		reason:  reason,
		expires: time.Now().Add(decryptFailureCacheTTL),
	}
}

func (c *Client) fetchReveal(ctx context.Context, providerID, credentialID int) (string, error) {
	if c.dbPool != nil && (c.keyring != nil || len(c.fernetKey) == 32) {
		return c.fetchRevealDB(ctx, providerID, credentialID)
	}
	return "", fmt.Errorf("%w (no DB, keyring, or fernet key)", errRevealNotConfigured)
}

func (c *Client) fetchRevealDB(ctx context.Context, providerID, credentialID int) (string, error) {
	var ciphertext []byte
	err := c.dbPool.QueryRow(ctx, `
		SELECT secret_ciphertext
		FROM credentials
		WHERE id = $1 AND provider_id = $2 AND status <> 'disabled'
	`, credentialID, providerID).Scan(&ciphertext)
	if err != nil {
		if err == pgx.ErrNoRows {
			return "", fmt.Errorf("credential %d: %w", credentialID, errRevealNotFound)
		}
		return "", err
	}
	if len(ciphertext) == 0 {
		return "", nil
	}
	pt, _, err := secret.DecryptAny(string(ciphertext), c.keyring, c.fernetKey)
	if err != nil {
		// Wrap the upstream error with a package-local sentinel so callers
		// (metrics, tests) can classify via errors.Is. The original error
		// remains reachable through Unwrap for log formatting.
		if strings.Contains(err.Error(), "unknown format") {
			return "", fmt.Errorf("%w: %v", errRevealUnknownFormat, err)
		}
		return "", fmt.Errorf("%w: %v", errRevealDecrypt, err)
	}
	return string(pt), nil
}

// fetchExtraKeys returns the decrypted extra keys for a credential from the
// credential_keys table (kid_index >= 1, status='active'), ordered by kid_index.
// The primary key (index 0) is NOT included — it lives in credentials and is
// fetched by fetchRevealDB. Returns nil when the table has no rows or when the
// table doesn't exist (graceful degradation for pre-076-migration deployments).
func (c *Client) fetchExtraKeys(ctx context.Context, credentialID int) []string {
	if c.dbPool == nil {
		return nil
	}
	rows, err := c.dbPool.Query(ctx, `
		SELECT secret_ciphertext
		FROM credential_keys
		WHERE credential_id = $1 AND status = 'active'
		ORDER BY kid_index
	`, credentialID)
	if err != nil {
		// table missing (pre-076) or query error — degrade to single-key mode.
		return nil
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var ciphertext []byte
		if err := rows.Scan(&ciphertext); err != nil {
			continue
		}
		if len(ciphertext) == 0 {
			continue
		}
		pt, _, err := secret.DecryptAny(string(ciphertext), c.keyring, c.fernetKey)
		if err != nil {
			slog.Warn("fetchExtraKeys: decrypt failed, skipping key",
				"credential_id", credentialID, "error", err)
			continue
		}
		keys = append(keys, string(pt))
	}
	return keys
}

func (c *Client) enrichWithAPIKeys(ctx context.Context, rr *resolveResponse) []Candidate {
	if rr == nil {
		return nil
	}

	planSet := make(map[int]bool, len(rr.PlanOrder))
	for _, p := range rr.PlanOrder {
		planSet[p.CredentialID] = true
	}

	var cands []Candidate
	var skippedCount int
	for _, raw := range rr.Candidates {
		var cand Candidate
		if err := json.Unmarshal(raw, &cand); err != nil {
			continue
		}
		if !planSet[cand.CredentialID] {
			continue
		}

		apiKey, err := c.RevealAPIKey(ctx, cand.ProviderID, cand.CredentialID)
		if err != nil {
			// 2026-07-08 P0 fix: 密钥解密失败时不再硬性过滤候选者。
			// 修复场景：当部分或全部候选者的密钥解密失败时（密钥配置错误、
			// 加密版本不兼容等），如果硬性过滤会导致路由器收到空列表，
			// 触发"无可用路由"错误（即使数据库中有可用凭据）。
			//
			// 降级策略：
			// - 保留候选者但标记为不可路由（Routable=false）
			// - 设置 BlockReason 说明原因
			// - 记录警告日志供运维排查
			// - 路由器的 filterAvailable 会过滤掉这些候选者
			//
			// 这样做的好处：
			// 1. 当只有部分密钥解密失败时，其他候选者仍可用
			// 2. 当全部失败时，路由器会进入降级模式（tryDegradedMode）
			// 3. 保留候选者元数据，便于 GetCandidates 调用方诊断
			slog.Warn("enrichWithAPIKeys: reveal failed, marking candidate unavailable",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"error", err,
			)
			// Cached failures are already counted by recordCredentialRevealCachedHit
			// inside RevealAPIKey; counting them again here would over-report
			// cache amplification. Only record fresh failures here.
			if !errors.Is(err, errRevealCached) {
				recordCredentialRevealFailure(cand.ProviderID, err)
			}
			skippedCount++
			reason := fmt.Sprintf("key_decrypt_failed: %v (credential_id=%d, provider_id=%d)", err, cand.CredentialID, cand.ProviderID)
			cand.Routable = false
			cand.BlockReason = &reason
			cand.APIKey = "" // 确保没有泄漏部分解密的数据
			cands = append(cands, cand)
			continue
		}
		cand.APIKey = apiKey
		// multi-key: fetch extra keys and register with the rotator so the
		// executor can round-robin across all keys for this credential.
		if c.keyRotator != nil {
			extras := c.fetchExtraKeys(ctx, cand.CredentialID)
			if len(extras) > 0 {
				cand.APIKeys = extras
				cand.KeyRotator = c.keyRotator
				// count = 1 primary + len(extras); rotator indices 0..N.
				c.keyRotator.EnsureCred(cand.CredentialID, 1+len(extras))
			}
		}
		cands = append(cands, cand)
	}

	// 2026-07-08: 当有候选者因密钥解密失败被降级时，记录汇总日志
	if skippedCount > 0 {
		slog.Warn("enrichWithAPIKeys: some candidates marked unavailable due to key decrypt failure",
			"total_candidates", len(rr.Candidates),
			"failed_count", skippedCount,
			"available_count", len(cands)-skippedCount,
		)
	}

	planOrder := rr.PlanOrder
	orderMap := make(map[int]int, len(planOrder))
	for i, p := range planOrder {
		orderMap[p.CredentialID] = i
	}

	byID := make(map[int]*Candidate, len(cands))
	for i := range cands {
		byID[cands[i].CredentialID] = &cands[i]
	}

	ordered := make([]Candidate, 0, len(planOrder))
	for _, p := range planOrder {
		if cand, ok := byID[p.CredentialID]; ok {
			ordered = append(ordered, *cand)
		}
	}
	return ordered
}

func (c *Client) maybeExitSuspicious(credentialID int, rawModel string) {
	if c.redis == nil || rawModel == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	data, err := c.redis.HGetAll(ctx, fmt.Sprintf("llmgw:avail:%d:%s", credentialID, rawModel)).Result()
	if err != nil || len(data) == 0 || data["state"] != "suspicious" {
		recordSuspiciousExit("noop")
		return
	}

	if c.asyncExitSuspicious == nil {
		recordSuspiciousExit("no_writer")
		return
	}
	recordSuspiciousExit("dispatched")
	c.asyncExitSuspicious(credentialID, rawModel)
}

func (c *Client) defaultAsyncExitSuspicious(credentialID int, rawModel string) {
	if c.dbPool == nil || c.redis == nil {
		return
	}
	go func() {
		bgCtx, bgCancel := context.WithTimeout(context.Background(), time.Second)
		defer bgCancel()
		dbStart := time.Now()
		_, err := c.dbPool.Exec(bgCtx, `
			UPDATE model_probe_state
			SET state = 'recovering',
			    next_retry_at = NOW() + INTERVAL '30 seconds',
			    consecutive_successes = 0,
			    consecutive_failures = 0,
			    last_state_change_at = NOW()
			WHERE credential_id = $1
			  AND raw_model_name = $2
			  AND state = 'suspicious'
		`, credentialID, rawModel)
		recordSuspiciousExitDBDuration(time.Since(dbStart).Seconds())
		if err != nil {
			slog.Warn("provider: maybeExitSuspicious db update failed",
				"credential_id", credentialID,
				"raw_model", rawModel,
				"error", err)
			recordSuspiciousExit("db_error")
			return
		}
		nextRetryAt := time.Now().Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
		if cacheErr := c.redis.HSet(bgCtx, fmt.Sprintf("llmgw:avail:%d:%s", credentialID, rawModel), map[string]any{
			"state":         "recovering",
			"updated_at":    time.Now().UTC().Format(time.RFC3339Nano),
			"next_retry_at": nextRetryAt,
			"source":        "call_exit",
		}).Err(); cacheErr != nil {
			slog.Warn("provider: maybeExitSuspicious cache update failed",
				"credential_id", credentialID,
				"raw_model", rawModel,
				"error", cacheErr)
			recordSuspiciousExit("cache_error")
			return
		}
		// 2026-07-03: Bug #3 fix - invalidate candidate cache after suspicious->recovering
		// Without this, router sees stale candidates for 30s
		InvalidateAllCandidateCache()
		recordSuspiciousExit("dispatched")
	}()
}
