package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/provider"
	"github.com/kaixuan/llm-gateway-go/routing"
)

type routingHandler struct {
	db     *pgxpool.Pool
	secret string
}

// availableModelsCache caches /api/routing/available-models responses
// for a short window (default 30s).  The endpoint aggregates 4 separate
// DB queries (model_offers + LATERAL aliases, routing_policy,
// model_aliases, plus the popular-usage 7d scan) which together
// dominate admin page load latency.  Since the underlying data is
// updated only on provider refresh / admin edits, a 30s window is
// safe and gives an order-of-magnitude speedup under repeated
// page-load / tab-switch traffic.
type availableModelsCache struct {
	mu        sync.Mutex
	value     map[string]any
	expiresAt time.Time
	ttl       time.Duration
	// hits/misses are exposed via /api/system/background-tasks for ops.
	hits   uint64
	misses uint64
}

func (c *availableModelsCache) get(now time.Time) (map[string]any, bool) {
	if c == nil || c.value == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Before(c.expiresAt) {
		c.hits++
		return c.value, true
	}
	c.misses++
	return nil, false
}

func (c *availableModelsCache) set(now time.Time, value map[string]any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value = value
	c.expiresAt = now.Add(c.ttl)
}

const availableModelsCacheTTL = 30 * time.Second

var globalAvailableModelsCache = &availableModelsCache{ttl: availableModelsCacheTTL}

func (h *Handler) logAudit(r *http.Request, action string, details map[string]any) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	detailsJSON, _ := json.Marshal(details)
	// Do NOT use X-Actor header as the authoritative identity — any client that
	// can reach the admin API can forge it. Use the verified remote address as
	// a tamper-evident fallback. If a proper auth layer (JWT, mTLS) is added
	// later, extract the principal from that context instead.
	actor := r.RemoteAddr
	if actor == "" {
		actor = "unknown"
	}

	// Schema (sql/030_routing_v2.sql): id, ts, actor, action, target_type, target_id,
	// before_json, after_json. Use action as target_type so logAudit stays a
	// single-call helper; structured details go into after_json.
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO routing_audit_log (actor, action, target_type, after_json)
		VALUES ($1, $2, $3, $4)
	`, actor, action, action, detailsJSON)
}

func (h *Handler) handleRoutingResolve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	model := queryString(r, "model")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model parameter required")
		return
	}
	// 2026-06-19 audit: walk the cross-form variant matrix so a
	// request like "claude-sonnet-4.6" matches a DB canonical
	// "claude-sonnet-4-6" (and the inverse).  The variant matrix is
	// produced by modelname.NormalizeRouteKeyAliases, which keeps
	// exact-match forms first and adds dot↔dash bridges behind.
	variants := modelname.NormalizeRouteKeyAliases(model)
	if len(variants) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"client_model":    model,
			"canonical_name":  model,
			"canonical_id":    nil,
			"resolution_path": "direct",
			"raw_models":      []string{model},
			"plan_order":      []any{},
			"candidates":      []any{},
		})
		return
	}
	normalizedModel := variants[0]

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type candidate struct {
		Rank                 int      `json:"rank"`
		ProviderID           int      `json:"provider_id"`
		ProviderName         string   `json:"provider_name"`
		CatalogCode          string   `json:"catalog_code"`
		Protocol             string   `json:"protocol"`
		BaseURL              string   `json:"base_url"`
		ProviderEnabled      bool     `json:"provider_enabled"`
		CredentialID         int      `json:"credential_id"`
		CredentialLabel      string   `json:"credential_label"`
		CredentialStatus     string   `json:"credential_status"`
		LifecycleStatus      string   `json:"lifecycle_status"`
		AvailabilityState    string   `json:"availability_state"`
		AvailabilityRecoverAt *string `json:"availability_recover_at"`
		QuotaState           string   `json:"quota_state"`
		QuotaRecoverAt       *string  `json:"quota_recover_at"`
		ConcurrencyLimit     *int     `json:"concurrency_limit"`
		EffectiveConcurrency *int     `json:"effective_concurrency"`
		EffectiveAt          *string  `json:"effective_at"`
		ExpiresAt            *string  `json:"expires_at"`
		CredentialInEffect   bool     `json:"credential_in_effect"`
		BalanceUSD           *float64 `json:"balance_usd"`
		CircuitState         string   `json:"circuit_state"`
		CoolingUntil         *string  `json:"cooling_until"`
		Available            bool     `json:"available"`
		Tier                 int      `json:"tier"`
		Weight               int      `json:"weight"`
		UnitPriceInPer1M     *float64 `json:"unit_price_in_per_1m"`
		UnitPriceOutPer1M    *float64 `json:"unit_price_out_per_1m"`
		Currency             string   `json:"currency"`
		SuccessRate          float64  `json:"success_rate"`
		P95LatencyMs         int      `json:"p95_latency_ms"`
		ModelName            string   `json:"model_name"`
		StandardizedName     string   `json:"standardized_name"`
		QuotaCapUSD          float64  `json:"quota_cap_usd"`
		QuotaUsedUSD         float64  `json:"quota_used_usd"`
		RuntimeRoutable      bool     `json:"runtime_routable"`
		Routable             bool     `json:"routable"`
		BlockReason          string   `json:"block_reason,omitempty"`
		ManualPriority       int      `json:"manual_priority"`
		ActiveSessions       int      `json:"active_sessions"`
		ConsecutiveFailures  int      `json:"consecutive_failures"`
		CompositeScore       float64  `json:"composite_score"`
		BillingMode          string   `json:"billing_mode"`
		BillingRound         int      `json:"billing_round"`
		RecentSuccessRate    *float64 `json:"recent_success_rate,omitempty"`
		RecentSamples        *int     `json:"recent_samples,omitempty"`
	}

	rawModels := append([]string{normalizedModel}, variants[1:]...)
	// 2026-06-26: Mirror provider.Client.loadCandidatesDB query so resolve API
	// shows exactly what real routing sees, including all filter reasons.
	rows, err := h.db.Query(ctx, `
		SELECT
			p.id AS provider_id,
			COALESCE(p.display_name, p.code) AS provider_name,
			COALESCE(p.catalog_code, '') AS catalog_code,
			COALESCE(p.protocol, 'openai-completions') AS protocol,
			p.base_url,
			p.enabled AS provider_enabled,
			p.manual_disabled AS provider_manual_disabled,
			c.id AS credential_id,
			COALESCE(c.label, '') AS credential_label,
			COALESCE(c.status, 'active') AS credential_status,
			COALESCE(c.lifecycle_status, 'active') AS lifecycle_status,
			COALESCE(c.availability_state, 'ready') AS availability_state,
			c.availability_recover_at::text,
			COALESCE(c.quota_state, 'ok') AS quota_state,
			c.quota_recover_at::text,
			c.concurrency_limit,
			c.effective_concurrency,
			c.effective_at::text,
			c.expires_at::text,
			(c.effective_at IS NULL OR c.effective_at <= now())
				AND (c.expires_at IS NULL OR c.expires_at > now()) AS credential_in_effect,
			c.balance_usd::float8,
			COALESCE(c.circuit_state, 'closed') AS circuit_state,
			c.cooling_until::text,
			mo.available,
			COALESCE(mo.routing_tier, 2) AS tier,
			COALESCE(mo.weight, 100) AS weight,
			COALESCE(mo.manual_priority, 99) AS manual_priority,
			COALESCE(mo.active_sessions, 0) AS active_sessions,
			COALESCE(mo.consecutive_failures, 0) AS consecutive_failures,
			COALESCE(mo.billing_mode, 'token') AS billing_mode,
			mo.unit_price_in_per_1m,
			mo.unit_price_out_per_1m,
			COALESCE(mo.currency, 'USD') AS currency,
			COALESCE(mo.success_rate, 0.9)::float8 AS success_rate,
			COALESCE(mo.p95_latency_ms, 9999) AS p95_latency_ms,
			mo.raw_model_name AS model_name,
			COALESCE(mo.standardized_name, mo.raw_model_name) AS standardized_name,
			COALESCE(mo.unit_price_in_per_1m, 0) AS quota_cap_usd,
			COALESCE(mo.unit_price_out_per_1m, 0) AS quota_used_usd,
		-- Routing filter columns (2026-06-26 audit fix)
		COALESCE(v.is_routable, FALSE) AS is_routable,
		mps.state AS probe_state,
		rsr.rate AS recent_success_rate,
		rsr.samples AS recent_samples
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
	LEFT JOIN v_routable_credential_models v
	       ON v.credential_id = mo.credential_id
	      AND v.raw_model_name = mo.raw_model_name
	LEFT JOIN model_probe_state mps
	       ON mps.credential_id = c.id
	      AND mps.raw_model_name = mo.raw_model_name
	LEFT JOIN LATERAL (
			SELECT 
				COUNT(CASE WHEN success THEN 1 END)::float / NULLIF(COUNT(*), 0) AS rate,
				COUNT(*) AS samples
			FROM (
				SELECT success
				FROM request_logs 
				WHERE credential_id = c.id 
				  AND outbound_model = mo.raw_model_name 
				ORDER BY ts DESC 
				LIMIT 50
			) recent
		) AS rsr ON true
		WHERE p.tenant_id = 'default'
		  AND (lower(mo.raw_model_name) = ANY($1) OR lower(mo.standardized_name) = ANY($1))
		  AND mo.available IS TRUE
		  AND c.status IN ('active','cooling','degraded')
		  AND p.enabled IS TRUE
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
			COALESCE(rsr.rate, mo.success_rate, 0.9) DESC
	`, rawModels)
	if err != nil {
		slog.Error("routing resolve query failed", "error", err, "model", model)
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	weights := scoringWeightsFromMap(h.getScoringWeights(ctx))
	candidates := make([]candidate, 0)
	for rows.Next() {
		var c candidate
		var providerManualDisabled bool
		var isRoutable bool
		var probeState *string
		var recentSuccessRate *float64
		var recentSamples *int
		if err := rows.Scan(
			&c.ProviderID, &c.ProviderName, &c.CatalogCode, &c.Protocol, &c.BaseURL,
			&c.ProviderEnabled, &providerManualDisabled, &c.CredentialID, &c.CredentialLabel, &c.CredentialStatus,
			&c.LifecycleStatus, &c.AvailabilityState, &c.AvailabilityRecoverAt,
			&c.QuotaState, &c.QuotaRecoverAt, &c.ConcurrencyLimit, &c.EffectiveConcurrency,
			&c.EffectiveAt, &c.ExpiresAt, &c.CredentialInEffect, &c.BalanceUSD,
			&c.CircuitState, &c.CoolingUntil, &c.Available, &c.Tier, &c.Weight,
			&c.ManualPriority, &c.ActiveSessions, &c.ConsecutiveFailures, &c.BillingMode,
			&c.UnitPriceInPer1M, &c.UnitPriceOutPer1M, &c.Currency, &c.SuccessRate,
			&c.P95LatencyMs, &c.ModelName, &c.StandardizedName,
			&c.QuotaCapUSD, &c.QuotaUsedUSD,
			&isRoutable, &probeState, &recentSuccessRate, &recentSamples,
		); err != nil {
			continue
		}
		
		// 2026-06-26: Multi-tier quality gate matching provider.Client.loadCandidatesDB
		// Store raw filter results for multi-round evaluation
		c.RuntimeRoutable = c.Available &&
			c.CredentialStatus == "active" &&
			c.LifecycleStatus == "active" &&
			c.AvailabilityState == "ready" &&
			c.QuotaState == "ok" &&
			c.CircuitState != "open" &&
			isRoutable &&  // from v_routable_credential_models
			!providerManualDisabled &&
			(probeState == nil || *probeState != "broken_confirmed")
		
		// Store recent success metrics for multi-round evaluation
		c.RecentSuccessRate = recentSuccessRate
		c.RecentSamples = recentSamples
		
		c.Routable = c.RuntimeRoutable
		if !c.RuntimeRoutable {
			// Build detailed block reason (2026-06-26 audit fix)
			if providerManualDisabled {
				c.BlockReason = "provider_manual_disabled"
			} else if !isRoutable {
				c.BlockReason = "not_in_routable_view"
			} else if probeState != nil && *probeState == "broken_confirmed" {
				c.BlockReason = "probe_broken_confirmed"
			} else if c.LifecycleStatus != "active" {
				c.BlockReason = "lifecycle_" + c.LifecycleStatus
			} else if c.CircuitState == "open" {
				c.BlockReason = "circuit_open"
			} else if c.QuotaState != "" && c.QuotaState != "ok" {
				c.BlockReason = "quota_" + c.QuotaState
			} else if c.AvailabilityState != "" && c.AvailabilityState != "ready" {
				c.BlockReason = "availability_" + c.AvailabilityState
			} else if !c.Available {
				c.BlockReason = "offer_unavailable"
			} else {
				c.BlockReason = "unknown"
			}
		}
		c.BillingRound = provider.BillingRound(c.BillingMode)
		pc := provider.Candidate{
			ManualPriority:      c.ManualPriority,
			PriceInPer1M:        c.UnitPriceInPer1M,
			PriceOutPer1M:       c.UnitPriceOutPer1M,
			Currency:            c.Currency,
			ConcurrencyLimit:    c.ConcurrencyLimit,
			ActiveSessions:      c.ActiveSessions,
			ConsecutiveFailures: c.ConsecutiveFailures,
			BillingMode:         c.BillingMode,
			Tier:                c.Tier,
			CredentialID:        c.CredentialID,
		}
		c.CompositeScore = routing.CalculateCompositeScore(pc, weights)
		candidates = append(candidates, c)
	}

	toProviderCandidate := func(c candidate) provider.Candidate {
		return provider.Candidate{
			CredentialID:        c.CredentialID,
			Tier:                c.Tier,
			ManualPriority:      c.ManualPriority,
			PriceInPer1M:        c.UnitPriceInPer1M,
			PriceOutPer1M:       c.UnitPriceOutPer1M,
			Currency:            c.Currency,
			ConcurrencyLimit:    c.ConcurrencyLimit,
			ActiveSessions:      c.ActiveSessions,
			ConsecutiveFailures: c.ConsecutiveFailures,
			CompositeScore:      c.CompositeScore,
			BillingMode:         c.BillingMode,
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return routing.CompareCandidatePriority(
			toProviderCandidate(candidates[i]),
			toProviderCandidate(candidates[j]),
		)
	})

	// 2026-06-26: Multi-tier fallback strategy matching provider.Client.loadCandidatesDB
	// Apply quality gate in multiple rounds with progressively relaxed thresholds
	thresholds := []float64{0.3, 0.0}
	var usedThreshold float64
	var routableCandidates []candidate
	
	for _, threshold := range thresholds {
		routableCandidates = nil
		for _, c := range candidates {
			// Apply quality gate: recent success rate threshold
			passesQualityGate := c.RecentSamples == nil || 
				*c.RecentSamples < 20 || 
				c.RecentSuccessRate == nil || 
				*c.RecentSuccessRate >= threshold
			
			if c.RuntimeRoutable && passesQualityGate {
				routableCandidates = append(routableCandidates, c)
			} else if !c.RuntimeRoutable {
				// Already has block_reason from earlier checks
			} else {
				// Blocked by quality gate
				c.BlockReason = fmt.Sprintf("recent_success_rate_low:%.2f%% (threshold:%.0f%%)", 
					*c.RecentSuccessRate*100, threshold*100)
			}
		}
		
		if len(routableCandidates) > 0 {
			usedThreshold = threshold
			break
		}
	}
	
	// Update candidates with quality gate results
	for i := range candidates {
		found := false
		for _, rc := range routableCandidates {
			if candidates[i].CredentialID == rc.CredentialID {
				candidates[i].Routable = true
				found = true
				break
			}
		}
		if !found && candidates[i].RuntimeRoutable {
			// Was routable but filtered by quality gate
			candidates[i].Routable = false
			if candidates[i].BlockReason == "" && candidates[i].RecentSamples != nil && 
				*candidates[i].RecentSamples >= 20 && candidates[i].RecentSuccessRate != nil {
				candidates[i].BlockReason = fmt.Sprintf("recent_success_rate_low:%.2f%%", 
					*candidates[i].RecentSuccessRate*100)
			}
		}
	}

	for i := range candidates {
		candidates[i].Rank = i + 1
	}

	if r.URL.Query().Get("persist_probe") == "1" {
		probes := make([]resolveProbeCandidate, 0, len(candidates))
		for _, c := range candidates {
			probes = append(probes, resolveProbeCandidate{
				ProviderID:   c.ProviderID,
				CredentialID: c.CredentialID,
				ModelName:    c.ModelName,
				Tier:         c.Tier,
				Routable:     c.Routable,
				BlockReason:  c.BlockReason,
			})
		}
		h.persistResolveProbe(ctx, normalizedModel, probes)
	}

	resolutionPath := "direct"
	var qualityGateInfo map[string]any
	if len(candidates) > 0 {
		// 2026-06-19 audit: if the SQL matched a variant that is
		// different from the normalized form, surface ':variant' so
		// admins know a cross-form hit landed.
		if strings.EqualFold(rawModels[0], normalizedModel) {
			resolutionPath = "canonical"
		} else {
			resolutionPath = "canonical:variant"
		}
		
		// 2026-06-26: Report quality gate threshold used
		qualityGateInfo = map[string]any{
			"threshold_used":    usedThreshold,
			"routable_count":    len(routableCandidates),
			"total_count":       len(candidates),
			"fallback_applied":  usedThreshold < 0.3,
		}
	}
	
	response := map[string]any{
		"client_model":    model,
		"canonical_name":  rawModels[0],
		"canonical_id":    nil,
		"resolution_path": resolutionPath,
		"raw_models":      rawModels,
		"plan_order":      []any{},
		"candidates":      candidates,
	}
	if qualityGateInfo != nil {
		response["quality_gate"] = qualityGateInfo
	}
	writeJSON(w, http.StatusOK, response)
}

// isRoutable and blockReason work on the anonymous candidate struct via an
// interface — the struct is defined locally inside handleListCandidates.
// L-3: replaced the stubs that always returned true / "state check".

type routableCandidate interface {
	getAvailable() bool
	getLifecycleStatus() string
	getAvailabilityState() string
	getQuotaState() string
	getCircuitState() string
}

func isRoutable(c interface{}) bool {
	if rc, ok := c.(routableCandidate); ok {
		return rc.getAvailable()
	}
	// Fallback: use struct field reflection via type assertion on concrete type
	type hasAvailable interface{ getAvailable() bool }
	if ha, ok := c.(hasAvailable); ok {
		return ha.getAvailable()
	}
	return true // unknown type, optimistic
}

func blockReason(c interface{}) string {
	type hasFields interface {
		getLifecycleStatus() string
		getCircuitState() string
		getQuotaState() string
		getAvailabilityState() string
	}
	if f, ok := c.(hasFields); ok {
		if f.getLifecycleStatus() != "active" {
			return "lifecycle_" + f.getLifecycleStatus()
		}
		if f.getCircuitState() == "open" {
			return "circuit_open"
		}
		if s := f.getQuotaState(); s != "" && s != "ok" {
			return "quota_" + s
		}
		if s := f.getAvailabilityState(); s != "" && s != "available" {
			return "availability_" + s
		}
	}
	return "unavailable"
}

func (h *Handler) handleRoutingOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var featured []string
	_ = h.db.QueryRow(ctx, `
		SELECT COALESCE(featured_models, ARRAY[]::TEXT[])
		FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
	`).Scan(&featured)
	if featured == nil {
		featured = []string{}
	}

	featuredOnly := queryBool(r, "featured_only")
	sql := `
		SELECT
			mo.raw_model_name AS model_name,
			p.id AS provider_id,
			p.display_name AS provider_name,
			COALESCE(p.catalog_code, '') AS catalog_code,
			p.protocol,
			p.base_url,
			p.enabled AS provider_enabled,
			c.id AS credential_id,
			c.label AS credential_label,
			c.status AS credential_status,
			c.lifecycle_status,
			c.availability_state,
			c.availability_recover_at,
			c.quota_state,
			c.quota_recover_at,
			c.balance_usd::float8,
			c.effective_at,
			c.expires_at,
			c.circuit_state,
			c.cooling_until,
			mo.available,
			COALESCE(mo.routing_tier, 2)::int AS tier,
			COALESCE(mo.weight, 100)::int AS weight,
			mo.unit_price_in_per_1m,
			mo.unit_price_out_per_1m,
			mo.currency,
			COALESCE(mo.success_rate, 0.9)::float8 AS success_rate,
			COALESCE(mo.p95_latency_ms, 9999)::int AS p95_latency_ms,
			mo.standardized_name
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default'
	`
	args := []any{}
	if featuredOnly && len(featured) > 0 {
		sql += ` AND mo.raw_model_name = ANY($1)`
		args = append(args, featured)
	}
	sql += ` ORDER BY mo.raw_model_name, tier ASC, weight DESC, success_rate DESC`

	rows, err := h.db.Query(ctx, sql, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	outRows := make([]map[string]any, 0)
	for rows.Next() {
		var (
			modelName, providerName, catalogCode, protocol, baseURL string
			credentialLabel, credentialStatus, lifecycleStatus      string
			availabilityState, quotaState, circuitState             string
			currency, standardizedName                                *string
			providerID, credentialID, tier, weight, p95               int
			providerEnabled, available                                bool
			successRate, balanceUSD                                   *float64
			availabilityRecoverAt, quotaRecoverAt                     *time.Time
			effectiveAt, expiresAt, coolingUntil                      *time.Time
			priceIn, priceOut                                         *float64
		)
		if err := rows.Scan(
			&modelName, &providerID, &providerName, &catalogCode, &protocol, &baseURL,
			&providerEnabled, &credentialID, &credentialLabel, &credentialStatus,
			&lifecycleStatus, &availabilityState, &availabilityRecoverAt,
			&quotaState, &quotaRecoverAt, &balanceUSD, &effectiveAt, &expiresAt,
			&circuitState, &coolingUntil, &available, &tier, &weight,
			&priceIn, &priceOut, &currency, &successRate, &p95, &standardizedName,
		); err != nil {
			continue
		}
		runtimeRoutable := available &&
			credentialStatus == "active" &&
			lifecycleStatus == "active" &&
			availabilityState == "ready" &&
			quotaState == "ok" &&
			circuitState != "open"
		row := map[string]any{
			"model_name":              modelName,
			"provider_id":             providerID,
			"provider_name":           providerName,
			"catalog_code":            catalogCode,
			"protocol":                protocol,
			"base_url":                baseURL,
			"provider_enabled":        providerEnabled,
			"credential_id":           credentialID,
			"credential_label":        credentialLabel,
			"credential_status":       credentialStatus,
			"lifecycle_status":        lifecycleStatus,
			"availability_state":      availabilityState,
			"availability_recover_at": availabilityRecoverAt,
			"quota_state":             quotaState,
			"quota_recover_at":        quotaRecoverAt,
			"balance_usd":             balanceUSD,
			"effective_at":            effectiveAt,
			"expires_at":              expiresAt,
			"circuit_state":           circuitState,
			"cooling_until":           coolingUntil,
			"available":               available,
			"tier":                    tier,
			"weight":                  weight,
			"unit_price_in_per_1m":    priceIn,
			"unit_price_out_per_1m":   priceOut,
			"currency":                currency,
			"success_rate":            successRate,
			"p95_latency_ms":          p95,
			"standardized_name":       standardizedName,
			"runtime_routable":        runtimeRoutable,
			"routable":                runtimeRoutable,
		}
		if !runtimeRoutable {
			row["runtime_block_reason"] = routingBlockReason(
				available, credentialStatus, lifecycleStatus, availabilityState, quotaState, circuitState,
			)
		}
		outRows = append(outRows, row)
	}
	if outRows == nil {
		outRows = []map[string]any{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"featured": featured,
		"rows":     outRows,
	})
}

func routingBlockReason(available bool, credStatus, lifecycle, availability, quota, circuit string) string {
	if !available {
		return "offer_unavailable"
	}
	if credStatus != "active" {
		return "credential_" + credStatus
	}
	if lifecycle != "active" {
		return "lifecycle_" + lifecycle
	}
	if availability != "ready" {
		return "availability_" + availability
	}
	if quota != "ok" {
		return "quota_" + quota
	}
	if circuit == "open" {
		return "circuit_open"
	}
	return "unknown"
}

func (h *Handler) handleRoutingModelTree(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	featuredOnly := queryBool(r, "featured_only")

	// For tenant_admin, hide credential routing details (which credentials route to which model).
	// They see model names and availability, but NOT credential labels, provider IDs, pricing, etc.
	hideCredentialDetails := IsTenantAdmin(r)

	var featuredModels []string
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `SELECT COALESCE(featured_models, ARRAY[]::TEXT[]) FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1`).Scan(&featuredModels)

	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(mc.canonical_name, mo.raw_model_name) AS canonical_name,
			COALESCE(mc.family, 'unknown') AS family,
			mo.raw_model_name,
			p.id AS provider_id, p.display_name AS provider_name,
			c.id AS credential_id, c.label AS credential_label,
			COALESCE(c.status, 'unknown') AS credential_status,
			COALESCE(c.availability_state, 'ready') AS availability_state,
			mo.available,
			COALESCE(mo.routing_tier, 2) AS tier,
			COALESCE(mo.weight, 100) AS weight,
			mo.unit_price_in_per_1m, mo.unit_price_out_per_1m, mo.currency,
			COALESCE(mo.success_rate, 0.9)::float8 AS success_rate,
			COALESCE(mo.p95_latency_ms, 9999)::int AS p95_latency_ms
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		WHERE p.tenant_id = 'default'
		  AND COALESCE(mc.status, 'active') = 'active'
		ORDER BY canonical_name, tier ASC, weight DESC, success_rate DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type credentialEntry struct {
		CredentialID     int      `json:"credential_id"`
		CredentialLabel  string   `json:"credential_label"`
		CredentialStatus string   `json:"credential_status"`
		ProviderID       int      `json:"provider_id"`
		ProviderName     string   `json:"provider_name"`
		Available        bool     `json:"available"`
		Tier             int      `json:"tier"`
		Weight           int      `json:"weight"`
		PriceInPer1M     *float64 `json:"unit_price_in_per_1m"`
		PriceOutPer1M    *float64 `json:"unit_price_out_per_1m"`
		Currency         *string  `json:"currency"`
		SuccessRate      float64  `json:"success_rate"`
		P95LatencyMs     int      `json:"p95_latency_ms"`
		Routable         bool     `json:"runtime_routable"`
		BlockReason      string   `json:"runtime_block_reason,omitempty"`
	}

	type variantEntry struct {
		Variant       string            `json:"variant"`
		CanonicalName string            `json:"canonical_name"`
		Tags          []string          `json:"tags"`
		Credentials   []credentialEntry `json:"credentials"`
	}

	type generationEntry struct {
		Generation string         `json:"generation"`
		Variants   []variantEntry `json:"variants"`
	}

	type seriesEntry struct {
		Series      string            `json:"series"`
		Generations []generationEntry `json:"generations"`
	}

	// Build tree: series -> generation -> variant -> credentials
	seriesMap := map[string]*seriesEntry{}
	var unmapped []map[string]any

	for rows.Next() {
		var canonicalName, family, rawName string
		var provID, credID, tier, weight, p95 int
		var provName, credLabel, credStatus, availState string
		var available bool
		var priceIn, priceOut *float64
		var currency *string
		var successRate float64

		if err := rows.Scan(&canonicalName, &family, &rawName,
			&provID, &provName, &credID, &credLabel, &credStatus, &availState,
			&available, &tier, &weight, &priceIn, &priceOut, &currency,
			&successRate, &p95); err != nil {
			continue
		}

		tags := []string{}

		series := family
		generation := "default"
		variant := canonicalName

		// Check if routable
		routable := available && credStatus == "active"
		blockReason := ""
		if !available {
			blockReason = "offer_unavailable"
		} else if credStatus != "active" {
			blockReason = "credential_" + credStatus
		}

		cred := credentialEntry{
			CredentialID:     credID,
			CredentialLabel:  credLabel,
			CredentialStatus: credStatus,
			ProviderID:       provID,
			ProviderName:     provName,
			Available:        available,
			Tier:             tier,
			Weight:           weight,
			PriceInPer1M:     priceIn,
			PriceOutPer1M:    priceOut,
			Currency:         currency,
			SuccessRate:      successRate,
			P95LatencyMs:     p95,
			Routable:         routable,
			BlockReason:      blockReason,
		}

		// Featured filter
		if featuredOnly && len(featuredModels) > 0 {
			found := false
			for _, fm := range featuredModels {
				if fm == rawName || fm == canonicalName {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}

		// Build tree
		if _, ok := seriesMap[series]; !ok {
			seriesMap[series] = &seriesEntry{Series: series, Generations: []generationEntry{}}
		}
		se := seriesMap[series]

		var ge *generationEntry
		for i := range se.Generations {
			if se.Generations[i].Generation == generation {
				ge = &se.Generations[i]
				break
			}
		}
		if ge == nil {
			se.Generations = append(se.Generations, generationEntry{Generation: generation, Variants: []variantEntry{}})
			ge = &se.Generations[len(se.Generations)-1]
		}

		var ve *variantEntry
		for i := range ge.Variants {
			if ge.Variants[i].Variant == variant {
				ve = &ge.Variants[i]
				break
			}
		}
		if ve == nil {
			ge.Variants = append(ge.Variants, variantEntry{
				Variant:       variant,
				CanonicalName: canonicalName,
				Tags:          tags,
				Credentials:   []credentialEntry{},
			})
			ve = &ge.Variants[len(ge.Variants)-1]
		}
		ve.Credentials = append(ve.Credentials, cred)
	}

	seriesList := make([]seriesEntry, 0, len(seriesMap))
	for _, se := range seriesMap {
		seriesList = append(seriesList, *se)
	}

	if featuredModels == nil {
		featuredModels = []string{}
	}
	if unmapped == nil {
		unmapped = []map[string]any{}
	}

	// For tenant_admin, strip credential routing details to prevent data leakage.
	// They see which models are available, but not which credentials route to them.
	if hideCredentialDetails {
		type simpleVariant struct {
			Variant       string   `json:"variant"`
			CanonicalName string   `json:"canonical_name"`
			Tags          []string `json:"tags"`
			Available     bool     `json:"available"`
			CreditCount   int      `json:"credential_count"`
		}
		type simpleGeneration struct {
			Generation string          `json:"generation"`
			Variants   []simpleVariant `json:"variants"`
		}
		type simpleSeries struct {
			Series      string           `json:"series"`
			Generations []simpleGeneration `json:"generations"`
		}
		simpleList := make([]simpleSeries, 0, len(seriesList))
		for _, se := range seriesList {
			ss := simpleSeries{Series: se.Series, Generations: []simpleGeneration{}}
			for _, ge := range se.Generations {
				sg := simpleGeneration{Generation: ge.Generation, Variants: []simpleVariant{}}
				for _, ve := range ge.Variants {
					if len(ve.Credentials) == 0 {
						continue
					}
					allAvailable := true
					for _, c := range ve.Credentials {
						if !c.Available {
							allAvailable = false
							break
						}
					}
					sg.Variants = append(sg.Variants, simpleVariant{
						Variant:       ve.Variant,
						CanonicalName: ve.CanonicalName,
						Tags:          ve.Tags,
						Available:     allAvailable,
						CreditCount:   len(ve.Credentials),
					})
				}
				if len(sg.Variants) > 0 {
					ss.Generations = append(ss.Generations, sg)
				}
			}
			if len(ss.Generations) > 0 {
				simpleList = append(simpleList, ss)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"featured": featuredModels,
			"series":   simpleList,
			"unmapped": unmapped,
			"readonly": true, // hint to frontend that credential details are hidden
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"featured": featuredModels,
		"series":   seriesList,
		"unmapped": unmapped,
	})
}

func (h *Handler) handleRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		row := h.db.QueryRow(ctx, `
			SELECT row_to_json(rp)::text
			FROM routing_policy rp
			WHERE tenant_id = 'default'
			ORDER BY id LIMIT 1
		`)
		var raw string
		if err := row.Scan(&raw); err != nil || raw == "" {
			writeJSON(w, http.StatusOK, map[string]any{})
			return
		}
		var pol map[string]any
		if err := json.Unmarshal([]byte(raw), &pol); err != nil {
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		writeJSON(w, http.StatusOK, pol)
		return
	}

	if r.Method == http.MethodPatch {
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		// Use a static parameterised UPDATE with COALESCE so that only
		// explicitly-listed columns can ever be touched — no dynamic SQL
		// construction that could become an injection vector if fields were
		// ever expanded carelessly. "actor" is intentionally excluded: it is
		// an audit identity field, not a configuration value.
		if _, err := h.db.Exec(ctx, `
			UPDATE routing_policy SET
				algorithm_version         = COALESCE($1, algorithm_version),
				retry_per_credential      = COALESCE($2, retry_per_credential),
				tier_fallback_max         = COALESCE($3, tier_fallback_max),
				circuit_open_seconds      = COALESCE($4, circuit_open_seconds),
				circuit_failure_threshold = COALESCE($5, circuit_failure_threshold),
				circuit_max_open_seconds  = COALESCE($6, circuit_max_open_seconds)
			WHERE tenant_id = 'default'
		`,
			patch["algorithm_version"],
			patch["retry_per_credential"],
			patch["tier_fallback_max"],
			patch["circuit_open_seconds"],
			patch["circuit_failure_threshold"],
			patch["circuit_max_open_seconds"],
		); err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func (h *Handler) handleRoutingFeatured(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		var models []string
		err := h.db.QueryRow(ctx, `
			SELECT COALESCE(featured_models, ARRAY[]::TEXT[])
			FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
		`).Scan(&models)
		if err != nil {
			slog.Warn("routing featured: query failed", "error", err.Error())
			models = []string{}
		}
		if models == nil {
			models = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"featured_models": models})
		return
	}

	if r.Method == http.MethodPatch {
		var patch struct {
			FeaturedModels []string `json:"featured_models"`
			Actor          string   `json:"actor"`
		}
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		_, err := h.db.Exec(ctx, `
			UPDATE routing_policy SET featured_models = $1 WHERE tenant_id = 'default'
		`, patch.FeaturedModels)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		// Bust the available-models cache so the new featured list
		// shows up in the next /api/routing/available-models read.
		InvalidateAvailableModelsCache()
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

type availableVersionEntry struct {
	CanonicalName string   `json:"canonical_name"`
	DisplayName   string   `json:"display_name"`
	Modality      string   `json:"modality"`
	ContextWindow *int     `json:"context_window"`
	ParametersB   *float64 `json:"parameters_b"`
	Aliases       []string `json:"aliases"`
	RawNames      []string `json:"raw_names"`
	ProviderCount int      `json:"provider_count"`
	Featured      bool     `json:"featured"`
	Tags          []string `json:"tags"`
}

type availableFamilyEntry struct {
	ID          string                  `json:"id"`
	DisplayName string                  `json:"display_name"`
	Vendor      string                  `json:"vendor"`
	Versions    []availableVersionEntry `json:"versions"`
}

type popularModelEntry struct {
	CanonicalName string `json:"canonical_name"`
	DisplayName   string `json:"display_name"`
	Source        string `json:"source"`
	Count         *int   `json:"count,omitempty"`
}

func (h *Handler) queryPopularModels(ctx context.Context, featuredModels []string, byCanonical map[string]*availableVersionEntry) []popularModelEntry {
	popular := make([]popularModelEntry, 0, 20)
	seen := map[string]bool{}

	add := func(canonical, display, source string, count *int) {
		key := strings.ToLower(strings.TrimSpace(canonical))
		if key == "" || seen[key] {
			return
		}
		if v, ok := byCanonical[key]; ok {
			canonical = v.CanonicalName
			if v.DisplayName != "" {
				display = v.DisplayName
			}
		}
		if display == "" {
			display = canonical
		}
		popular = append(popular, popularModelEntry{
			CanonicalName: canonical,
			DisplayName:   display,
			Source:        source,
			Count:         count,
		})
		seen[key] = true
	}

	for _, name := range featuredModels {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		add(n, n, "policy", nil)
	}

	usageRows, err := h.db.Query(ctx, `
		SELECT model_key, cnt FROM (
			SELECT
				COALESCE(mc.canonical_name, mc2.canonical_name, rl.client_model) AS model_key,
				COUNT(*) AS cnt
			FROM request_logs rl
			LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
			LEFT JOIN LATERAL (
				SELECT canonical_id
				FROM model_aliases
				WHERE lower(raw_name) = lower(rl.client_model)
				  AND status = 'active'
				LIMIT 1
			) ma ON TRUE
			LEFT JOIN models_canonical mc2 ON mc2.id = ma.canonical_id
			WHERE rl.ts > NOW() - INTERVAL '7 days'
			  AND rl.success = TRUE
			  AND rl.client_model IS NOT NULL AND rl.client_model != ''
			GROUP BY model_key
		) u
		ORDER BY cnt DESC
		LIMIT 10
	`)
	if err == nil {
		defer usageRows.Close()
		for usageRows.Next() {
			var modelKey string
			var cnt int
			if err := usageRows.Scan(&modelKey, &cnt); err != nil {
				continue
			}
			c := cnt
			add(modelKey, modelKey, "usage", &c)
		}
	}
	return popular
}

func (h *Handler) handleRoutingAvailableModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if h.db == nil {
		writeError(w, http.StatusServiceUnavailable, "database not configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 2026-06-19 audit: this endpoint runs 4 round-trips per call
	// (model_offers + LATERAL aliases, routing_policy, model_aliases
	// ARRAY_AGG, plus the popular-usage 7d scan).  Cache for 30s to
	// absorb the spike when /providers/33, /providers/34 etc. all
	// hit it during a single admin page render.  Busting on featured
	// policy edit / provider refresh is handled in those paths
	// (call invalidateAvailableModelsCache below).
	if r.Method == http.MethodGet {
		if cached, ok := globalAvailableModelsCache.get(time.Now()); ok {
			writeJSON(w, http.StatusOK, cached)
			return
		}
	}

	rows, err := h.db.Query(ctx, `
		SELECT mo.raw_model_name,
		       mo.standardized_name,
		       COUNT(DISTINCT p.id) AS provider_count,
		       mc.canonical_name,
		       mc.display_name,
		       mc.family,
		       mc.modality,
		       mc.context_window,
		       mc.parameters_b,
		       COALESCE(mf.display_name, mc.family, 'Unclassified') AS family_display_name,
		       mf.vendor AS family_vendor
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN LATERAL (
			SELECT canonical_id
			FROM model_aliases
			WHERE lower(raw_name) = lower(mo.raw_model_name)
			  AND status = 'active'
			LIMIT 1
		) ma ON TRUE
		LEFT JOIN models_canonical mc ON mc.id = COALESCE(mo.canonical_id, ma.canonical_id)
		LEFT JOIN model_families mf ON mf.id = mc.family AND mf.status = 'active'
		WHERE p.tenant_id = 'default'
		  AND mo.available = TRUE
		  AND c.status = 'active'
		  AND p.enabled = TRUE
		  AND COALESCE(mc.status, 'active') = 'active'
		GROUP BY mo.raw_model_name, mo.standardized_name, mc.canonical_name, mc.display_name,
		         mc.family, mc.modality, mc.context_window, mc.parameters_b,
		         mf.display_name, mf.vendor
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	featuredSet := map[string]bool{}
	polRow := h.db.QueryRow(ctx, `SELECT featured_models FROM routing_policy WHERE tenant_id = 'default'`)
	var featuredModels []string
	if err := polRow.Scan(&featuredModels); err == nil {
		for _, f := range featuredModels {
			featuredSet[strings.ToLower(strings.TrimSpace(f))] = true
		}
	}

	aliasesByCanonical := map[string][]string{}
	aliasRows, err := h.db.Query(ctx, `
		SELECT mc.canonical_name, ARRAY_AGG(ma.raw_name ORDER BY ma.raw_name) AS aliases
		FROM models_canonical mc
		JOIN model_aliases ma ON ma.canonical_id = mc.id
		WHERE mc.status = 'active' AND ma.status = 'active'
		GROUP BY mc.canonical_name
	`)
	if err == nil {
		defer aliasRows.Close()
		for aliasRows.Next() {
			var canonical string
			var aliases []string
			if err := aliasRows.Scan(&canonical, &aliases); err == nil {
				aliasesByCanonical[canonical] = aliases
			}
		}
	}

	byCanonical := map[string]*availableVersionEntry{}
	canonFamily := map[string]string{}
	familyMeta := map[string]availableFamilyEntry{}
	unmapped := make([]string, 0)
	totalRaw := 0

	for rows.Next() {
		totalRaw++
		var rawName string
		var stdName, canonName, dispName, family, modality, familyDisplay, familyVendor *string
		var ctxWin *int
		var paramsB *float64
		var provCount int
		if err := rows.Scan(&rawName, &stdName, &provCount, &canonName, &dispName, &family, &modality,
			&ctxWin, &paramsB, &familyDisplay, &familyVendor); err != nil {
			continue
		}
		if canonName == nil || strings.TrimSpace(*canonName) == "" {
			unmapped = append(unmapped, rawName)
			continue
		}
		cn := strings.TrimSpace(*canonName)
		canonKey := strings.ToLower(cn)
		item, ok := byCanonical[canonKey]
		if !ok {
			dn := cn
			if dispName != nil && strings.TrimSpace(*dispName) != "" {
				dn = strings.TrimSpace(*dispName)
			}
			md := "text"
			if modality != nil && strings.TrimSpace(*modality) != "" {
				md = strings.TrimSpace(*modality)
			}
			item = &availableVersionEntry{
				CanonicalName: cn,
				DisplayName:   dn,
				Modality:      md,
				ContextWindow: ctxWin,
				ParametersB:   paramsB,
				Aliases:       aliasesByCanonical[cn],
				RawNames:      []string{},
				ProviderCount: 0,
				Featured:      featuredSet[canonKey] || featuredSet[strings.ToLower(rawName)],
				Tags:          []string{},
			}
			byCanonical[canonKey] = item

			familyID := "unclassified"
			familyLabel := "未分类"
			vendor := "其他"
			if family != nil && strings.TrimSpace(*family) != "" {
				familyID = strings.TrimSpace(*family)
			}
			if familyDisplay != nil && strings.TrimSpace(*familyDisplay) != "" {
				familyLabel = strings.TrimSpace(*familyDisplay)
			}
			if familyVendor != nil && strings.TrimSpace(*familyVendor) != "" {
				vendor = strings.TrimSpace(*familyVendor)
			}
			canonFamily[canonKey] = familyID
			if _, exists := familyMeta[familyID]; !exists {
				familyMeta[familyID] = availableFamilyEntry{
					ID:          familyID,
					DisplayName: familyLabel,
					Vendor:      vendor,
				}
			}
		}
		item.RawNames = append(item.RawNames, rawName)
		item.ProviderCount += provCount
		if featuredSet[canonKey] || featuredSet[strings.ToLower(rawName)] {
			item.Featured = true
		}
	}

	familyVersions := map[string][]availableVersionEntry{}
	for canonKey, item := range byCanonical {
		familyID := canonFamily[canonKey]
		if familyID == "" {
			familyID = "unclassified"
		}
		familyVersions[familyID] = append(familyVersions[familyID], *item)
	}
	familiesOut := make([]availableFamilyEntry, 0, len(familyMeta))
	for familyID, meta := range familyMeta {
		versions := familyVersions[familyID]
		sort.Slice(versions, func(i, j int) bool {
			return versions[i].CanonicalName < versions[j].CanonicalName
		})
		familiesOut = append(familiesOut, availableFamilyEntry{
			ID:          meta.ID,
			DisplayName: meta.DisplayName,
			Vendor:      meta.Vendor,
			Versions:    versions,
		})
	}
	sort.Slice(familiesOut, func(i, j int) bool {
		if familiesOut[i].Vendor == familiesOut[j].Vendor {
			return familiesOut[i].DisplayName < familiesOut[j].DisplayName
		}
		return familiesOut[i].Vendor < familiesOut[j].Vendor
	})
	sort.Strings(unmapped)

	popular := h.queryPopularModels(ctx, featuredModels, byCanonical)

	resp := map[string]any{
		"families":  familiesOut,
		"popular":   popular,
		"unmapped":  unmapped,
		"total_raw": totalRaw,
	}
	if r.Method == http.MethodGet {
		globalAvailableModelsCache.set(time.Now(), resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

// invalidateAvailableModelsCache is called by paths that mutate the
// data this endpoint aggregates: featured-model policy edit, provider
// refresh, and the model_offer standardized_name PATCH in admin/providers.go.
// Keep this exported so the post-deploy hot-paths above can simply call
// admin.InvalidateAvailableModelsCache() without importing the cache.
func InvalidateAvailableModelsCache() {
	globalAvailableModelsCache.mu.Lock()
	globalAvailableModelsCache.value = nil
	globalAvailableModelsCache.expiresAt = time.Time{}
	globalAvailableModelsCache.mu.Unlock()
}

// 2026-06-19 audit: cache unit-test hooks — these let regression tests
// (admin/routing_cache_test.go) exercise the hit/miss/invalidate
// lifecycle without spinning up a real DB.
func setAvailableModelsCacheForTest(value map[string]any) {
	globalAvailableModelsCache.set(time.Now(), value)
}

func getAvailableModelsCacheForTest() (map[string]any, bool) {
	return globalAvailableModelsCache.get(time.Now())
}

func (h *Handler) handleRoutingAvailableModelsRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT mo.raw_model_name
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE mo.available = TRUE AND c.status = 'active' AND p.enabled = TRUE
		ORDER BY mo.raw_model_name
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			continue
		}
		names = append(names, name)
	}
	writeJSON(w, http.StatusOK, names)
}

func (h *Handler) handleRoutingDecisions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	sinceMin := queryInt(r, "since_minutes", 30)
	limit := queryInt(r, "limit", 100)
	if limit > 500 {
		limit = 500
	}
	offset := queryInt(r, "offset", 0)
	if offset < 0 {
		offset = 0
	}
	model := strings.TrimSpace(queryString(r, "model"))
	canonical := strings.TrimSpace(queryString(r, "canonical"))

	clauses := []string{"rdl.ts >= NOW() - make_interval(mins => $1)"}
	args := []any{sinceMin}
	argIdx := 2

	// tenant_admin callers may only see decisions for their own tenant.
	if IsTenantAdmin(r) {
		clauses = append(clauses, fmt.Sprintf("rdl.tenant_id = $%d", argIdx))
		args = append(args, GetTenantID(r))
		argIdx++
	}

	if model != "" {
		clauses = append(clauses, fmt.Sprintf("(rdl.model ILIKE $%d OR rdl.client_model ILIKE $%d OR rdl.outbound_model ILIKE $%d)", argIdx, argIdx, argIdx))
		args = append(args, "%"+model+"%")
		argIdx++
	}
	if canonical != "" {
		clauses = append(clauses, fmt.Sprintf("rdl.canonical_model ILIKE $%d", argIdx))
		args = append(args, "%"+canonical+"%")
		argIdx++
	}
	if v := queryOptionalBool(r, "success"); v != nil {
		clauses = append(clauses, fmt.Sprintf("rdl.success = $%d", argIdx))
		args = append(args, *v)
		argIdx++
	}
	where := strings.Join(clauses, " AND ")

	var total int
	countQ := fmt.Sprintf("SELECT COUNT(*) FROM routing_decision_log rdl WHERE %s", where)
	if err := h.db.QueryRow(ctx, countQ, args...).Scan(&total); err != nil {
		total = 0
	}

	args = append(args, limit, offset)

	q := fmt.Sprintf(`
		SELECT rdl.ts, rdl.request_id::text AS request_id, rdl.idempotency_key,
		       rdl.tenant_id, rdl.api_key_id, rdl.model,
		       rdl.chosen_credential_id, rdl.chosen_provider_id, rdl.tier,
		       rdl.candidates_tried, rdl.latency_ms, rdl.success, rdl.error_class,
		       rdl.prompt_tokens, rdl.completion_tokens,
		       COALESCE(rdl.cost_usd, 0)::float8 AS cost_usd,
		       rdl.request_bytes, rdl.response_bytes,
		       rdl.client_model, rdl.resolved_raw_model, rdl.outbound_model,
		       rdl.sticky_hit, rdl.client_profile, rdl.request_mode,
		       rdl.identity_hash, rdl.transform_rule_id, rdl.egress_protocol,
		       rdl.failure_stage, rdl.failure_detail_code,
		       rdl.resolution_path, rdl.canonical_model,
		       COALESCE(rdl.resolution_raw_models, '[]'::jsonb) AS resolution_raw_models,
		       COALESCE(rdl.decision_trace, '{}'::jsonb) AS decision_trace
		FROM routing_decision_log rdl
		WHERE %s
		ORDER BY rdl.ts DESC
		LIMIT $%d OFFSET $%d
	`, where, argIdx, argIdx+1)

	rows, err := h.db.Query(ctx, q, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	decisions := make([]map[string]any, 0)
	for rows.Next() {
		var ts time.Time
		var reqID, mdl string
		var idempotencyKey, tenantID, clientModel, resolvedRawModel, outboundModel *string
		var clientProfile, requestMode, identityHash, transformRuleID, egressProtocol *string
		var failureStage, failureDetailCode, resolutionPath, canonicalModel, errorClass *string
		var apiKeyID, credID, provID, tier, tried, latency, pTok, cTok, reqBytes, respBytes *int
		var success bool
		var cost float64
		var stickyHit *bool
		var resolutionRawModels, decisionTrace []byte
		if err := rows.Scan(
			&ts, &reqID, &idempotencyKey, &tenantID, &apiKeyID, &mdl,
			&credID, &provID, &tier, &tried, &latency, &success, &errorClass,
			&pTok, &cTok, &cost, &reqBytes, &respBytes,
			&clientModel, &resolvedRawModel, &outboundModel,
			&stickyHit, &clientProfile, &requestMode,
			&identityHash, &transformRuleID, &egressProtocol,
			&failureStage, &failureDetailCode, &resolutionPath, &canonicalModel,
			&resolutionRawModels, &decisionTrace,
		); err != nil {
			continue
		}
		var rawModels any = []string{}
		if len(resolutionRawModels) > 0 {
			_ = json.Unmarshal(resolutionRawModels, &rawModels)
		}
		var trace any = map[string]any{}
		if len(decisionTrace) > 0 {
			_ = json.Unmarshal(decisionTrace, &trace)
		}
		decisions = append(decisions, map[string]any{
			"ts":                     ts,
			"request_id":             reqID,
			"idempotency_key":        idempotencyKey,
			"tenant_id":              tenantID,
			"api_key_id":             apiKeyID,
			"model":                  mdl,
			"chosen_credential_id":   credID,
			"chosen_provider_id":     provID,
			"tier":                   tier,
			"candidates_tried":       tried,
			"latency_ms":             latency,
			"success":                success,
			"error_class":            errorClass,
			"prompt_tokens":          pTok,
			"completion_tokens":      cTok,
			"cost_usd":               cost,
			"request_bytes":          reqBytes,
			"response_bytes":         respBytes,
			"client_model":           clientModel,
			"resolved_raw_model":     resolvedRawModel,
			"outbound_model":         outboundModel,
			"sticky_hit":             stickyHit,
			"client_profile":         clientProfile,
			"request_mode":           requestMode,
			"identity_hash":          identityHash,
			"transform_rule_id":      transformRuleID,
			"egress_protocol":        egressProtocol,
			"failure_stage":          failureStage,
			"failure_detail_code":    failureDetailCode,
			"resolution_path":        resolutionPath,
			"canonical_model":        canonicalModel,
			"resolution_raw_models":  rawModels,
			"decision_trace":         trace,
		})
	}
	if decisions == nil {
		decisions = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":     total,
		"offset":    offset,
		"limit":     limit,
		"decisions": decisions,
	})
}

func (h *Handler) handleRoutingHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.label, c.status,
		       COALESCE(c.circuit_state, 'closed'),
		       COALESCE(c.consecutive_failures, 0),
		       COALESCE(c.circuit_open_count_window, 0),
		       c.cooling_until,
		       p.display_name, p.catalog_code
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default'
		ORDER BY (c.circuit_state = 'open') DESC, c.consecutive_failures DESC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type credHealth struct {
		CredentialID            int        `json:"credential_id"`
		Label                   string     `json:"label"`
		Status                  string     `json:"status"`
		CircuitState            string     `json:"circuit_state"`
		ConsecutiveFailures     int        `json:"consecutive_failures"`
		CircuitOpenCountWindow  int        `json:"circuit_open_count_window"`
		CoolingUntil            *time.Time `json:"cooling_until"`
		ProviderName            string     `json:"provider_name"`
		CatalogCode             *string    `json:"catalog_code"`
	}
	creds := make([]credHealth, 0)
	openCount := 0
	for rows.Next() {
		var c credHealth
		if err := rows.Scan(&c.CredentialID, &c.Label, &c.Status,
			&c.CircuitState, &c.ConsecutiveFailures, &c.CircuitOpenCountWindow,
			&c.CoolingUntil, &c.ProviderName, &c.CatalogCode); err != nil {
			continue
		}
		if c.CircuitState == "open" {
			openCount++
		}
		creds = append(creds, c)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"credentials": creds,
		"summary": map[string]any{
			"total":  len(creds),
			"open":   openCount,
			"closed": len(creds) - openCount,
		},
	})
}

func (h *Handler) handleRoutingAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	limit := queryInt(r, "limit", 50)
	if limit > 500 {
		limit = 500
	}

	rows, err := h.db.Query(ctx, `
		SELECT id, ts, COALESCE(actor,''), COALESCE(action,''),
		       target_type, target_id, before_json, after_json
		FROM routing_audit_log
		ORDER BY ts DESC LIMIT $1
	`, limit)
	if err != nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	defer rows.Close()

	audits := make([]map[string]any, 0)
	for rows.Next() {
		var id int64
		var ts time.Time
		var actor, action string
		var targetType *string
		var targetID *int64
		var beforeJSON, afterJSON []byte
		if err := rows.Scan(&id, &ts, &actor, &action, &targetType, &targetID, &beforeJSON, &afterJSON); err != nil {
			continue
		}
		var before, after any
		if len(beforeJSON) > 0 {
			_ = json.Unmarshal(beforeJSON, &before)
		}
		if len(afterJSON) > 0 {
			_ = json.Unmarshal(afterJSON, &after)
		}
		audits = append(audits, map[string]any{
			"id":          id,
			"ts":          ts,
			"actor":       actor,
			"action":      action,
			"target_type": targetType,
			"target_id":   targetID,
			"before_json": before,
			"after_json":  after,
		})
	}
	if audits == nil {
		audits = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, audits)
}

func (h *Handler) handleRoutingProbe(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Model         string           `json:"model"`
		Messages      []map[string]any `json:"messages"`
		MaxTokens     int              `json:"max_tokens"`
		ClientProfile *string          `json:"client_profile"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "model required")
		return
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = 20
	}
	if len(req.Messages) == 0 {
		req.Messages = []map[string]any{{"role": "user", "content": "Hello, please reply with one word: OK"}}
	}
	clientProfile := "roocode"
	if req.ClientProfile != nil {
		clientProfile = *req.ClientProfile
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT c.id, c.provider_id, p.base_url, COALESCE(p.protocol,'openai'),
		       c.secret_ciphertext, COALESCE(mo.raw_model_name, $1), COALESCE(mo.outbound_model_name, $1)
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id AND c.status = 'active'
		JOIN providers p ON p.id = c.provider_id AND p.enabled = TRUE
		WHERE mo.available = TRUE
		  AND lower(mo.raw_model_name) = lower($1)
		  AND COALESCE(c.lifecycle_status,'active') = 'active'
		  AND COALESCE(c.availability_state,'ready') = 'ready'
		ORDER BY mo.manual_priority NULLS LAST,
		         CASE p.category
		             WHEN 'official' THEN 1
		             WHEN 'official_proxy' THEN 2
		             WHEN 'self_host' THEN 3
		             WHEN 'aggregator' THEN 4
		             WHEN 'third_party_relay' THEN 5
		             ELSE 9
		         END,
		         p.id, c.id
		LIMIT 1
	`, req.Model)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "no available provider for model "+req.Model)
		return
	}
	defer rows.Close()
	if !rows.Next() {
		writeError(w, http.StatusServiceUnavailable, "no available provider for model "+req.Model)
		return
	}

	var credID, provID int
	var baseURL, protocol, rawModel, outModel string
	var ciphertext []byte
	if err := rows.Scan(&credID, &provID, &baseURL, &protocol, &ciphertext, &rawModel, &outModel); err != nil {
		writeError(w, http.StatusInternalServerError, "probe: scan failed")
		return
	}

	// Decrypt credential key. Some free-pool credentials have NULL ciphertext
	// (no API key required); in that case proceed with an empty key.
	var apiKey string
	if len(ciphertext) > 0 {
		var decErr error
		apiKey, decErr = h.decryptCredStr(string(ciphertext))
		if decErr != nil {
			writeError(w, http.StatusInternalServerError, "failed to decrypt credential")
			return
		}
	}

	var provName string
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `SELECT COALESCE(display_name,'') FROM providers WHERE id = $1`, provID).Scan(&provName)

	// ── Actually probe the provider with a lightweight request ───────────
	// Send a minimal completion request to verify the key works and the
	// provider is reachable. Use a short-lived context so the probe doesn't
	// hang on slow/dead endpoints.
	probeCtx, probeCancel := context.WithTimeout(ctx, 15*time.Second)
	defer probeCancel()

	probeBody, _ := json.Marshal(map[string]any{
		"model":      outModel,
		"messages":   req.Messages,
		"max_tokens": req.MaxTokens,
		"stream":     false,
	})
	probeURL := upstreamurl.ChatCompletionsURL(baseURL)
	probeReq, _ := http.NewRequestWithContext(probeCtx, http.MethodPost, probeURL, strings.NewReader(string(probeBody)))
	probeReq.Header.Set("Content-Type", "application/json")
	probeReq.Header.Set("Authorization", "Bearer "+apiKey)

	probeResp, probeErr := http.DefaultClient.Do(probeReq)
	var probeOK bool
	var probeStatus int
	var probeMsg string
	if probeErr != nil {
		probeMsg = fmt.Sprintf("probe request failed: %v", probeErr)
	} else {
		//nolint:errcheck // best-effort close
		defer probeResp.Body.Close()
		probeStatus = probeResp.StatusCode
		probeOK = probeStatus >= 200 && probeStatus < 300
		if !probeOK {
			respBody, _ := io.ReadAll(io.LimitReader(probeResp.Body, 2048))
			probeMsg = fmt.Sprintf("probe returned HTTP %d: %s", probeStatus, string(respBody))
		} else {
			probeMsg = "probe succeeded"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":        probeOK,
		"provider_id":    provID,
		"provider_name":  provName,
		"credential_id":  credID,
		"model":          req.Model,
		"raw_model":      rawModel,
		"outbound_model": outModel,
		"client_profile": clientProfile,
		"probe_status":   probeStatus,
		"message":        probeMsg,
	})
}

func (h *Handler) handleRoutingManualPriority(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		CredentialID   int    `json:"credential_id"`
		ModelName      string `json:"model_name"`
		ManualPriority int    `json:"manual_priority"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ManualPriority < 1 || req.ManualPriority > 99 {
		writeError(w, http.StatusBadRequest, "manual_priority must be between 1 and 99")
		return
	}
	if req.CredentialID == 0 || req.ModelName == "" {
		writeError(w, http.StatusBadRequest, "credential_id and model_name required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	tag, err := h.db.Exec(ctx, `
		UPDATE model_offers SET manual_priority = $1
		WHERE credential_id = $2 AND lower(raw_model_name) = lower($3)
	`, req.ManualPriority, req.CredentialID, req.ModelName)
	if err != nil {
		slog.Error("manual-priority update failed", "error", err, "cred_id", req.CredentialID, "model", req.ModelName)
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "model offer not found")
		return
	}

	h.logAudit(r, "manual_priority_update", map[string]any{
		"credential_id":   req.CredentialID,
		"model_name":      req.ModelName,
		"manual_priority": req.ManualPriority,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) handleRoutingScoreDetails(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	model := queryString(r, "model")
	if model == "" {
		writeError(w, http.StatusBadRequest, "model parameter required")
		return
	}
	// Normalize model name to match stored model_offers (remove date suffixes like -20250514)
	normalizedModel := discovery.NormalizeModelName(model)

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	weights := h.getScoringWeights(ctx)

	sqlQuery := `
		SELECT
			c.id AS credential_id,
			p.id AS provider_id,
			p.display_name,
			COALESCE(mo.raw_model_name, '') AS raw_model,
			COALESCE(mo.manual_priority, 99)::int AS manual_priority,
			COALESCE(mo.unit_price_in_per_1m, 0)::float8 AS price_in,
			COALESCE(mo.unit_price_out_per_1m, 0)::float8 AS price_out,
			COALESCE(mo.active_sessions, 0)::int AS active_sessions,
			COALESCE(mo.consecutive_failures, 0)::int AS consecutive_failures,
			c.concurrency_limit,
			COALESCE(mo.currency, 'USD') AS currency,
			COALESCE(mo.billing_mode, 'token') AS billing_mode,
			mo.unit_price_in_per_1m,
			mo.unit_price_out_per_1m,
			CASE
				WHEN mo.unit_price_in_per_1m = 0 AND mo.unit_price_out_per_1m = 0 THEN 0
				WHEN mo.unit_price_in_per_1m IS NULL AND mo.unit_price_out_per_1m IS NULL THEN 5.0
				ELSE COALESCE(mo.unit_price_in_per_1m, 0) + COALESCE(mo.unit_price_out_per_1m, 0)
			END AS blended_cost
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE p.tenant_id = 'default'
		  AND (lower(mo.raw_model_name) = lower($1) OR lower(mo.standardized_name) = lower($1))
		  AND mo.available IS TRUE
		ORDER BY
			CASE COALESCE(mo.billing_mode, 'token')
				WHEN 'free' THEN 1
				WHEN 'token_plan' THEN 1
				WHEN 'code_plan' THEN 1
				WHEN 'agent_plan' THEN 1
				WHEN 'monthly' THEN 1
				ELSE 2
			END,
			mo.manual_priority NULLS LAST
	`
	slog.Info("score-details executing SQL", "sql", sqlQuery, "model", model, "normalizedModel", normalizedModel)
	rows, err := h.db.Query(ctx, sqlQuery, normalizedModel)
	if err != nil {
		slog.Error("score-details query failed", "error", err, "model", model)
		writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
		return
	}
	defer rows.Close()

	type scoreDetail struct {
		CredentialID        int     `json:"credential_id"`
		ProviderID          int     `json:"provider_id"`
		ProviderName        string  `json:"provider_name"`
		RawModel            string  `json:"raw_model"`
		ManualPriority      int     `json:"manual_priority"`
		PriceIn             float64 `json:"price_in"`
		PriceOut            float64 `json:"price_out"`
		BlendedCost         float64 `json:"blended_cost"`
		ActiveSessions      int     `json:"active_sessions"`
		ConsecutiveFailures int     `json:"consecutive_failures"`
		ConcurrencyLimit    *int    `json:"concurrency_limit"`
		Currency            string  `json:"currency"`
		NormalizedCost      float64 `json:"normalized_cost"`
		SessionLoad         float64 `json:"session_load"`
		CompositeScore      float64 `json:"composite_score"`
		BillingMode         string  `json:"billing_mode"`
		BillingRound        int     `json:"billing_round"`
	}

	scoringWeights := scoringWeightsFromMap(weights)
	details := make([]scoreDetail, 0)
	for rows.Next() {
		var d scoreDetail
		var priceIn, priceOut *float64
		if err := rows.Scan(
			&d.CredentialID, &d.ProviderID, &d.ProviderName, &d.RawModel,
			&d.ManualPriority, &d.PriceIn, &d.PriceOut,
			&d.ActiveSessions, &d.ConsecutiveFailures, &d.ConcurrencyLimit,
			&d.Currency, &d.BillingMode, &priceIn, &priceOut, &d.BlendedCost,
		); err != nil {
			continue
		}

		var defaultPrice float64
		if d.Currency == "CNY" {
			defaultPrice = weights["default_price_cny"]
		} else {
			defaultPrice = weights["default_price_usd"]
		}
		if defaultPrice <= 0 {
			defaultPrice = 5.0
		}

		if d.BlendedCost == 0 {
			d.NormalizedCost = 0
		} else {
			d.NormalizedCost = d.BlendedCost / defaultPrice
		}

		if d.ConcurrencyLimit != nil && *d.ConcurrencyLimit > 0 {
			d.SessionLoad = float64(d.ActiveSessions) / float64(*d.ConcurrencyLimit)
			if d.SessionLoad > 1 {
				d.SessionLoad = 1
			}
		}

		d.BillingRound = provider.BillingRound(d.BillingMode)
		d.CompositeScore = routing.CalculateCompositeScore(provider.Candidate{
			ManualPriority:      d.ManualPriority,
			PriceInPer1M:        priceIn,
			PriceOutPer1M:       priceOut,
			Currency:            d.Currency,
			ConcurrencyLimit:    d.ConcurrencyLimit,
			ActiveSessions:      d.ActiveSessions,
			ConsecutiveFailures: d.ConsecutiveFailures,
			BillingMode:         d.BillingMode,
			CredentialID:        d.CredentialID,
		}, scoringWeights)

		details = append(details, d)
	}

	sort.SliceStable(details, func(i, j int) bool {
		return routing.CompareCandidatePriority(
			provider.Candidate{
				CredentialID:   details[i].CredentialID,
				ManualPriority: details[i].ManualPriority,
				CompositeScore: details[i].CompositeScore,
				BillingMode:    details[i].BillingMode,
			},
			provider.Candidate{
				CredentialID:   details[j].CredentialID,
				ManualPriority: details[j].ManualPriority,
				CompositeScore: details[j].CompositeScore,
				BillingMode:    details[j].BillingMode,
			},
		)
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"model":      model,
		"weights":    weights,
		"candidates": details,
	})
}

func (h *Handler) handleRoutingScoringWeights(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	if r.Method == http.MethodGet {
		weights := h.getScoringWeights(ctx)
		writeJSON(w, http.StatusOK, weights)
		return
	}

	if r.Method == http.MethodPatch {
		var patch map[string]float64
		if err := readJSON(r, &patch); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}

		current := h.getScoringWeights(ctx)
		if v, ok := patch["price"]; ok {
			current["price"] = v
		}
		if v, ok := patch["session_load"]; ok {
			current["session_load"] = v
		}
		if v, ok := patch["failure_penalty"]; ok {
			current["failure_penalty"] = v
		}
		if v, ok := patch["default_price_cny"]; ok {
			current["default_price_cny"] = v
		}
		if v, ok := patch["default_price_usd"]; ok {
			current["default_price_usd"] = v
		}

		weightsJSON, _ := json.Marshal(current)
		_, err := h.db.Exec(ctx, `
			UPDATE routing_policy SET scoring_weights_json = $1 WHERE tenant_id = 'default'
		`, weightsJSON)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}

		auditMap := make(map[string]any)
		for k, v := range current {
			auditMap[k] = v
		}
		h.logAudit(r, "scoring_weights_update", auditMap)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	writeError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func scoringWeightsFromMap(m map[string]float64) routing.ScoringWeights {
	w := routing.DefaultScoringWeights()
	if v, ok := m["price"]; ok {
		w.Price = v
	}
	if v, ok := m["session_load"]; ok {
		w.SessionLoad = v
	}
	if v, ok := m["failure_penalty"]; ok {
		w.FailurePenalty = v
	}
	if v, ok := m["default_price_cny"]; ok {
		w.DefaultPriceCNY = v
	}
	if v, ok := m["default_price_usd"]; ok {
		w.DefaultPriceUSD = v
	}
	return w
}

func (h *Handler) getScoringWeights(ctx context.Context) map[string]float64 {
	defaultWeights := map[string]float64{
		"price":             10,
		"session_load":      5,
		"failure_penalty":   20,
		"default_price_cny": 5.0,
		"default_price_usd": 5.0,
	}

	var weightsJSON []byte
	err := h.db.QueryRow(ctx, `
		SELECT COALESCE(scoring_weights_json, '{}'::jsonb)
		FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
	`).Scan(&weightsJSON)
	if err != nil || len(weightsJSON) == 0 {
		return defaultWeights
	}

	var weights map[string]float64
	if err := json.Unmarshal(weightsJSON, &weights); err != nil {
		return defaultWeights
	}

	for k, v := range defaultWeights {
		if _, ok := weights[k]; !ok {
			weights[k] = v
		}
	}
	return weights
}

func (h *Handler) handleRoutingFeaturedModelsDynamic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	var featuredModels []string
	polRow := h.db.QueryRow(ctx, `SELECT featured_models FROM routing_policy WHERE tenant_id = 'default'`)
	_ = polRow.Scan(&featuredModels)

	popular := h.queryPopularModels(ctx, featuredModels, nil)

	type featuredModel struct {
		Name             string `json:"name"`
		StandardizedName string `json:"standardized_name"`
		Count            int    `json:"count"`
		Source           string `json:"source"`
	}
	models := make([]featuredModel, 0, len(popular))
	for _, p := range popular {
		cnt := 0
		if p.Count != nil {
			cnt = *p.Count
		}
		models = append(models, featuredModel{
			Name:             p.CanonicalName,
			StandardizedName: p.CanonicalName,
			Count:            cnt,
			Source:           p.Source,
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}

func (h *Handler) handleFreePoolStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			COALESCE(p.catalog_code, '') AS catalog_code,
			p.display_name AS provider_name,
			c.id AS credential_id,
			c.label AS credential_label,
			c.status AS credential_status,
			c.availability_state,
			c.quota_state,
			c.balance_usd,
			(c.secret_ciphertext IS NOT NULL) AS has_secret,
			c.acquisition_source,
			c.acquisition_detail,
			COUNT(mo.id) AS total_offers,
			COUNT(mo.id) FILTER (WHERE mo.available) AS available_offers,
			COUNT(mo.id) FILTER (WHERE mo.unit_price_in_per_1m = 0 AND mo.unit_price_out_per_1m = 0) AS free_offers
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN model_offers mo ON mo.credential_id = c.id
		WHERE c.pool_group = 'free'
		GROUP BY p.catalog_code, p.display_name, c.id, c.label,
				 c.status, c.availability_state, c.quota_state, c.balance_usd,
				 c.secret_ciphertext, c.acquisition_source, c.acquisition_detail
		ORDER BY p.display_name
	`)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"pool": []any{}, "error": err.Error()})
		return
	}
	defer rows.Close()

	type poolEntry struct {
		CatalogCode       string   `json:"catalog_code"`
		ProviderName      string   `json:"provider_name"`
		CredentialID      int      `json:"credential_id"`
		CredentialLabel   string   `json:"credential_label"`
		CredentialStatus  string   `json:"credential_status"`
		AvailabilityState string   `json:"availability_state"`
		QuotaState        string   `json:"quota_state"`
		BalanceUSD        *float64 `json:"balance_usd"`
		HasSecret         bool     `json:"has_secret"`
		AcquisitionSource *string  `json:"acquisition_source"`
		AcquisitionDetail *string  `json:"acquisition_detail"`
		TotalOffers       int      `json:"total_offers"`
		AvailableOffers   int      `json:"available_offers"`
		FreeOffers        int      `json:"free_offers"`
		Models            []any    `json:"models"`
		ModelNames        []string `json:"model_names"`
	}
	pool := make([]poolEntry, 0)
	byCred := map[int][]any{}
	for rows.Next() {
		var e poolEntry
		if err := rows.Scan(
			&e.CatalogCode, &e.ProviderName, &e.CredentialID,
			&e.CredentialLabel, &e.CredentialStatus, &e.AvailabilityState,
			&e.QuotaState, &e.BalanceUSD, &e.HasSecret,
			&e.AcquisitionSource, &e.AcquisitionDetail,
			&e.TotalOffers, &e.AvailableOffers, &e.FreeOffers,
		); err != nil {
			slog.Warn("free-pool status: scan row failed", "error", err.Error())
			continue
		}
		e.Models = []any{}
		e.ModelNames = []string{}
		pool = append(pool, e)
		byCred[e.CredentialID] = []any{}
	}

	// Fetch all free-tier model offers (Python lists billing_mode='free' in models array,
	// but stats counts ALL model_offers joined to free credentials)
	modelRows, err := h.db.Query(ctx, `
		SELECT
			mo.id AS offer_id,
			mo.raw_model_name,
			COALESCE(mo.standardized_name, '') AS standardized_name,
			mo.available,
			mo.billing_mode,
			COALESCE(mo.routing_tier, 9) AS routing_tier,
			COALESCE(mo.unit_price_in_per_1m, 0) AS unit_price_in_per_1m,
			COALESCE(mo.unit_price_out_per_1m, 0) AS unit_price_out_per_1m,
			COALESCE(mo.currency, '') AS currency,
			COALESCE(p.catalog_code, '') AS catalog_code,
			p.display_name AS provider_name,
			p.protocol,
			p.base_url,
			c.id AS credential_id,
			c.label AS credential_label,
			c.status AS credential_status,
			c.availability_state,
			c.quota_state
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		WHERE c.pool_group = 'free' AND mo.billing_mode = 'free'
		ORDER BY mo.raw_model_name ASC, p.display_name ASC
	`)
	if err != nil {
		slog.Warn("free-pool status: models query failed", "error", err.Error())
	}

	// Also query for stats: ALL model_offers joined to free credentials (Python semantics)
	allModelRows, err := h.db.Query(ctx, `
		SELECT mo.id AS offer_id, mo.billing_mode
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		WHERE c.pool_group = 'free'
	`)
	if err != nil {
		slog.Warn("free-pool status: all-models query failed", "error", err.Error())
	}
	models := make([]any, 0)
	liveModelsByCode := map[string][]string{}
	// activeCodesSet is built from pool entries (Python semantics: status['pool'] is source of truth)
	activeCodesSet := map[string]struct{}{}
	byCodeForCatalog := map[string]struct{}{}
	if modelRows != nil {
		defer modelRows.Close()
		for modelRows.Next() {
			var (
				offerID                                                          int
				rawModel, stdName, billingMode, catalogCode, providerName        string
				protocol, baseURL, credLabel, credStatus, availState, quotaState string
				currency                                                         string
				available                                                        bool
				routingTier                                                      int
				priceIn, priceOut                                                float64
				credID                                                           int
			)
			if err := modelRows.Scan(
				&offerID, &rawModel, &stdName, &available, &billingMode, &routingTier,
				&priceIn, &priceOut, &currency, &catalogCode, &providerName,
				&protocol, &baseURL, &credID, &credLabel, &credStatus,
				&availState, &quotaState,
			); err != nil {
				slog.Warn("free-pool status: scan model row failed", "error", err.Error())
				continue
			}
			routable := available && credStatus == "active" &&
				availState != "rate_limited" && availState != "cooling" && availState != "unreachable" &&
				quotaState != "exhausted" && quotaState != "balance_exhausted"
			item := map[string]any{
				"offer_id":              offerID,
				"raw_model_name":        rawModel,
				"standardized_name":     nilOrString(stdName),
				"canonical_name":        nil,
				"available":             available,
				"billing_mode":          billingMode,
				"routing_tier":          routingTier,
				"unit_price_in_per_1m":  nilIfZeroFloat(priceIn),
				"unit_price_out_per_1m": nilIfZeroFloat(priceOut),
				"currency":              nilIfEmptyStr(currency),
				"catalog_code":          catalogCode,
				"provider_name":         providerName,
				"protocol":              protocol,
				"base_url":              baseURL,
				"credential_id":         credID,
				"credential_label":      credLabel,
				"credential_status":     credStatus,
				"availability_state":    availState,
				"quota_state":           quotaState,
				"routable":              routable,
			}
			models = append(models, item)
			if available {
				liveModelsByCode[catalogCode] = append(liveModelsByCode[catalogCode], rawModel)
			}
			byCodeForCatalog[catalogCode] = struct{}{}
			// Attach to pool entry models list
			if existing, ok := byCred[credID]; ok {
				byCred[credID] = append(existing, map[string]any{
					"offer_id":          offerID,
					"raw_model_name":    rawModel,
					"standardized_name": nilOrString(stdName),
					"available":         available,
					"routable":          routable,
					"routing_tier":      routingTier,
				})
			}
		}
	}

	// Build active_codes from pool entries (matches Python get_pool_overview)
	for _, e := range pool {
		if e.CredentialStatus == "active" {
			activeCodesSet[e.CatalogCode] = struct{}{}
		}
	}

	// Compute stats: total_models = all model_offers (no billing_mode filter),
	// free_models = model_offers with billing_mode='free'
	totalModelSet := map[int]struct{}{}
	freeModelSet := map[int]struct{}{}
	if allModelRows != nil {
		defer allModelRows.Close()
		for allModelRows.Next() {
			var oid int
			var bm string
			if err := allModelRows.Scan(&oid, &bm); err == nil {
				totalModelSet[oid] = struct{}{}
				if bm == "free" {
					freeModelSet[oid] = struct{}{}
				}
			}
		}
	}

	// Update pool entries with per-credential models and model_names
	for i := range pool {
		if ms, ok := byCred[pool[i].CredentialID]; ok {
			pool[i].Models = ms
			names := make([]string, 0, len(ms))
			for _, m := range ms {
				if mm, ok := m.(map[string]any); ok {
					if n, ok2 := mm["raw_model_name"].(string); ok2 {
						names = append(names, n)
					}
				}
			}
			pool[i].ModelNames = names
		}
	}

	// Build catalog list (mirrors Python catalog_for_api)
	registeredSet := map[string]struct{}{}
	for code := range activeCodesSet {
		registeredSet[code] = struct{}{}
	}
	catalog := buildFreePoolCatalog(registeredSet, liveModelsByCode)
	activeCodes := make([]string, 0, len(activeCodesSet))
	for c := range activeCodesSet {
		activeCodes = append(activeCodes, c)
	}
	sort.Strings(activeCodes)

	// Sort live models by code
	for c := range liveModelsByCode {
		sort.Strings(liveModelsByCode[c])
	}

	// Count routable models
	routableCount := 0
	for _, m := range models {
		if mm, ok := m.(map[string]any); ok {
			if r, ok2 := mm["routable"].(bool); ok2 && r {
				routableCount++
			}
		}
	}

	// Use DISTINCT counts to match Python (providers can have multiple credentials)
	distinctProviders := map[string]struct{}{}
	availableProvidersSet := map[string]struct{}{}
	for _, e := range pool {
		distinctProviders[e.CatalogCode] = struct{}{}
		if e.CredentialStatus == "active" &&
			e.AvailabilityState != "rate_limited" && e.AvailabilityState != "cooling" {
			availableProvidersSet[e.CatalogCode] = struct{}{}
		}
	}
	totalModels := len(totalModelSet)
	freeModels := len(freeModelSet)

	catalogRegistered := 0
	for _, c := range catalog {
		if reg, ok := c["pool_registered"].(bool); ok && reg {
			catalogRegistered++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"pool":   pool,
		"models": models,
		"stats": map[string]any{
			"total_providers":     len(distinctProviders),
			"available_providers": len(availableProvidersSet),
			"total_models":        totalModels,
			"free_models":         freeModels,
			"routable_models":     routableCount,
			"catalog_templates":   len(catalog),
			"catalog_registered":  catalogRegistered,
		},
		"active_catalog_codes": activeCodes,
		"live_models_by_code":  liveModelsByCode,
		"catalog":              catalog,
	})
}

func nilIfZeroFloat(p float64) any {
	if p == 0 {
		return nil
	}
	return p
}

func nilIfEmptyStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nilOrString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (h *Handler) handleFreePoolRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		CatalogCode string   `json:"catalog_code"`
		DisplayName string   `json:"display_name"`
		BaseURL     string   `json:"base_url"`
		Protocol    string   `json:"protocol"`
		APIKey      string   `json:"api_key"`
		Models      []string `json:"models"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	if req.CatalogCode == "" || req.BaseURL == "" {
		writeError(w, http.StatusBadRequest, "catalog_code and base_url required")
		return
	}

	if req.DisplayName == "" {
		req.DisplayName = req.CatalogCode
	}
	if req.Protocol == "" {
		req.Protocol = "openai-completions"
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// Upsert provider (align with Python free_pool_manager)
	_, err := h.db.Exec(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, catalog_code, is_custom,
			kind, category, protocol, base_url, egress_profile, domestic, enabled)
		VALUES ('default', $1, $2, $1, false,
			'cloud', 'aggregator', $3, $4, 'direct', true, true)
		ON CONFLICT (tenant_id, code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			base_url = EXCLUDED.base_url,
			enabled = true
	`, req.CatalogCode, req.DisplayName, req.Protocol, req.BaseURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert provider failed")
		return
	}

	var providerID int
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `
		SELECT id FROM providers WHERE catalog_code = $1 AND tenant_id = 'default'
	`, req.CatalogCode).Scan(&providerID)

	credLabel := req.CatalogCode + "-free-key"
	if req.APIKey != "" {
		encrypted, encErr := h.encryptCred([]byte(req.APIKey))
		if encErr != nil {
			writeError(w, http.StatusInternalServerError, "encryption failed")
			return
		}
		var existingID int
		findErr := h.db.QueryRow(ctx, `
			SELECT id FROM credentials WHERE provider_id = $1 AND label = $2
		`, providerID, credLabel).Scan(&existingID)
		if findErr != nil {
			//nolint:errcheck // best-effort exec, non-critical
			h.db.Exec(ctx, `
				INSERT INTO credentials (provider_id, tenant_id, label, secret_ciphertext,
					trust_level, status, lifecycle_status, availability_state, quota_state,
					pool_group)
				VALUES ($1, 'default', $2, $3, 'degraded', 'active',
					'active', 'ready', 'ok', 'free')
			`, providerID, credLabel, encrypted)
		} else {
			//nolint:errcheck // best-effort exec, non-critical
			h.db.Exec(ctx, `
				UPDATE credentials SET
					secret_ciphertext = $3,
					status = 'active',
					pool_group = 'free',
					updated_at = NOW()
				WHERE provider_id = $1 AND label = $2
			`, providerID, credLabel, encrypted)
		}
	}

	// Insert model offers
	for _, model := range req.Models {
		var freeCredID int
		if err := h.db.QueryRow(ctx, `SELECT id FROM credentials WHERE provider_id = $1 AND pool_group = 'free' LIMIT 1`, providerID).Scan(&freeCredID); err != nil {
			continue
		}
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			INSERT INTO model_offers (credential_id, raw_model_name, available,
				routing_tier, billing_mode, currency, unit_price_in_per_1m, unit_price_out_per_1m,
				pricing_source, pricing_updated_at)
			VALUES ($1, $2, true, 9, 'free', 'CNY', 0, 0, 'free_pool', NOW())
		`, freeCredID, model)
	}

	h.logAudit(r, "free_pool_register", map[string]any{
		"catalog_code": req.CatalogCode,
		"base_url":     req.BaseURL,
		"models":       req.Models,
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"status":      "registered",
		"provider_id": providerID,
	})
}

func (h *Handler) handleFreePoolModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	rows, err := h.db.Query(ctx, `
		SELECT
			mo.id AS offer_id, mo.raw_model_name, mo.standardized_name,
			COALESCE(mc.canonical_name, '') AS canonical_name,
			mo.available, COALESCE(mo.billing_mode, 'token') AS billing_mode,
			COALESCE(mo.routing_tier, 9) AS routing_tier,
			mo.unit_price_in_per_1m, mo.unit_price_out_per_1m, mo.currency,
			COALESCE(p.catalog_code, '') AS catalog_code,
			p.display_name AS provider_name, p.protocol, p.base_url,
			c.id AS credential_id, c.label AS credential_label,
			COALESCE(c.status, 'unknown') AS credential_status,
			COALESCE(c.availability_state, 'ready') AS availability_state,
			COALESCE(c.quota_state, 'ok') AS quota_state
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN models_canonical mc ON mc.id = mo.canonical_id
		WHERE c.pool_group = 'free' AND mo.billing_mode = 'free'
		ORDER BY mo.raw_model_name ASC, p.display_name ASC
	`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	type poolModel struct {
		OfferID           int      `json:"offer_id"`
		RawModelName      string   `json:"raw_model_name"`
		StandardizedName  *string  `json:"standardized_name"`
		CanonicalName     string   `json:"canonical_name"`
		Available         bool     `json:"available"`
		BillingMode       string   `json:"billing_mode"`
		RoutingTier       int      `json:"routing_tier"`
		PriceInPer1M      *float64 `json:"unit_price_in_per_1m"`
		PriceOutPer1M     *float64 `json:"unit_price_out_per_1m"`
		Currency          *string  `json:"currency"`
		CatalogCode       string   `json:"catalog_code"`
		ProviderName      string   `json:"provider_name"`
		Protocol          string   `json:"protocol"`
		BaseURL           string   `json:"base_url"`
		CredentialID      int      `json:"credential_id"`
		CredentialLabel   string   `json:"credential_label"`
		CredentialStatus  string   `json:"credential_status"`
		AvailabilityState string   `json:"availability_state"`
		QuotaState        string   `json:"quota_state"`
	}

	models := make([]poolModel, 0)
	routable := 0
	for rows.Next() {
		var m poolModel
		if err := rows.Scan(&m.OfferID, &m.RawModelName, &m.StandardizedName,
			&m.CanonicalName, &m.Available, &m.BillingMode, &m.RoutingTier,
			&m.PriceInPer1M, &m.PriceOutPer1M, &m.Currency,
			&m.CatalogCode, &m.ProviderName, &m.Protocol, &m.BaseURL,
			&m.CredentialID, &m.CredentialLabel, &m.CredentialStatus,
			&m.AvailabilityState, &m.QuotaState); err != nil {
			continue
		}
		models = append(models, m)
		if m.Available && m.CredentialStatus == "active" &&
			m.AvailabilityState != "rate_limited" && m.AvailabilityState != "cooling" &&
			m.AvailabilityState != "unreachable" &&
			m.QuotaState != "exhausted" && m.QuotaState != "balance_exhausted" {
			routable++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"models":   models,
		"total":    len(models),
		"routable": routable,
	})
}

func (h *Handler) handleFreePoolCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Get registered catalog codes + live models from DB
	rows, err := h.db.Query(ctx, `
		SELECT p.catalog_code, COALESCE(mo.raw_model_name, '')
		FROM providers p
		LEFT JOIN model_offers mo ON mo.credential_id IN (
			SELECT id FROM credentials WHERE provider_id = p.id AND pool_group = 'free'
		)
		WHERE EXISTS (SELECT 1 FROM credentials c WHERE c.provider_id = p.id AND c.pool_group = 'free')
		AND p.catalog_code <> ''
	`)
	registered := make(map[string][]string)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var code, model string
			if err := rows.Scan(&code, &model); err == nil && code != "" {
				if model != "" {
					registered[code] = append(registered[code], model)
				}
			}
		}
	}

	registeredSet := make(map[string]struct{})
	liveModelsByCode := make(map[string][]string)
	for code, models := range registered {
		registeredSet[code] = struct{}{}
		if models != nil {
			liveModelsByCode[code] = models
		}
	}

	entries := buildFreePoolCatalog(registeredSet, liveModelsByCode)

	writeJSON(w, http.StatusOK, map[string]any{"providers": entries})
}

// ── Free Pool: available (static catalog) ────────────────────────────────

func (h *Handler) handleFreePoolAvailable(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Static catalog of known free-tier providers
	providers := []map[string]any{
		{"catalog_code": "zhipu-free", "display_name": "Zhipu GLM (Free Tier)", "base_url": "https://open.bigmodel.cn/api/paas/v4", "models": []string{"glm-4-flash", "glm-4.7-flash"}, "rpm_limit": 5, "signup_url": "https://open.bigmodel.cn", "env_vars": []string{"ZHIPU_API_KEY", "BIGMODEL_API_KEY"}, "needs_key": true},
		{"catalog_code": "siliconflow-free", "display_name": "SiliconFlow (Free Tier)", "base_url": "https://api.siliconflow.cn/v1", "models": []string{"Qwen/Qwen2.5-7B-Instruct", "deepseek-ai/DeepSeek-R1-Distill-Qwen-7B"}, "rpm_limit": 10, "signup_url": "https://cloud.siliconflow.cn", "env_vars": []string{"SILICONFLOW_API_KEY"}, "needs_key": true},
		{"catalog_code": "groq-free", "display_name": "Groq (Free Tier)", "base_url": "https://api.groq.com/openai/v1", "models": []string{"llama-3.3-70b-versatile", "mixtral-8x7b-32768"}, "rpm_limit": 30, "signup_url": "https://console.groq.com", "env_vars": []string{"GROQ_API_KEY"}, "needs_key": true},
		{"catalog_code": "cerebras-free", "display_name": "Cerebras (Free Tier)", "base_url": "https://api.cerebras.ai/v1", "models": []string{"llama-3.3-70b"}, "rpm_limit": 30, "signup_url": "https://cloud.cerebras.ai", "env_vars": []string{"CEREBRAS_API_KEY"}, "needs_key": true},
		{"catalog_code": "sambanova-free", "display_name": "SambaNova (Free Tier)", "base_url": "https://api.sambanova.ai/v1", "models": []string{"Meta-Llama-3.3-70B-Instruct"}, "rpm_limit": 10, "signup_url": "https://cloud.sambanova.ai", "env_vars": []string{"SAMBANOVA_API_KEY"}, "needs_key": true},
		{"catalog_code": "google-gemini-free", "display_name": "Google Gemini (Free Tier)", "base_url": "https://generativelanguage.googleapis.com/v1beta/openai", "models": []string{"gemini-2.0-flash", "gemini-2.5-flash-preview-05-20"}, "rpm_limit": 15, "signup_url": "https://aistudio.google.com", "env_vars": []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, "needs_key": true},
		{"catalog_code": "mistral-free", "display_name": "Mistral (Free Tier)", "base_url": "https://api.mistral.ai/v1", "models": []string{"mistral-small-latest"}, "rpm_limit": 30, "signup_url": "https://console.mistral.ai", "env_vars": []string{"MISTRAL_API_KEY"}, "needs_key": true},
		{"catalog_code": "openrouter-free", "display_name": "OpenRouter (Free Models)", "base_url": "https://openrouter.ai/api/v1", "models": []string{"meta-llama/llama-3.3-70b-instruct:free"}, "rpm_limit": 20, "signup_url": "https://openrouter.ai", "env_vars": []string{"OPENROUTER_API_KEY"}, "needs_key": true},
		{"catalog_code": "together-free", "display_name": "Together (Free Tier)", "base_url": "https://api.together.xyz/v1", "models": []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo"}, "rpm_limit": 60, "signup_url": "https://api.together.xyz", "env_vars": []string{"TOGETHER_API_KEY"}, "needs_key": true},
		{"catalog_code": "nvidia-nim-free", "display_name": "NVIDIA NIM (Free Tier)", "base_url": "https://integrate.api.nvidia.com/v1", "models": []string{"meta/llama-3.3-70b-instruct"}, "rpm_limit": 10, "signup_url": "https://build.nvidia.com", "env_vars": []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"}, "needs_key": true},
		{"catalog_code": "cloudflare-workers-ai-free", "display_name": "Cloudflare Workers AI (Free)", "base_url": "https://api.cloudflare.com/client/v4/accounts/{account_id}/ai/v1", "models": []string{"@cf/meta/llama-3.3-70b-instruct"}, "rpm_limit": 300, "signup_url": "https://dash.cloudflare.com", "env_vars": []string{"CLOUDFLARE_API_TOKEN", "CF_API_TOKEN"}, "needs_key": true},
	}

	writeJSON(w, http.StatusOK, map[string]any{"providers": providers})
}

// ── Free Pool: register-all ──────────────────────────────────────────────

func (h *Handler) handleFreePoolRegisterAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Collect env-based provider configs
	envConfigs := h.collectEnvProviderConfigs()

	// Register each
	results := make([]map[string]any, 0)
	for _, cfg := range envConfigs {
		result := h.registerFreeProvider(w, r, cfg)
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, results)
}

// ── Free Pool: import-env ────────────────────────────────────────────────

func (h *Handler) handleFreePoolImportEnv(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	envConfigs := h.collectEnvProviderConfigs()
	registered := 0
	results := make([]map[string]any, 0)

	for _, cfg := range envConfigs {
		result := h.registerFreeProvider(w, r, cfg)
		if result["status"] == "registered" {
			registered++
		}
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":       "env",
		"candidates": len(envConfigs),
		"registered": registered,
		"results":    results,
	})
}

// ── Free Pool: bootstrap ─────────────────────────────────────────────────

func (h *Handler) handleFreePoolBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// 1. Cleanup disabled oauth-bridge credentials
	cleanupResults := make([]map[string]any, 0)
	rows, err := h.db.Query(ctx, `
		SELECT id, label FROM credentials
		WHERE pool_group = 'free' AND label LIKE '%oauth-bridge%' AND status = 'disabled'
	`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id int
			var label string
			//nolint:errcheck // best-effort
			rows.Scan(&id, &label)
			//nolint:errcheck // best-effort exec, non-critical
			h.db.Exec(ctx, `UPDATE credentials SET status = 'disabled', availability_state = 'unreachable', updated_at = NOW() WHERE id = $1`, id)
			cleanupResults = append(cleanupResults, map[string]any{"id": id, "label": label})
		}
	}

	// 2. Mirror existing keys to free pool
	mirrorResults := h.mirrorExistingKeys(ctx)

	// 3. Import from env
	envConfigs := h.collectEnvProviderConfigs()
	envResults := make([]map[string]any, 0)
	for _, cfg := range envConfigs {
		result := h.registerFreeProvider(w, r, cfg)
		envResults = append(envResults, result)
	}

	// 4. Get current status
	statusRows, _ := h.db.Query(ctx, `
		SELECT COALESCE(p.catalog_code, ''), p.display_name, c.label, c.status,
		       COUNT(mo.id) AS offers
		FROM credentials c
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN model_offers mo ON mo.credential_id = c.id
		WHERE c.pool_group = 'free'
		GROUP BY p.catalog_code, p.display_name, c.label, c.status
		ORDER BY p.display_name
	`)
	poolStatus := make([]map[string]any, 0)
	if statusRows != nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var code, name, label, status string
			var offers int
			//nolint:errcheck // best-effort
			statusRows.Scan(&code, &name, &label, &status, &offers)
			poolStatus = append(poolStatus, map[string]any{
				"catalog_code": code, "provider_name": name,
				"label": label, "status": status, "offers": offers,
			})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"cleanup": map[string]any{"disabled": cleanupResults},
		"mirror":  map[string]any{"registered": len(mirrorResults), "results": mirrorResults},
		"env":     map[string]any{"registered": len(envResults), "results": envResults},
		"status":  poolStatus,
	})
}

// ── Free Pool: bridge-oauth ──────────────────────────────────────────────

func (h *Handler) handleFreePoolBridgeOAuth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Check for OAuth tokens in environment
	geminiToken := os.Getenv("OAUTH_GEMINI_ACCESS_TOKEN")
	if geminiToken == "" {
		geminiToken = os.Getenv("GEMINI_OAUTH_ACCESS_TOKEN")
	}

	registered := 0
	results := make([]map[string]any, 0)

	if geminiToken != "" {
		cfg := freeProviderConfig{
			catalogCode:       "google-gemini-free",
			displayName:       "Google Gemini (oauth-bridge)",
			baseURL:           "https://generativelanguage.googleapis.com/v1beta/openai",
			protocol:          "openai-completions",
			apiKey:            geminiToken,
			models:            []string{"gemini-2.0-flash", "gemini-2.5-flash-preview-05-20"},
			acquisitionMode:   "oauth",
			acquisitionDetail: "env:OAUTH_GEMINI_ACCESS_TOKEN",
		}
		result := h.registerFreeProvider(w, r, cfg)
		if result["status"] == "registered" {
			registered++
		}
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"mode":       "oauth_bridge",
		"candidates": len(results),
		"registered": registered,
		"results":    results,
	})
}

// ── Free Pool: discover ──────────────────────────────────────────────────

func (h *Handler) handleFreePoolDiscover(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Check capacity
	var count int
	//nolint:errcheck // scan error non-critical
	h.db.QueryRow(ctx, `SELECT COUNT(*) FROM credentials WHERE pool_group = 'free' AND status = 'active'`).Scan(&count)
	if count >= 30 {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "skipped",
			"reason":  "pool_at_capacity",
			"current": count,
		})
		return
	}

	// Import from env
	envConfigs := h.collectEnvProviderConfigs()
	registered := 0
	for _, cfg := range envConfigs {
		result := h.registerFreeProvider(w, r, cfg)
		if result["status"] == "registered" {
			registered++
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "completed",
		"registered": registered,
		"total_pool": count + registered,
	})
}

// ── Free Pool: bulk-register ─────────────────────────────────────────────

func (h *Handler) handleFreePoolBulkRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		CatalogCode string   `json:"catalog_code"`
		DisplayName string   `json:"display_name"`
		BaseURL     string   `json:"base_url"`
		APIKeys     []string `json:"api_keys"`
		Models      []string `json:"models"`
		Protocol    string   `json:"protocol"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Protocol == "" {
		req.Protocol = "openai-completions"
	}

	registered := 0
	errors := 0
	results := make([]map[string]any, 0)

	for i, key := range req.APIKeys {
		if key == "" {
			continue
		}
		cfg := freeProviderConfig{
			catalogCode:       req.CatalogCode,
			displayName:       req.DisplayName,
			baseURL:           req.BaseURL,
			protocol:          req.Protocol,
			apiKey:            key,
			models:            req.Models,
			credentialLabel:   fmt.Sprintf("%s-free-key-%d", req.CatalogCode, i+1),
			acquisitionMode:   "bulk",
			acquisitionDetail: fmt.Sprintf("bulk-register key %d/%d", i+1, len(req.APIKeys)),
		}
		result := h.registerFreeProvider(w, r, cfg)
		if result["status"] == "registered" {
			registered++
		} else {
			errors++
		}
		result["key_index"] = i
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"catalog_code": req.CatalogCode,
		"total_keys":   len(req.APIKeys),
		"registered":   registered,
		"errors":       errors,
		"results":      results,
	})
}

// ── Shared helpers ────────────────────────────────────────────────────────

type freeProviderConfig struct {
	catalogCode       string
	displayName       string
	baseURL           string
	protocol          string
	apiKey            string
	models            []string
	credentialLabel   string
	acquisitionMode   string
	acquisitionDetail string
}

func (h *Handler) collectEnvProviderConfigs() []freeProviderConfig {
	envBindings := []struct {
		catalogCode string
		displayName string
		baseURL     string
		models      []string
		envVars     []string
	}{
		{"zhipu-free", "Zhipu GLM (Free Tier)", "https://open.bigmodel.cn/api/paas/v4", []string{"glm-4-flash", "glm-4.7-flash"}, []string{"ZHIPU_API_KEY", "BIGMODEL_API_KEY"}},
		{"siliconflow-free", "SiliconFlow (Free Tier)", "https://api.siliconflow.cn/v1", []string{"Qwen/Qwen2.5-7B-Instruct"}, []string{"SILICONFLOW_API_KEY"}},
		{"groq-free", "Groq (Free Tier)", "https://api.groq.com/openai/v1", []string{"llama-3.3-70b-versatile"}, []string{"GROQ_API_KEY"}},
		{"cerebras-free", "Cerebras (Free Tier)", "https://api.cerebras.ai/v1", []string{"llama-3.3-70b"}, []string{"CEREBRAS_API_KEY"}},
		{"sambanova-free", "SambaNova (Free Tier)", "https://api.sambanova.ai/v1", []string{"Meta-Llama-3.3-70B-Instruct"}, []string{"SAMBANOVA_API_KEY"}},
		{"google-gemini-free", "Google Gemini (Free Tier)", "https://generativelanguage.googleapis.com/v1beta/openai", []string{"gemini-2.0-flash"}, []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}},
		{"mistral-free", "Mistral (Free Tier)", "https://api.mistral.ai/v1", []string{"mistral-small-latest"}, []string{"MISTRAL_API_KEY"}},
		{"openrouter-free", "OpenRouter (Free Models)", "https://openrouter.ai/api/v1", []string{"meta-llama/llama-3.3-70b-instruct:free"}, []string{"OPENROUTER_API_KEY"}},
		{"together-free", "Together (Free Tier)", "https://api.together.xyz/v1", []string{"meta-llama/Llama-3.3-70B-Instruct-Turbo"}, []string{"TOGETHER_API_KEY"}},
		{"nvidia-nim-free", "NVIDIA NIM (Free Tier)", "https://integrate.api.nvidia.com/v1", []string{"meta/llama-3.3-70b-instruct"}, []string{"NVIDIA_API_KEY", "NVIDIA_NIM_API_KEY"}},
	}

	configs := make([]freeProviderConfig, 0)
	for _, binding := range envBindings {
		apiKey := ""
		for _, envVar := range binding.envVars {
			if v := os.Getenv(envVar); v != "" {
				apiKey = v
				break
			}
		}
		if apiKey == "" {
			continue
		}
		configs = append(configs, freeProviderConfig{
			catalogCode:       binding.catalogCode,
			displayName:       binding.displayName,
			baseURL:           binding.baseURL,
			protocol:          "openai-completions",
			apiKey:            apiKey,
			models:            binding.models,
			credentialLabel:   binding.catalogCode + "-free-key",
			acquisitionMode:   "env",
			acquisitionDetail: "env:" + binding.envVars[0],
		})
	}
	return configs
}

func (h *Handler) registerFreeProvider(w http.ResponseWriter, r *http.Request, cfg freeProviderConfig) map[string]any {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	// 1. Upsert provider_catalog
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO provider_catalog (code, tier, display_name, category, kind, protocol,
			base_url_template, discovery_strategy, domestic, hidden, notes)
		VALUES ($1, 9, $2, 'aggregator', 'cloud', $3, $4, 'manifest', true, false, 'auto-registered by free pool')
		ON CONFLICT (code) DO UPDATE SET display_name = EXCLUDED.display_name,
			base_url_template = EXCLUDED.base_url_template
	`, cfg.catalogCode, cfg.displayName, cfg.protocol, cfg.baseURL)

	// 2. Upsert provider
	//nolint:errcheck // best-effort exec, non-critical
	h.db.Exec(ctx, `
		INSERT INTO providers (tenant_id, code, display_name, catalog_code, is_custom,
			kind, category, protocol, base_url, egress_profile, domestic, enabled)
		VALUES ('default', $1, $2, $1, false,
			'cloud', 'aggregator', $3, $4, 'direct', true, true)
		ON CONFLICT (tenant_id, code) DO UPDATE SET display_name = EXCLUDED.display_name,
			base_url = EXCLUDED.base_url, enabled = true
	`, cfg.catalogCode, cfg.displayName, cfg.protocol, cfg.baseURL)

	// 3. Get provider_id
	var providerID int
	err := h.db.QueryRow(ctx, `SELECT id FROM providers WHERE tenant_id = 'default' AND catalog_code = $1`, cfg.catalogCode).Scan(&providerID)
	if err != nil {
		return map[string]any{"catalog_code": cfg.catalogCode, "status": "error", "message": "provider not found"}
	}

	// 4. Encrypt API key
	encrypted, encErr := h.encryptCred([]byte(cfg.apiKey))
	if encErr != nil {
		return map[string]any{"catalog_code": cfg.catalogCode, "status": "error", "message": "encryption failed"}
	}

	// 5. Upsert credential
	credLabel := cfg.credentialLabel
	if credLabel == "" {
		credLabel = cfg.catalogCode + "-free-key"
	}

	var credID int
	findErr := h.db.QueryRow(ctx, `SELECT id FROM credentials WHERE provider_id = $1 AND label = $2`, providerID, credLabel).Scan(&credID)
	tagsJSON := `["free-pool","source:` + cfg.acquisitionMode + `","catalog:` + cfg.catalogCode + `"]`
	if findErr != nil {
		// Insert new
		err = h.db.QueryRow(ctx, `
			INSERT INTO credentials (provider_id, tenant_id, label, secret_ciphertext,
				trust_level, status, lifecycle_status, availability_state, quota_state,
				pool_group, acquisition_source, acquisition_detail, tags)
			VALUES ($1, 'default', $2, $3, 'degraded', 'active',
				'active', 'ready', 'ok', 'free', $4, $5, CAST($6 AS jsonb))
			RETURNING id
		`, providerID, credLabel, encrypted, cfg.acquisitionMode, cfg.acquisitionDetail, tagsJSON).Scan(&credID)
		if err != nil {
			return map[string]any{"catalog_code": cfg.catalogCode, "status": "error", "message": "credential insert failed"}
		}
	} else {
		// Update existing
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			UPDATE credentials SET secret_ciphertext = $1, status = 'active',
				pool_group = 'free', acquisition_source = $2, acquisition_detail = $3,
				tags = CAST($4 AS jsonb), updated_at = NOW()
			WHERE id = $5
		`, encrypted, cfg.acquisitionMode, cfg.acquisitionDetail, tagsJSON, credID)
	}

	// 6. Insert model offers
	for _, model := range cfg.models {
		var canonID *int
		var cid int
		if err := h.db.QueryRow(ctx, `SELECT id FROM models_canonical WHERE canonical_name ILIKE $1 LIMIT 1`, model).Scan(&cid); err == nil {
			canonID = &cid
		}
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			INSERT INTO model_offers (credential_id, canonical_id, raw_model_name,
				available, routing_tier, billing_mode, currency,
				unit_price_in_per_1m, unit_price_out_per_1m, pricing_source, pricing_updated_at)
			VALUES ($1, $2, $3, true, 9, 'free', 'CNY', 0, 0, 'pool_manager', NOW())
		`, credID, canonID, model)
	}

	h.logAudit(r, "free_pool_register", map[string]any{
		"catalog_code": cfg.catalogCode,
		"base_url":     cfg.baseURL,
		"models":       cfg.models,
		"source":       cfg.acquisitionMode,
	})

	return map[string]any{
		"catalog_code":  cfg.catalogCode,
		"status":        "registered",
		"provider_id":   providerID,
		"credential_id": credID,
		"models":        len(cfg.models),
	}
}

func (h *Handler) mirrorExistingKeys(ctx context.Context) []map[string]any {
	// Mirror rules: copy credentials from main providers to free-pool providers
	mirrorRules := []struct {
		sourceCatalog string
		targetCatalog string
	}{
		{"nvidia", "nvidia-nim-free"},
		{"zhipu", "zhipu-free"},
	}

	results := make([]map[string]any, 0)
	for _, rule := range mirrorRules {
		var credID int
		var label, ciphertext string
		err := h.db.QueryRow(ctx, `
			SELECT c.id, c.label, c.secret_ciphertext
			FROM credentials c
			JOIN providers p ON p.id = c.provider_id
			WHERE p.catalog_code = $1 AND c.secret_ciphertext IS NOT NULL AND c.status = 'active'
			ORDER BY c.id ASC LIMIT 1
		`, rule.sourceCatalog).Scan(&credID, &label, &ciphertext)
		if err != nil {
			continue
		}

		// Check if target already has a free credential
		var existingID int
		err = h.db.QueryRow(ctx, `
			SELECT c.id FROM credentials c
			JOIN providers p ON p.id = c.provider_id
			WHERE p.catalog_code = $1 AND c.pool_group = 'free' LIMIT 1
		`, rule.targetCatalog).Scan(&existingID)
		if err == nil {
			// Already exists, skip
			continue
		}

		// Get target provider ID
		var targetProviderID int
		err = h.db.QueryRow(ctx, `SELECT id FROM providers WHERE catalog_code = $1`, rule.targetCatalog).Scan(&targetProviderID)
		if err != nil {
			continue
		}

		// Create free credential with mirrored key
		_, err = h.db.Exec(ctx, `
			INSERT INTO credentials (provider_id, tenant_id, label, secret_ciphertext,
				trust_level, status, lifecycle_status, availability_state, quota_state,
				pool_group, acquisition_source, acquisition_detail)
			VALUES ($1, 'default', $2, $3, 'degraded', 'active',
				'active', 'ready', 'ok', 'free', 'mirror', $4)
		`, targetProviderID, label+"-mirror", ciphertext, rule.sourceCatalog)
		if err == nil {
			results = append(results, map[string]any{
				"source": rule.sourceCatalog,
				"target": rule.targetCatalog,
				"label":  label,
			})
		}
	}
	return results
}
