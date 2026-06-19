package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/secret"
	"golang.org/x/sync/singleflight"
)

type Candidate struct {
	CredentialID        int      `json:"credential_id"`
	ProviderID          int      `json:"provider_id"`
	BaseURL             string   `json:"base_url"`
	Protocol            string   `json:"protocol"`
	CatalogCode         string   `json:"catalog_code"`
	Tier                int      `json:"tier"`
	Weight              int      `json:"weight"`
	RawModel            string   `json:"model_name"` // upstream name: COALESCE(outbound_model_name, raw_model_name)
	OfferRawModel       string   `json:"raw_model_name,omitempty"` // mo.raw_model_name for transform templates
	StandardizedName    string   `json:"standardized_name"`
	SuccessRate         float64  `json:"success_rate"`
	P95LatencyMs        int      `json:"p95_latency_ms"`
	ConcurrencyLimit    *int     `json:"concurrency_limit"`
	BalanceUSD          *float64 `json:"balance_usd"`
	CircuitState        string   `json:"circuit_state"`
	AvailabilityState   string   `json:"availability_state"`
	QuotaState          string   `json:"quota_state"`
	LifecycleStatus     string   `json:"lifecycle_status"`
	Routable            bool     `json:"runtime_routable"`
	BlockReason         *string  `json:"runtime_block_reason"`
	PriceInPer1M        *float64 `json:"unit_price_in_per_1m"`
	PriceOutPer1M       *float64 `json:"unit_price_out_per_1m"`
	CacheReadPricePer1M *float64 `json:"cache_read_price_per_1m"`
	CacheWritePricePer1M *float64 `json:"cache_write_price_per_1m"`
	SupportsPromptCache bool     `json:"supports_prompt_cache"`
	CacheMode           string   `json:"cache_mode"`
	ManualPriority      int      `json:"manual_priority"`
	ActiveSessions      int      `json:"active_sessions"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	CompositeScore      float64  `json:"composite_score"`
	Currency            string   `json:"currency"`
	BillingMode         string   `json:"billing_mode"`
	// ContextWindow is the upstream model's context window in tokens, read
	// from models_canonical.context_window. Used by the Q1/Q2/Q3 client-side
	// context trim path (transform.CompressMessagesIfNeeded). nil means
	// "unknown" — in which case the trim path is a no-op.
	ContextWindow *int `json:"context_window,omitempty"`
	APIKey        string `json:"-"`
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
// candidate cannot be used, or "" if it is fully available. Order matches
// IsAvailable() so the first non-nil branch is the dominant reason.
func (c *Candidate) UnavailableReason() string {
	if !c.Routable {
		if c.BlockReason != nil && *c.BlockReason != "" {
			return "routing_blocked:" + *c.BlockReason
		}
		return "routing_blocked"
	}
	if c.LifecycleStatus != "" && c.LifecycleStatus != "active" {
		return "lifecycle:" + c.LifecycleStatus
	}
	switch c.AvailabilityState {
	case "suspended":
		return "availability:suspended"
	case "auth_failed":
		return "availability:auth_failed"
	case "cooling":
		return "availability:cooling"
	case "rate_limited":
		return "availability:rate_limited"
	case "unreachable":
		return "availability:unreachable"
	}
	switch c.QuotaState {
	case "balance_exhausted":
		return "quota:balance_exhausted"
	case "permanently_exhausted":
		return "quota:permanently_exhausted"
	case "periodic_exhausted":
		return "quota:periodic_exhausted"
	}
	if c.BalanceUSD != nil && *c.BalanceUSD <= 0 {
		return "balance:zero"
	}
	return ""
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
	StickyTTLSeconds      int `json:"sticky_ttl_seconds"`
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

type Client struct {
	dbPool    *pgxpool.Pool
	fernetKey []byte
	keyring   *secret.Keyring

	mu        sync.RWMutex
	candCache map[string]cacheEntry[*resolveResponse]
	polCache  cacheEntry[*Policy]
	keyCache  map[int]cacheEntry[string]

	sf singleflight.Group
}

var defaultClient *Client

func NewClient() *Client {
	c := &Client{
		candCache: make(map[string]cacheEntry[*resolveResponse]),
		keyCache:  make(map[int]cacheEntry[string]),
	}
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
	defaultClient.candCache = make(map[string]cacheEntry[*resolveResponse])
	defaultClient.mu.Unlock()
	slog.Info("candidate cache invalidated")
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
}

func (c *Client) GetCandidates(ctx context.Context, model, profile string) ([]Candidate, *Policy, error) {
	if !c.Enabled() {
		return nil, DefaultPolicy(), fmt.Errorf("provider client not configured")
	}
	routeModel := modelname.NormalizeRouteKey(model)

	key := routeModel
	if profile != "" {
		key = routeModel + "|" + profile
	}

	c.mu.RLock()
	if entry, ok := c.candCache[key]; ok && time.Now().Before(entry.expires) {
		c.mu.RUnlock()
		policy, _ := c.getPolicyCached(ctx)
		cands := c.enrichWithAPIKeys(ctx, entry.value)
		return cands, policy, nil
	}
	c.mu.RUnlock()

	v, err, _ := c.sf.Do("cand:"+key, func() (any, error) {
		resp, fetchErr := c.fetchCandidatesDB(ctx, routeModel, profile)
		if fetchErr != nil {
			return nil, fetchErr
		}

		c.mu.Lock()
		c.candCache[key] = cacheEntry[*resolveResponse]{
			value:   resp,
			expires: time.Now().Add(30 * time.Second),
		}
		c.mu.Unlock()
		return resp, nil
	})
	if err != nil {
		return nil, DefaultPolicy(), err
	}

	policy, _ := c.getPolicyCached(ctx)
	cands := c.enrichWithAPIKeys(ctx, v.(*resolveResponse))
	return cands, policy, nil
}

func (c *Client) GetPolicy(ctx context.Context) (*Policy, error) {
	if !c.Enabled() {
		return DefaultPolicy(), nil
	}
	return c.getPolicyCached(ctx)
}

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

func (c *Client) fetchCandidatesDB(ctx context.Context, model, profile string) (*resolveResponse, error) {
	if c.dbPool == nil {
		return nil, fmt.Errorf("routing DB not configured")
	}
	res, err := c.resolveModelDB(ctx, model, profile)
	if err != nil {
		return nil, err
	}
	cands, err := c.loadCandidatesDB(ctx, res.ClientModel)
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
	rawLookup := strings.TrimSpace(strings.ToLower(model))

	var canonicalID *int
	var canonicalName string
	err := c.dbPool.QueryRow(ctx, `
		SELECT id, canonical_name
		FROM models_canonical
		WHERE lower(canonical_name) = lower($1)
		  AND COALESCE(status, 'active') = 'active'
	`, model).Scan(&canonicalID, &canonicalName)
	if err == nil && canonicalID != nil {
		raw, err := c.aliasRawNamesDB(ctx, *canonicalID, profile)
		if err != nil {
			return nil, err
		}
		return &resolveResponse{ClientModel: model, CanonicalName: canonicalName, CanonicalID: canonicalID, ResolutionPath: "canonical", RawModels: lowerUnique(append(raw, model))}, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}

	err = c.dbPool.QueryRow(ctx, `
		SELECT mc.id, mc.canonical_name
		FROM model_aliases ma
		JOIN models_canonical mc ON mc.id = ma.canonical_id
		WHERE lower(ma.raw_name) = lower($1)
		  AND COALESCE(ma.status, 'active') = 'active'
		  AND COALESCE(mc.status, 'active') = 'active'
		  AND (
		      ma.client_profiles IS NULL
		      OR cardinality(ma.client_profiles) = 0
		      OR $2 = ANY(ma.client_profiles)
		      OR $2 = ''
		  )
		LIMIT 1
	`, model, profile).Scan(&canonicalID, &canonicalName)
	if err == nil && canonicalID != nil {
		raw, err := c.aliasRawNamesDB(ctx, *canonicalID, profile)
		if err != nil {
			return nil, err
		}
		return &resolveResponse{ClientModel: model, CanonicalName: canonicalName, CanonicalID: canonicalID, ResolutionPath: "alias", RawModels: lowerUnique(append(raw, model))}, nil
	}
	if err != nil && err != pgx.ErrNoRows {
		return nil, err
	}

	if rawLookup != "" && rawLookup != model {
		err = c.dbPool.QueryRow(ctx, `
			SELECT mc.id, mc.canonical_name
			FROM model_aliases ma
			JOIN models_canonical mc ON mc.id = ma.canonical_id
			WHERE lower(ma.raw_name) = lower($1)
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
			return &resolveResponse{ClientModel: model, CanonicalName: canonicalName, CanonicalID: canonicalID, ResolutionPath: "raw_fallback", RawModels: lowerUnique(append(raw, model, rawLookup))}, nil
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
//	OR EXISTS alias match (ma.canonical_id = mo.canonical_id)
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
func (c *Client) loadCandidatesDB(ctx context.Context, clientModel string) ([]Candidate, error) {
	if c.dbPool == nil {
		return nil, nil
	}
	clientModelLower := strings.ToLower(clientModel)
	rows, err := c.dbPool.Query(ctx, `
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
			COALESCE(mo.billing_mode, 'token') AS billing_mode,
			mo.raw_model_name,
			mc.context_window
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN v_routable_credential_models v
		       ON v.credential_id = mo.credential_id
		      AND v.raw_model_name = mo.raw_model_name
		LEFT JOIN credential_capabilities cc ON cc.credential_id = c.id AND cc.capability = 'prompt_caching'
		LEFT JOIN model_aliases ma
		       ON lower(ma.raw_name) = lower(mo.raw_model_name)
		      AND COALESCE(ma.status, 'active') = 'active'
		LEFT JOIN models_canonical mc ON mc.id = COALESCE(mo.canonical_id, ma.canonical_id)
		WHERE p.tenant_id = 'default'
		  AND COALESCE(mc.status, 'active') != 'disabled'
		  AND COALESCE(c.status, 'active') NOT IN ('disabled')
		  -- v.is_routable is FALSE for any model with manual disable at any layer
		  -- (provider.manual_disabled, credentials.manual_disabled, or cmb.unavailable_reason='manual')
		  AND v.is_routable = TRUE
		  AND (
		      -- (1) exact (case-insensitive) match on the offer's raw_model_name
		      lower(mo.raw_model_name) = $1
		      -- (2) alias match: client_model points to a canonical that this offer belongs to
		      OR EXISTS (
		          SELECT 1 FROM model_aliases ma2
		          WHERE lower(ma2.raw_name) = $1
		            AND COALESCE(ma2.status, 'active') = 'active'
		            AND (
		                (mo.canonical_id IS NOT NULL AND ma2.canonical_id = mo.canonical_id)
		                OR (mo.canonical_id IS NULL AND ma2.canonical_id IS NULL)
		            )
		      )
		  )
		ORDER BY
			CASE COALESCE(mo.billing_mode, 'token')
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
			COALESCE(mo.success_rate, 0.9) DESC
	`, clientModelLower)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Candidate
	for rows.Next() {
		var cand Candidate
		var offerRawModel string
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
		); err != nil {
			return nil, err
		}
		cand.OfferRawModel = offerRawModel
		out = append(out, cand)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
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

func lowerUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func (c *Client) RevealAPIKey(ctx context.Context, providerID, credentialID int) (string, error) {
	c.mu.RLock()
	if entry, ok := c.keyCache[credentialID]; ok && time.Now().Before(entry.expires) {
		c.mu.RUnlock()
		return entry.value, nil
	}
	c.mu.RUnlock()

	v, err, _ := c.sf.Do(fmt.Sprintf("key:%d", credentialID), func() (any, error) {
		key, fetchErr := c.fetchReveal(ctx, providerID, credentialID)
		if fetchErr != nil {
			return "", fetchErr
		}
		c.mu.Lock()
		c.keyCache[credentialID] = cacheEntry[string]{
			value:   key,
			expires: time.Now().Add(5 * time.Minute),
		}
		c.mu.Unlock()
		return key, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (c *Client) fetchReveal(ctx context.Context, providerID, credentialID int) (string, error) {
	if c.dbPool != nil && (c.keyring != nil || len(c.fernetKey) == 32) {
		return c.fetchRevealDB(ctx, providerID, credentialID)
	}
	return "", fmt.Errorf("credential reveal not configured (no DB, keyring, or fernet key)")
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
			return "", fmt.Errorf("credential %d not found", credentialID)
		}
		return "", err
	}
	if len(ciphertext) == 0 {
		return "", nil
	}
	pt, _, err := secret.DecryptAny(string(ciphertext), c.keyring, c.fernetKey)
	if err != nil {
		return "", err
	}
	return string(pt), nil
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
			slog.Warn("failed to reveal api key",
				"credential_id", cand.CredentialID,
				"provider_id", cand.ProviderID,
				"error", err,
			)
			continue
		}
		cand.APIKey = apiKey
		cands = append(cands, cand)
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
