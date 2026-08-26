package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/discovery"
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate"     //nolint:depguard // emergency-repair state recovery (2026-08-15)
	"github.com/kaixuan/llm-gateway-go/domains/streaming/executors" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/internal/upstreamurl"
	"github.com/kaixuan/llm-gateway-go/modelname"
	"github.com/kaixuan/llm-gateway-go/provider"
)

type routingHandler struct { //nolint:unused
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
	mu      sync.Mutex
	entries map[string]availableModelsCacheEntry
	ttl     time.Duration
	// hits/misses are exposed via /api/system/background-tasks for ops.
	hits   uint64
	misses uint64
}

type availableModelsCacheEntry struct {
	value     map[string]any
	expiresAt time.Time
}

func (c *availableModelsCache) get(now time.Time, tenantID string) (map[string]any, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[tenantID]
	if ok && now.Before(entry.expiresAt) {
		c.hits++
		return entry.value, true
	}
	c.misses++
	return nil, false
}

func (c *availableModelsCache) set(now time.Time, tenantID string, value map[string]any) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]availableModelsCacheEntry)
	}
	c.entries[tenantID] = availableModelsCacheEntry{value: value, expiresAt: now.Add(c.ttl)}
}

const availableModelsCacheTTL = 30 * time.Second

var globalAvailableModelsCache = &availableModelsCache{ttl: availableModelsCacheTTL}

func (h *Handler) logAudit(r *http.Request, action string, details map[string]any) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	actor := requestActor(r)
	if err := logAuditExec(ctx, h.db, actor, action, details); err != nil {
		slog.Debug("routing audit insert failed (best-effort)", "action", action, "error", err.Error())
	}
}

// logAuditExec inserts a row into routing_audit_log using the supplied
// executor (typically *pgxpool.Pool or pgx.Tx). It is exported so handlers
// running inside a transaction can keep their audit row tied to the same
// commit boundary as the data change.
func logAuditExec(ctx context.Context, exec pgxAuditExecutor, actor, action string, details map[string]any) error {
	detailsJSON, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal audit details: %w", err)
	}
	// Pass the payload as text and cast to jsonb. Passing a []byte would make
	// pgx encode it as the bytea type, which Postgres cannot assign to the
	// jsonb after_json column and rejects with "invalid input syntax for type
	// json" (SQLSTATE 22P02). This mirrors the insert in admin/users.go.
	_, err = exec.Exec(ctx, `
		INSERT INTO routing_audit_log (actor, action, target_type, after_json)
		VALUES ($1, $2, $3, $4::text::jsonb)
	`, actor, action, action, string(detailsJSON))
	return err
}

// pgxAuditExecutor is the small surface area logAuditExec needs. Both
// *pgxpool.Pool and pgx.Tx satisfy it via pgx v5's stable signatures.
type pgxAuditExecutor interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func requestActor(r *http.Request) string {
	// Prefer the verified AuthContext subject (super_admin / tenant_admin
	// username) over the network address so audit rows stay attributable
	// to a real operator. The X-Admin-User header is intentionally NOT
	// trusted here — any caller able to reach the admin API could forge it.
	if auth := GetAuthContext(r); auth != nil {
		if auth.Username != "" {
			return auth.Username
		}
		if auth.UserID > 0 {
			return fmt.Sprintf("user:%d", auth.UserID)
		}
		if auth.Role != "" {
			return "role:" + auth.Role
		}
	}
	actor := r.RemoteAddr
	if actor == "" {
		actor = "unknown"
	}
	return actor
}

// resolveCandidate is one row of the routing resolve table. It was a
// function-local type until 2026-08-18; promoted to package scope so
// applyURSMOverlay (and its unit tests) can operate on the slice directly.
type resolveCandidate struct {
	Rank                  int      `json:"rank"`
	ProviderID            int      `json:"provider_id"`
	ProviderName          string   `json:"provider_name"`
	CatalogCode           string   `json:"catalog_code"`
	Protocol              string   `json:"protocol"`
	BaseURL               string   `json:"base_url"`
	ProviderEnabled       bool     `json:"provider_enabled"`
	CredentialID          int      `json:"credential_id"`
	TenantID              string   `json:"-"`
	CredentialLabel       string   `json:"credential_label"`
	CredentialStatus      string   `json:"credential_status"`
	LifecycleStatus       string   `json:"lifecycle_status"`
	AvailabilityState     string   `json:"availability_state"`
	AvailabilityRecoverAt *string  `json:"availability_recover_at"`
	QuotaState            string   `json:"quota_state"`
	QuotaRecoverAt        *string  `json:"quota_recover_at"`
	ConcurrencyLimit      *int     `json:"concurrency_limit"`
	EffectiveConcurrency  *int     `json:"effective_concurrency"`
	EffectiveAt           *string  `json:"effective_at"`
	ExpiresAt             *string  `json:"expires_at"`
	CredentialInEffect    bool     `json:"credential_in_effect"`
	BalanceUSD            *float64 `json:"balance_usd"`
	CircuitState          string   `json:"circuit_state"`
	CoolingUntil          *string  `json:"cooling_until"`
	Available             bool     `json:"available"`
	Tier                  int      `json:"tier"`
	Weight                int      `json:"weight"`
	UnitPriceInPer1M      *float64 `json:"unit_price_in_per_1m"`
	UnitPriceOutPer1M     *float64 `json:"unit_price_out_per_1m"`
	Currency              string   `json:"currency"`
	SuccessRate           float64  `json:"success_rate"`
	P95LatencyMs          int      `json:"p95_latency_ms"`
	ModelName             string   `json:"model_name"`
	StandardizedName      string   `json:"standardized_name"`
	QuotaCapUSD           float64  `json:"quota_cap_usd"`
	QuotaUsedUSD          float64  `json:"quota_used_usd"`
	RuntimeRoutable       bool     `json:"runtime_routable"`
	Routable              bool     `json:"routable"`
	DBEligible            bool     `json:"db_eligible"`
	URSMObserved          bool     `json:"ursm_observed"`
	RuntimeState          string   `json:"runtime_state"`
	BlockReason           string   `json:"block_reason,omitempty"`
	ManualPriority        int      `json:"manual_priority"`
	ActiveSessions        int      `json:"active_sessions"`
	// R7 fix: 前端将基于这个字段判断 show 重置计数按钮，
	// 而 resolve 操作的是 credentials.consecutive_failures。
	// 这里增列 credential-level 的值用于显示，避免误判。
	ConsecutiveFailures           int     `json:"consecutive_failures"`
	CredentialConsecutiveFailures int     `json:"credential_consecutive_failures"`
	CompositeScore                float64 `json:"composite_score"`
	BillingMode                   string  `json:"billing_mode"`
	BillingRound                  int     `json:"billing_round"`
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
	// Resolve is an operator-facing diagnostic surface: always return every
	// matched candidate so unavailable credentials remain visible with their
	// block reason. Runtime routing still uses the view's is_routable flag.
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

	rawModels := append([]string{normalizedModel}, variants[1:]...)
	// 这些字段由 URSM v2 覆写；在 authoritative 模式下 not-ready 或查询失败
	// 会显式报告 unknown，而不是把数据库 eligibility 当作运行时可用。

	rows, err := h.db.Query(ctx, `
			SELECT
				p.id AS provider_id,
				COALESCE(p.display_name, p.code) AS provider_name,
				COALESCE(p.catalog_code, '') AS catalog_code,
				COALESCE(p.protocol, 'openai-completions') AS protocol,
				p.base_url,
				p.enabled AS provider_enabled,
					v.credential_id,
					v.tenant_id,
					COALESCE(c.label, '') AS credential_label,
				c.status AS credential_status,
				c.lifecycle_status,
				c.availability_state,
				c.availability_recover_at::text,
				c.quota_state,
				c.quota_recover_at::text,
				c.concurrency_limit,
				c.effective_concurrency,
				c.effective_at::text,
				c.expires_at::text,
				(c.effective_at IS NULL OR c.effective_at <= now())
					AND (c.expires_at IS NULL OR c.expires_at > now()) AS credential_in_effect,
				c.balance_usd::float8,
				COALESCE(cmb.routing_tier, 2) AS tier,
				COALESCE(cmb.weight, 100) AS weight,
				COALESCE(cmb.manual_priority, 99) AS manual_priority,
				COALESCE(cmb.active_sessions, 0) AS active_sessions,
				COALESCE(cmb.billing_mode, 'per_token') AS billing_mode,
				mo.unit_price_in_per_1m,
				mo.unit_price_out_per_1m,
				COALESCE(cmb.currency, 'USD') AS currency,
				v.raw_model_name AS model_name,
				COALESCE(mo.standardized_name, v.raw_model_name) AS standardized_name,
				COALESCE(mo.unit_price_in_per_1m, 0) AS quota_cap_usd,
				COALESCE(mo.unit_price_out_per_1m, 0) AS quota_used_usd,
				v.is_routable,
				v.unavailable_reason
			FROM v_routable_credential_models v
			JOIN credentials c ON c.id = v.credential_id
			JOIN providers p ON p.id = c.provider_id
			JOIN credential_model_bindings cmb ON cmb.id = v.binding_id
			LEFT JOIN model_offers mo ON mo.credential_id = v.credential_id
				AND mo.raw_model_name = v.raw_model_name
			-- 2026-07-24 fix: the ModelPicker emits models_canonical.canonical_name
			-- (e.g. "minimax-m3"), but the previous WHERE only matched
			-- v.raw_model_name (provider-cased, prefixed like "minimaxai/minimax-m3")
			-- or mo.standardized_name (NULL for any legacy row that didn't go
			-- through migration 395c). The picker never sent raw_model_name, so
			-- canonical-only lookups silently returned zero candidates. Join
			-- through v.canonical_id (already exposed by the view) and add a
			-- third OR branch so canonical_name → pm.canonical_id → mc.canonical_name
			-- resolves. models_canonical.canonical_name is lowercased by
			-- migration 396; lower(...) is a safety net.
			LEFT JOIN models_canonical mc ON mc.id = v.canonical_id
				WHERE ($2 = '' OR v.tenant_id = $2)
				  AND v.tenant_id = p.tenant_id
				  AND v.tenant_id = c.tenant_id
				  AND (
				      lower(v.raw_model_name) = ANY($1)
			      OR lower(COALESCE(mo.standardized_name, v.raw_model_name)) = ANY($1)
			      OR lower(mc.canonical_name) = ANY($1)
			  )
				  AND p.enabled IS TRUE

			ORDER BY
				CASE COALESCE(cmb.billing_mode, 'per_token')
					WHEN 'free' THEN 1
					WHEN 'token_plan' THEN 1
					WHEN 'code_plan' THEN 1
					WHEN 'agent_plan' THEN 1
					WHEN 'monthly' THEN 1
					ELSE 2
				END,
				COALESCE(cmb.manual_priority, 99),
				COALESCE(cmb.routing_tier, 2),
				COALESCE(cmb.weight, 100) DESC,
				COALESCE(cmb.success_rate, 0.9) DESC
				`, rawModels, EffectiveTenantIDAll(r))

	if err != nil {
		slog.Error("routing resolve query failed", "error", err.Error(), "model", model, "rawModels", rawModels)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	weights := scoringWeightsFromMap(h.getScoringWeights(ctx))
	candidates := make([]resolveCandidate, 0)
	for rows.Next() {
		var c resolveCandidate
		var isRoutable bool
		var unavailableReason *string

		// Slimmed scan (2026-07-24): circuit_state / cooling_until /
		// cmb.available / consecutive_failures / success_rate / p95_latency_ms
		// are no longer in the SELECT — they're owned by URSM v2 and
		// back-filled below if the manager is ready.
		if err := rows.Scan(
			&c.ProviderID, &c.ProviderName, &c.CatalogCode, &c.Protocol, &c.BaseURL,
			&c.ProviderEnabled, &c.CredentialID, &c.TenantID, &c.CredentialLabel, &c.CredentialStatus,
			&c.LifecycleStatus, &c.AvailabilityState, &c.AvailabilityRecoverAt,
			&c.QuotaState, &c.QuotaRecoverAt, &c.ConcurrencyLimit, &c.EffectiveConcurrency,
			&c.EffectiveAt, &c.ExpiresAt, &c.CredentialInEffect, &c.BalanceUSD,
			&c.Tier, &c.Weight,
			&c.ManualPriority, &c.ActiveSessions, &c.BillingMode,
			&c.UnitPriceInPer1M, &c.UnitPriceOutPer1M, &c.Currency,
			&c.ModelName, &c.StandardizedName,
			&c.QuotaCapUSD, &c.QuotaUsedUSD,
			&isRoutable, &unavailableReason,
		); err != nil {
			continue
		}
		// Keep database eligibility separate from the authoritative runtime
		// decision. The resolve endpoint is diagnostic and must not imply that
		// a SQL-routable row is request-routable when URSM has no node view.
		c.DBEligible = isRoutable
		c.RuntimeRoutable = isRoutable
		c.Routable = isRoutable
		c.RuntimeState = "db_only"

		if unavailableReason != nil {
			c.BlockReason = *unavailableReason
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
		c.CompositeScore = executors.CalculateCompositeScore(pc, weights)
		candidates = append(candidates, c)
	}

	// 2026-07-24: URSM v2 运行时状态注入。SQL 只查拓扑/配置，运行时字段
	// (Available / CoolUntil / FailStreak / SR5m / LatP95Ms) 由 URSM v2 store
	// 持有。如果 h.ursmV2 不可用或 ModeOff / not ready / pipeline error，
	// 静默降级为 DB 字段填默认值（available=true / circuit_state=closed /
	// fail_streak=0 / success_rate=0.9 / p95_latency_ms=9999），保留旧的
	// resolve 行为。
	//
	// T4 保护性拒绝契约：URSM v2 Redis miss ⇒ Available=false。权威
	// 模式下 resolve 仅把真正拿到的 NodeView 标为可路由；miss 会在
	// applyURSMOverlay 中明确呈现为 runtime_state=missing。

	ursmManager := h.ursmV2
	applyResolveDefaults := func(candidates []resolveCandidate) {
		for i := range candidates {
			resolveRuntimeDefaults(&candidates[i])
		}
	}
	switch {
	case ursmManager == nil, ursmManager.Mode() == api.ModeOff:
		applyResolveDefaults(candidates)
	case !ursmManager.Ready(ctx):
		slog.Debug("routing resolve: ursm v2 not ready; runtime state is unknown")
		markResolveRuntimeUnknown(candidates, "ursm_not_ready")

	default:
		seeds := make([]v2.CandidateSeed, 0, len(candidates))
		for _, c := range candidates {
			var priceIn, priceOut float64
			if c.UnitPriceInPer1M != nil {
				priceIn = *c.UnitPriceInPer1M
			}
			if c.UnitPriceOutPer1M != nil {
				priceOut = *c.UnitPriceOutPer1M
			}
			seeds = append(seeds, v2.CandidateSeed{
				ProviderID:   c.ProviderID,
				CredentialID: c.CredentialID,
				RawModel:     c.ModelName,
				Canonical:    c.StandardizedName,
				TenantID:     c.TenantID,
				PriceIn:      priceIn,
				PriceOut:     priceOut,
				BillingMode:  c.BillingMode,
				Trust:        0,
				BaseURLMs:    0,
			})
		}
		views, err := ursmManager.FilterAndScore(ctx, seeds)
		if err != nil {
			slog.Warn("routing resolve: ursm v2 filter failed; runtime state is unknown",
				"error", err.Error(), "model_count", len(seeds))
			markResolveRuntimeUnknown(candidates, "ursm_query_failed")
		} else {
			if len(views) != len(candidates) {
				slog.Warn("routing resolve: ursm v2 partial result",
					"got", len(views), "want", len(candidates))
			}
			applyURSMOverlay(candidates, views, time.Now())
		}
	}

	toProviderCandidate := func(c resolveCandidate) provider.Candidate {
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
	// Keep the persisted manual order authoritative on this page. Composite
	// score remains a deterministic tie-breaker for candidates that share a
	// priority, while unavailable candidates stay in the same ordered list.
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.ManualPriority != b.ManualPriority {
			return a.ManualPriority < b.ManualPriority
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		return executors.CompareCandidatePriority(toProviderCandidate(a), toProviderCandidate(b))
	})

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
	if len(candidates) > 0 {
		// 2026-06-19 audit: if the SQL matched a variant that is
		// different from the normalized form, surface ':variant' so
		// admins know a cross-form hit landed.
		if strings.EqualFold(rawModels[0], normalizedModel) {
			resolutionPath = "canonical"
		} else {
			resolutionPath = "canonical:variant"
		}
	}
	// Reorder revision: only meaningful when the resolve hits exactly one
	// raw_model — mixed aliases or canonical hits intentionally leave the
	// field empty so the UI keeps reordering disabled. We use the
	// persistent scope revision (migration 541) so the value is monotonic
	// across concurrent writers; the dashboard's drag-and-drop relies on
	// the integer in front of the colon for 409 detection.
	//
	// Errors loading the revision are not fatal: the dashboard still works,
	// just without drag-and-drop, so we log and continue with an empty
	// token. An empty scope (no rows yet) also yields an empty token —
	// loadScopeRevision returns the zero value when the row is absent.
	var reorderRevision string
	if len(candidates) > 0 {
		firstRaw := strings.TrimSpace(candidates[0].ModelName)
		singleRaw := firstRaw != ""
		for _, c := range candidates[1:] {
			if c.ModelName != candidates[0].ModelName {
				singleRaw = false
				break
			}
		}
		if singleRaw {
			_, rev, revErr := fetchReorderScope(ctx, h.db, firstRaw, false)
			if revErr != nil {
				slog.Warn("routing resolve: reorder scope fetch failed; revision omitted",
					"raw_model", firstRaw, "error", revErr.Error())
			} else {
				reorderRevision = rev.Raw
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_model":     model,
		"canonical_name":   rawModels[0],
		"canonical_id":     nil,
		"resolution_path":  resolutionPath,
		"raw_models":       rawModels,
		"plan_order":       []any{},
		"candidates":       candidates,
		"reorder_revision": reorderRevision,
	})
}

// (applyURSMv2Overrides inlined below — see handler body; it must be a
// closure because the local `candidate` type is private to the handler.)

// isRoutable and blockReason work on the anonymous candidate struct via an
// interface — the struct is defined locally inside handleListCandidates.
// L-3: replaced the stubs that always returned true / "state check".

type routableCandidate interface { //nolint:unused
	getAvailable() bool
	getLifecycleStatus() string
	getAvailabilityState() string
	getQuotaState() string
	getCircuitState() string
}

func isRoutable(c interface{}) bool { //nolint:unused
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

func blockReason(c interface{}) string { //nolint:unused
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

// handleRoutingCandidateBindingUpdate updates credential_model_bindings fields
// that influence routing order (manual_priority / routing_tier / weight).
// It is intentionally narrow: only those three fields can be PATCHed here,
// so admin mistakes stay inside the routing-sorted surface area and don't
// silently flip a credential's lifecycle / availability / circuit flags.
//
// Path: PATCH /api/routing/candidate-binding/{credential_id}?raw_model=...
// Body: { manual_priority?: int, routing_tier?: int, weight?: int }
//
// Authorization: super_admin only (registered via h.superAdmin).
// Audit: every successful write appends a row to routing_audit_log.
func (h *Handler) handleRoutingCandidateBindingUpdate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	credIDStr := strings.TrimPrefix(r.URL.Path, "/api/routing/candidate-binding/")
	credID, err := strconv.Atoi(credIDStr)
	if err != nil || credID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid credential_id")
		return
	}
	rawModel := queryString(r, "raw_model")
	if rawModel == "" {
		writeError(w, http.StatusBadRequest, "raw_model query parameter required")
		return
	}

	var req struct {
		ManualPriority *int `json:"manual_priority"`
		RoutingTier    *int `json:"routing_tier"`
		Weight         *int `json:"weight"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.ManualPriority == nil && req.RoutingTier == nil && req.Weight == nil {
		writeError(w, http.StatusBadRequest, "at least one of manual_priority / routing_tier / weight required")
		return
	}
	if req.RoutingTier != nil && (*req.RoutingTier < 0 || *req.RoutingTier > 9) {
		writeError(w, http.StatusBadRequest, "routing_tier must be in [0,9]")
		return
	}
	if req.Weight != nil && (*req.Weight < 0 || *req.Weight > 10000) {
		writeError(w, http.StatusBadRequest, "weight must be in [0,10000]")
		return
	}
	if req.ManualPriority != nil && (*req.ManualPriority < 0 || *req.ManualPriority > 99) {
		writeError(w, http.StatusBadRequest, "manual_priority must be in [0,99]")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Lookup the binding_id for (credential_id, raw_model_name) — the
	// resolve page hands callers those two keys, never the surrogate
	// binding id. Resolve via provider_models.raw_model_name so we don't
	// rely on the inferred provider-side match.
	var bindingID int
	if err := h.db.QueryRow(ctx, `
		SELECT cmb.id
		FROM credential_model_bindings cmb
		JOIN provider_models pm ON pm.id = cmb.provider_model_id
		WHERE cmb.credential_id = $1
		  AND pm.raw_model_name = $2
		LIMIT 1
	`, credID, rawModel).Scan(&bindingID); err != nil {
		writeError(w, http.StatusNotFound, "binding not found for credential/model pair")
		return
	}

	// Static parameterised UPDATE with COALESCE so callers can PATCH a
	// single field without wiping the others. Same defensive shape used
	// in handleRoutingPolicy — no dynamic SQL building.
	if _, err := h.db.Exec(ctx, `
		UPDATE credential_model_bindings SET
			manual_priority = COALESCE($1::int, manual_priority),
			routing_tier    = COALESCE($2::int, routing_tier),
			weight          = COALESCE($3::int, weight),
			updated_at      = NOW()
		WHERE id = $4
	`, req.ManualPriority, req.RoutingTier, req.Weight, bindingID); err != nil {
		writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
		return
	}

	// Audit log: keep before/after so the routing_audit_log table holds
	// enough context for post-mortem diffs (rule 36 alignment).
	actor := r.Header.Get("X-Admin-User")
	if actor == "" {
		actor = r.RemoteAddr
	}
	beforeAfter := map[string]any{
		"credential_id":   credID,
		"binding_id":      bindingID,
		"raw_model_name":  rawModel,
		"manual_priority": req.ManualPriority,
		"routing_tier":    req.RoutingTier,
		"weight":          req.Weight,
	}
	h.logAudit(r, "routing_candidate_binding_update", beforeAfter)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":    "updated",
		"binding_id": bindingID,
		"actor":      actor,
	})
}

const maxRoutingCandidateReorderItems = 99

// reorderScopeSQL selects every binding row for an exact raw_model in
// deterministic id order. The same SQL backs the resolve endpoint's
// reorder_revision helper, so the revision hash stays aligned with the
// scope the writer will mutate.
const reorderScopeSQL = `
SELECT cmb.id,
       cmb.credential_id,
       cmb.manual_priority,
       cmb.updated_at
FROM credential_model_bindings cmb
JOIN provider_models pm ON pm.id = cmb.provider_model_id
WHERE pm.raw_model_name = $1
ORDER BY cmb.id
`

// reorderScopeRow is one ordered entry of the reorder scope. Sorted by
// id before hashing so concurrent transactions agree on the digest.
type reorderScopeRow struct {
	ID             int64
	CredentialID   int64
	ManualPriority int
	UpdatedAt      time.Time
}

// pgxQueryRower is the read surface shared by *pgxpool.Pool and pgx.Tx.
type pgxQueryRower interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// fetchReorderScope returns every binding row for rawModel in id order.
// PROBE-MARKER-2026-08-19-0338: file should have loadScopeRevision helper below.
func fetchReorderScope(ctx context.Context, q pgxQueryRower, rawModel string, lock bool) ([]reorderScopeRow, scopeRevision, error) {
	sqlText := reorderScopeSQL
	if lock {
		sqlText = sqlText + "\nFOR UPDATE OF cmb"
	}
	rows, err := q.Query(ctx, sqlText, rawModel)
	if err != nil {
		return nil, scopeRevision{}, err
	}
	defer rows.Close()
	out := make([]reorderScopeRow, 0)
	for rows.Next() {
		var r reorderScopeRow
		if err := rows.Scan(&r.ID, &r.CredentialID, &r.ManualPriority, &r.UpdatedAt); err != nil {
			return nil, scopeRevision{}, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, scopeRevision{}, err
	}
	rev, err := loadScopeRevision(ctx, q, rawModel)
	if err != nil {
		return nil, scopeRevision{}, err
	}
	return out, rev, nil
}

// scopeRevision is the persistent, monotonic version of a candidate-binding
// scope. The Raw form is the wire payload sent to clients; Version is the
// integer used for 409 detection.
type scopeRevision struct {
	Version int64
	Hash    string // 64-char SHA-256 hex; empty when no rows exist for the scope
	Raw     string // "<version>:<hash>" (or "" when the scope has no revision row yet)
}

func formatScopeRevision(version int64, hash string) string {
	if version <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%s", version, hash)
}

// loadScopeRevision reads the persistent scope revision row. Missing rows
// are NOT errors — a scope that has never been bumped returns the zero value
// and a nil error so the caller can decide whether the empty token is
// acceptable (resolve) or fatal (reorder).
func loadScopeRevision(ctx context.Context, q pgxQueryRower, rawModel string) (scopeRevision, error) {
	var version int64
	var hash string
	err := q.QueryRow(ctx, `
		SELECT scope_version, scope_hash
		  FROM public.candidate_binding_scope_revision
		 WHERE raw_model = $1
	`, rawModel).Scan(&version, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return scopeRevision{}, nil
	}
	if err != nil {
		return scopeRevision{}, err
	}
	return scopeRevision{
		Version: version,
		Hash:    hash,
		Raw:     formatScopeRevision(version, hash),
	}, nil
}

// parseScopeRevision parses the wire form "<version>:<hash>" back into a
// scopeRevision. The shape is forward-compatible: extra ":" segments after
// the hash are preserved verbatim in Raw.
func parseScopeRevision(token string) (scopeRevision, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return scopeRevision{}, fmt.Errorf("malformed revision: empty token")
	}
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return scopeRevision{}, fmt.Errorf("malformed revision %q: expected \"<version>:<hash>\"", token)
	}
	v, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || v <= 0 {
		return scopeRevision{}, fmt.Errorf("malformed revision %q: bad version", token)
	}
	return scopeRevision{Version: v, Hash: parts[1], Raw: token}, nil
}

// scopeRevisionsEqual returns true when two revisions refer to the same
// committed state.
func scopeRevisionsEqual(a, b scopeRevision) bool {
	if a.Version != b.Version {
		return false
	}
	if a.Version == 0 {
		return true
	}
	return secureEqualString(a.Hash, b.Hash)
}

// candidateReorderRevision hashes the scope rows so the writer can detect
// membership or priority drift between the client's snapshot and the
// locked current state. UpdatedAt is included so direct PATCH or legacy
// view-trigger writes invalidate the digest.
func candidateReorderRevision(rows []reorderScopeRow) (string, error) {
	sorted := make([]reorderScopeRow, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	hasher := sha256.New()
	for _, r := range sorted {
		if _, err := fmt.Fprintf(hasher, "%d|%d|%d|%s\n",
			r.ID, r.CredentialID, r.ManualPriority,
			r.UpdatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// secureEqualString compares opaque revsions in constant time so callers
// can't infer match probability from response timing.
func secureEqualString(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func isPgSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40001"
}

func isPgDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

// validateReorderCompleteness rejects partial or mismatched submissions:
// the request must cover every binding currently in scope.
func validateReorderCompleteness(scope []reorderScopeRow, items []routingCandidateReorderItem) error {
	if len(scope) != len(items) {
		return fmt.Errorf("incomplete candidate set, refetch and retry (scope=%d, submitted=%d)", len(scope), len(items))
	}
	scopeIDs := make(map[int64]struct{}, len(scope))
	for _, r := range scope {
		scopeIDs[r.CredentialID] = struct{}{}
	}
	seenIDs := make(map[int64]struct{}, len(items))
	for _, it := range items {
		if _, ok := scopeIDs[int64(it.CredentialID)]; !ok {
			return fmt.Errorf("incomplete candidate set, refetch and retry (credential_id=%d not in scope)", it.CredentialID)
		}
		seenIDs[int64(it.CredentialID)] = struct{}{}
	}
	if len(seenIDs) != len(scopeIDs) {
		return fmt.Errorf("incomplete candidate set, refetch and retry (missing credentials)")
	}
	return nil
}

// applyReorderUpdate writes the new priorities in a single statement,
// parameterised in id order so concurrent transactions lock rows in the
// same sequence and avoid reverse-order deadlocks.
func applyReorderUpdate(ctx context.Context, tx pgx.Tx, scope []reorderScopeRow, items []routingCandidateReorderItem) error {
	if len(scope) == 0 {
		return nil
	}
	picked := make(map[int64]int, len(items))
	for _, it := range items {
		picked[int64(it.CredentialID)] = it.ManualPriority
	}
	args := make([]any, 0, 2*len(scope))
	values := make([]string, 0, len(scope))
	for _, r := range scope {
		priority, ok := picked[r.CredentialID]
		if !ok {
			return fmt.Errorf("missing priority for credential_id=%d", r.CredentialID)
		}
		values = append(values, fmt.Sprintf("($%d::bigint, $%d::int)", len(args)+1, len(args)+2))
		args = append(args, r.ID, priority)
	}
	sqlText := fmt.Sprintf(`
		UPDATE credential_model_bindings
		SET manual_priority = v.priority, updated_at = NOW()
		FROM (VALUES %s) AS v(id, priority)
		WHERE credential_model_bindings.id = v.id
	`, strings.Join(values, ","))
	_, err := tx.Exec(ctx, sqlText, args...)
	return err
}

type routingCandidateReorderItem struct {
	CredentialID   int    `json:"credential_id"`
	RawModel       string `json:"raw_model"`
	ManualPriority int    `json:"manual_priority"`
}

type routingCandidateReorderRequest struct {
	// RawModel scopes the entire reorder to one exact provider_models.raw_model_name.
	// Mixed-model submissions are rejected to keep the write path atomic.
	RawModel string `json:"raw_model"`
	// ExpectedRevision is the opaque token the client received from
	// /api/routing/resolve. The handler locks the scope, recomputes the
	// revision, and rejects stale clients with HTTP 409.
	ExpectedRevision string                        `json:"expected_revision"`
	Items            []routingCandidateReorderItem `json:"items"`
}

func validateRoutingCandidateReorder(req routingCandidateReorderRequest) string {
	if len(req.Items) == 0 {
		return "items must not be empty"
	}
	if len(req.Items) > maxRoutingCandidateReorderItems {
		return "too many items"
	}
	seenBindings := make(map[string]struct{}, len(req.Items))
	seenPriorities := make(map[int]struct{}, len(req.Items))
	for _, item := range req.Items {
		if strings.TrimSpace(item.RawModel) == "" {
			return "raw_model is required for every item"
		}
		if item.CredentialID <= 0 {
			return "credential_id must be positive"
		}
		if item.ManualPriority < 1 || item.ManualPriority > len(req.Items) {
			return "manual_priority must be contiguous starting at 1"
		}
		bindingKey := fmt.Sprintf("%d:%s", item.CredentialID, strings.TrimSpace(item.RawModel))
		if _, ok := seenBindings[bindingKey]; ok {
			return "credential_id and raw_model must be unique"
		}
		if _, ok := seenPriorities[item.ManualPriority]; ok {
			return "manual_priority must be unique"
		}
		seenBindings[bindingKey] = struct{}{}
		seenPriorities[item.ManualPriority] = struct{}{}
	}
	for priority := 1; priority <= len(req.Items); priority++ {
		if _, ok := seenPriorities[priority]; !ok {
			return "manual_priority must be contiguous starting at 1"
		}
	}
	return ""
}

// handleRoutingCandidateBindingReorder persists the complete resolve-list order
// in one transaction. The write path enforces three invariants:
//
//  1. The reorder targets exactly one raw_model (the top-level RawModel field).
//     Mixed-model submissions are rejected at validation time.
//  2. The submission covers every credential currently bound to that raw_model.
//     A subset write would silently orphan sibling rows.
//  3. The client's ExpectedRevision matches the locked current scope state.
//     Mismatches indicate a stale view and surface as HTTP 409 so the UI can
//     refetch before retrying.
//
// Authorization: super_admin only (registered via h.superAdmin).
// Concurrency: SERIALIZABLE isolation; every binding row is locked with
// FOR UPDATE OF cmb in id order before any UPDATE, which both serialises
// concurrent writers and prevents reverse-order deadlocks.
// Audit: written inside the same transaction so the audit row commits or
// rolls back together with the priority change.
func (h *Handler) handleRoutingCandidateBindingReorder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req routingCandidateReorderRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.RawModel = strings.TrimSpace(req.RawModel)
	if req.RawModel == "" {
		writeError(w, http.StatusBadRequest, "raw_model is required")
		return
	}
	req.ExpectedRevision = strings.TrimSpace(req.ExpectedRevision)
	if req.ExpectedRevision == "" {
		writeError(w, http.StatusBadRequest, "expected_revision is required")
		return
	}
	for i := range req.Items {
		itemRaw := strings.TrimSpace(req.Items[i].RawModel)
		switch {
		case itemRaw == "":
			req.Items[i].RawModel = req.RawModel
		case !strings.EqualFold(itemRaw, req.RawModel):
			writeError(w, http.StatusBadRequest, "raw_model mismatch between request and item")
			return
		default:
			req.Items[i].RawModel = itemRaw
		}
	}
	if validationErr := validateRoutingCandidateReorder(req); validationErr != "" {
		writeError(w, http.StatusBadRequest, validationErr)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	tx, err := h.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "begin reorder transaction failed")
		return
	}
	defer tx.Rollback(ctx)

	// Plumb the audit-attributable actor into the session GUC so the
	// migration 541 trigger records it on candidate_binding_scope_revision.
	// We do this BEFORE the first SELECT so the revision row written by
	// our own UPDATE is attributed to this actor rather than NULL.
	actor := requestActor(r)
	if _, setErr := tx.Exec(ctx, "SELECT set_config('app.actor', $1, true)", actor); setErr != nil {
		writeError(w, http.StatusInternalServerError, "set app.actor guc failed: "+setErr.Error())
		return
	}

	scope, currentRev, err := fetchReorderScope(ctx, tx, req.RawModel, true)
	if err != nil {
		if isPgSerializationFailure(err) || isPgDeadlock(err) {
			writeError(w, http.StatusConflict, "transient ordering conflict, retry")
			return
		}
		writeError(w, http.StatusInternalServerError, "load reorder scope failed: "+err.Error())
		return
	}
	if len(scope) == 0 {
		writeError(w, http.StatusNotFound, "no candidate bindings found for raw_model")
		return
	}
	// The persistent scope revision (migration 541) is the source of truth
	// for 409 detection. We parse the client token once and compare on
	// the integer version (primary) plus the hash (defence-in-depth: catches
	// scope contents drifting without a version bump, which would indicate
	// the trigger is broken).
	expectedRev, parseErr := parseScopeRevision(req.ExpectedRevision)
	if parseErr != nil || !scopeRevisionsEqual(expectedRev, currentRev) {
		writeError(w, http.StatusConflict, "stale candidate binding set, refetch and retry")
		return
	}
	if err := validateReorderCompleteness(scope, req.Items); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := applyReorderUpdate(ctx, tx, scope, req.Items); err != nil {
		writeError(w, http.StatusInternalServerError, "apply reorder failed: "+err.Error())
		return
	}
	// The trigger fired by applyReorderUpdate has already advanced
	// scope_version inside the same statement; we re-read the row so the
	// response carries the fresh token the client must echo on its next
	// PATCH. We deliberately do NOT cache the pre-write currentRev here.
	nextRev, err := loadScopeRevision(ctx, tx, req.RawModel)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "reload revision failed: "+err.Error())
		return
	}
	if err := logAuditExec(ctx, tx, actor,
		"routing_candidate_binding_reorder", map[string]any{
			"raw_model":         req.RawModel,
			"expected_revision": req.ExpectedRevision,
			"scope_version":     nextRev.Version,
			"items":             req.Items,
		}); err != nil {
		writeError(w, http.StatusInternalServerError, "audit insert failed: "+err.Error())
		return
	}
	if err := tx.Commit(ctx); err != nil {
		if isPgSerializationFailure(err) || isPgDeadlock(err) {
			writeError(w, http.StatusConflict, "transient ordering conflict, retry")
			return
		}
		writeError(w, http.StatusInternalServerError, "commit reorder failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"message":           "updated",
		"raw_model":         req.RawModel,
		"expected_revision": nextRev.Raw,
		"items":             req.Items,
	})
}

const forceEnableCredentialSQL = `
	UPDATE credentials SET
		manual_disabled = false,
		lifecycle_status = 'active',
		availability_state = 'ready',
		availability_recover_at = NULL,
		quota_state = 'ok',
		quota_recover_at = NULL,
		circuit_state = 'closed',
		cooling_until = NULL,
		health_status = 'healthy',
		consecutive_failures = 0,
		state_reason_code = NULL,
		state_reason_detail = $2,
		state_updated_at = NOW()
	WHERE id = $1
`

// handleEmergencyRepair handles emergency repair actions for credentials.
// PATCH /api/routing/emergency-repair
// Body: {"credential_id": int, "raw_model": string, "action": string}
// Actions: "force_enable" | "force_disable" | "clear_circuit" | "reset_errors"
//
// Authorization: super_admin only (registered via h.superAdmin).
// Audit: every successful action appends a row to routing_audit_log.
func (h *Handler) handleEmergencyRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req struct {
		CredentialID int    `json:"credential_id"`
		RawModel     string `json:"raw_model"`
		Action       string `json:"action"`
		Reason       string `json:"reason"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.CredentialID <= 0 {
		writeError(w, http.StatusBadRequest, "credential_id must be positive")
		return
	}
	if req.Reason == "" {
		writeError(w, http.StatusBadRequest, "reason is required for audit trail")
		return
	}

	validActions := map[string]bool{
		"force_enable":  true,
		"force_disable": true,
		"clear_circuit": true,
		"reset_errors":  true,
	}
	if !validActions[req.Action] {
		writeError(w, http.StatusBadRequest, "action must be one of: force_enable, force_disable, clear_circuit, reset_errors")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	ursmTenantID := ""
	// 2026-08-15: the tenant lookup no longer requires RawModel — the
	// whole-credential path (RawModel == "") now also clears URSM v2 state
	// per binding model (see resetInMemoryNodeState + the URSM loop below),
	// so the tenant ID is always needed when URSM v2 is wired.
	if h.ursmV2 != nil {
		if err := h.db.QueryRow(ctx,
			"SELECT COALESCE(tenant_id, '') FROM credentials WHERE id = $1",
			req.CredentialID,
		).Scan(&ursmTenantID); err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "credential not found")
			} else {
				writeError(w, http.StatusInternalServerError, "credential tenant lookup failed: "+err.Error())
			}
			return
		}
	}

	// Get actor for audit: prefer authenticated username from AuthContext,
	// fallback to r.RemoteAddr when not available (C4 修复).
	actor := r.RemoteAddr
	if auth := GetAuthContext(r); auth != nil && auth.Username != "" {
		actor = auth.Username
	}

	beforeAfter := map[string]any{
		"credential_id":  req.CredentialID,
		"raw_model":      req.RawModel,
		"action":         req.Action,
		"reason":         req.Reason,
		"ursm_tenant_id": ursmTenantID,
	}

	switch req.Action {
	case "force_enable":
		// Force-enable must make the node actually routable again.
		// 2026-07-24: previously only cleared manual_disabled + cmb.available.
		// Nodes blocked by node_probe_state backoff (unavailable_reason=
		// node_probe_failed) stayed red after HTTP 200 — observed on NVIDIA NIM
		// (cred 8/18/19/23 · minimaxai/minimax-m3) and 普联 (cred 29 · glm-5.2).
		// Now also: reset availability/circuit/failures + node_probe_state.
		var currentDisabled bool
		var availState string
		var providerID int
		err := h.db.QueryRow(ctx,
			`SELECT COALESCE(manual_disabled, false), COALESCE(availability_state, 'ready'),
			        COALESCE(provider_id, 0)
			 FROM credentials WHERE id = $1`,
			req.CredentialID,
		).Scan(&currentDisabled, &availState, &providerID)
		if err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "credential not found")
			} else {
				writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			}
			return
		}

		tx, err := h.db.Begin(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "begin tx failed: "+err.Error())
			return
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx, forceEnableCredentialSQL,
			req.CredentialID, "emergency force_enable: "+req.Reason); err != nil {
			writeError(w, http.StatusInternalServerError, "update credentials failed: "+err.Error())
			return
		}

		var cmbRows int64
		if req.RawModel != "" {
			tag, err := tx.Exec(ctx, `
				UPDATE credential_model_bindings cmb
				SET available = TRUE,
					unavailable_reason = NULL,
					unavailable_at = NULL,
					unavailable_recover_at = NULL,
					updated_at = NOW()
				FROM provider_models pm
				WHERE cmb.credential_id = $1
				  AND pm.id = cmb.provider_model_id
				  AND pm.raw_model_name = $2
			`, req.CredentialID, req.RawModel)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "update cmb failed: "+err.Error())
				return
			}
			cmbRows = tag.RowsAffected()
		}

		// Clear NodeProbeWorker backoff so v_routable_credential_models
		// drops node_probe_failed immediately (see migration 417).
		var probeRows int64
		if req.RawModel != "" {
			tag, err := tx.Exec(ctx, `
				UPDATE node_probe_state SET
					last_direct_ok = TRUE,
					last_gateway_ok = TRUE,
					last_err_code = NULL,
					last_err_detail = NULL,
					next_retry_at = now(),
					next_retry_seconds = 0,
					consecutive_failures = 0,
					paused = FALSE,
					in_flight_until = NULL,
					updated_at = now()
				WHERE credential_id = $1 AND raw_model_name = $2
			`, req.CredentialID, req.RawModel)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "reset node_probe_state failed: "+err.Error())
				return
			}
			probeRows = tag.RowsAffected()
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE node_probe_state SET
					last_direct_ok = TRUE,
					last_gateway_ok = TRUE,
					last_err_code = NULL,
					last_err_detail = NULL,
					next_retry_at = now(),
					next_retry_seconds = 0,
					consecutive_failures = 0,
					paused = FALSE,
					in_flight_until = NULL,
					updated_at = now()
				WHERE credential_id = $1
			`, req.CredentialID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "reset node_probe_state failed: "+err.Error())
				return
			}
			probeRows = tag.RowsAffected()
		}

		// Clear model_probe_state broken_confirmed (2026-08-13): the SQL view
		// v_routable_credential_models also gates routability on
		// model_probe_state.state != 'broken_confirmed', and
		// reconcileBrokenConfirmedBindings re-marks the cmb unavailable every
		// probe cycle while it stays broken_confirmed. Without this clear,
		// force_enable leaves a broken_confirmed node non-routable despite the
		// credential/cmb/node_probe resets above. 'recovering' is immediately
		// routable (only 'broken_confirmed' is gated) and matches the
		// BrokenProbeReviver semantics.
		var modelProbeRows int64
		if req.RawModel != "" {
			tag, err := tx.Exec(ctx, `
				UPDATE model_probe_state SET
					state = 'recovering',
					consecutive_failures = 0,
					next_retry_at = now(),
					last_state_change_at = now()
				WHERE credential_id = $1 AND raw_model_name = $2
				  AND state = 'broken_confirmed'
			`, req.CredentialID, req.RawModel)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "reset model_probe_state failed: "+err.Error())
				return
			}
			modelProbeRows = tag.RowsAffected()
		} else {
			tag, err := tx.Exec(ctx, `
				UPDATE model_probe_state SET
					state = 'recovering',
					consecutive_failures = 0,
					next_retry_at = now(),
					last_state_change_at = now()
				WHERE credential_id = $1
				  AND state = 'broken_confirmed'
			`, req.CredentialID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "reset model_probe_state failed: "+err.Error())
				return
			}
			modelProbeRows = tag.RowsAffected()
		}

		if err := tx.Commit(ctx); err != nil {
			writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
			return
		}
		beforeAfter["previous_manual_disabled"] = currentDisabled
		beforeAfter["previous_availability_state"] = availState
		beforeAfter["new_manual_disabled"] = false
		beforeAfter["new_availability_state"] = "ready"
		beforeAfter["cmb_available"] = cmbRows > 0 || req.RawModel == ""
		beforeAfter["cmb_rows_updated"] = cmbRows
		beforeAfter["node_probe_rows_updated"] = probeRows
		beforeAfter["model_probe_rows_updated"] = modelProbeRows

		// 2026-08-15: the DB transaction above only reaches the persistent
		// layers. The request hot path also consults in-process / Redis state
		// (circuit breaker, fpslot NodeState cooldown, legacy credentialstate
		// cache) that would keep filtering this node out for up to 5 minutes
		// after force_enable returned 200. Reset them now.
		resetModels, memOutcome := h.resetInMemoryNodeState(ctx, req.CredentialID, providerID, req.RawModel, true)
		for k, v := range memOutcome {
			beforeAfter[k] = v
		}

		// URSM v2: clear manual_hold via ApplyAdmin, AND wipe any
		// residual disabled/fail_streak/cool_until_ms via ClearState.
		// 2026-08-15: when RawModel is empty (whole-credential repair) the
		// per-model loop below covers every binding model instead of
		// skipping URSM entirely.
		if h.ursmV2 != nil {
			disabled := false
			applied, cleared := 0, 0
			for _, m := range resetModels {
				adminAction := api.AdminAction{
					Scope:          api.ScopeNode,
					CredentialID:   req.CredentialID,
					RawModel:       m,
					TenantID:       ursmTenantID,
					ManualDisabled: &disabled,
					Reason:         req.Reason,
					Actor:          actor,
					IssuedAtMs:     time.Now().UnixMilli(),
				}
				if err := h.ursmV2.ApplyAdmin(ctx, adminAction); err != nil {
					slog.Warn("emergency_repair: ursm.v2 apply_admin failed", "error", err, "cred", req.CredentialID, "model", m)
				} else {
					applied++
				}
				if err := h.ursmV2.ClearStateForTenant(ctx, ursmTenantID, req.CredentialID, m); err != nil {
					slog.Warn("emergency_repair: ursm.v2 clear_state failed", "error", err, "cred", req.CredentialID, "model", m)
				} else {
					cleared++
				}
			}
			beforeAfter["ursm_v2_admin_applied"] = len(resetModels) > 0 && applied == len(resetModels)
			beforeAfter["ursm_v2_cleared"] = len(resetModels) > 0 && cleared == len(resetModels)
			beforeAfter["ursm_v2_models_covered"] = len(resetModels)
		}

	case "force_disable":
		// Set manual_disabled flag on credentials table.
		// C+ 修复: now wrap UPDATE + state_updated_at in same style as
		// the other actions for symmetry; cmb.available is NOT flipped
		// because the binding becomes unavailable via v_routable_credential_models
		// reading c.manual_disabled (view evaluates c, not cmb, for manual disable).
		var currentDisabled bool
		err := h.db.QueryRow(ctx,
			"SELECT COALESCE(manual_disabled, false) FROM credentials WHERE id = $1",
			req.CredentialID,
		).Scan(&currentDisabled)
		if err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "credential not found")
			} else {
				writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			}
			return
		}
		if _, err := h.db.Exec(ctx, `
			UPDATE credentials SET
				manual_disabled = true,
				state_updated_at = NOW()
			WHERE id = $1
		`, req.CredentialID); err != nil {
			writeError(w, http.StatusInternalServerError, "update failed: "+err.Error())
			return
		}
		beforeAfter["previous_manual_disabled"] = currentDisabled
		beforeAfter["new_manual_disabled"] = true

		// Also set manual hold in URSM v2 Redis
		if h.ursmV2 != nil && req.RawModel != "" {
			disabled := true
			adminAction := api.AdminAction{
				Scope:          api.ScopeNode,
				CredentialID:   req.CredentialID,
				RawModel:       req.RawModel,
				TenantID:       ursmTenantID,
				ManualDisabled: &disabled,
				Reason:         req.Reason,
				Actor:          actor,
				IssuedAtMs:     time.Now().UnixMilli(),
			}
			if err := h.ursmV2.ApplyAdmin(ctx, adminAction); err != nil {
				slog.Warn("emergency_repair: ursm.v2 apply_admin failed", "error", err, "cred", req.CredentialID)
				beforeAfter["ursm_v2_admin_applied"] = false
			} else {
				beforeAfter["ursm_v2_admin_applied"] = true
			}
		}

	case "clear_circuit":
		// Reset circuit_state to 'closed' on credentials + cmb row.
		// C1 修复: must also flip cmb.available back to TRUE and clear
		// unavailable_reason / unavailable_at / unavailable_recover_at,
		// because v_routable_credential_models reads cmb-side columns.
		var currentState *string
		var providerID int
		err := h.db.QueryRow(ctx,
			"SELECT circuit_state, COALESCE(provider_id, 0) FROM credentials WHERE id = $1",
			req.CredentialID,
		).Scan(&currentState, &providerID)
		if err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "credential not found")
			} else {
				writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			}
			return
		}
		previousState := "closed"
		if currentState != nil {
			previousState = *currentState
		}

		// Single transaction: credentials row + cmb row together.
		tx, err := h.db.Begin(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "begin tx failed: "+err.Error())
			return
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx, `
			UPDATE credentials SET
				circuit_state = 'closed',
				cooling_until = NULL,
				state_updated_at = NOW()
			WHERE id = $1
		`, req.CredentialID); err != nil {
			writeError(w, http.StatusInternalServerError, "update credentials failed: "+err.Error())
			return
		}
		if req.RawModel != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE credential_model_bindings cmb
				SET available = TRUE,
					unavailable_reason = NULL,
					unavailable_at = NULL,
					unavailable_recover_at = NULL,
					updated_at = NOW()
				FROM provider_models pm
				WHERE cmb.credential_id = $1
				  AND pm.id = cmb.provider_model_id
				  AND pm.raw_model_name = $2
			`, req.CredentialID, req.RawModel); err != nil {
				writeError(w, http.StatusInternalServerError, "update cmb failed: "+err.Error())
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
			return
		}
		beforeAfter["previous_circuit_state"] = previousState
		beforeAfter["new_circuit_state"] = "closed"
		beforeAfter["cmb_available"] = true

		// 2026-08-15: the request hot path uses the IN-PROCESS breaker
		// (domains/credential), not the DB column above. Reset it (and the
		// fpslot cooldown, which is the other cooling-style gate) so
		// clear_circuit is effective immediately.
		_, memOutcome := h.resetInMemoryNodeState(ctx, req.CredentialID, providerID, req.RawModel, false)
		for k, v := range memOutcome {
			beforeAfter[k] = v
		}

		// Also clear cooling state in URSM v2 Redis
		if h.ursmV2 != nil && req.RawModel != "" {
			if err := h.ursmV2.ClearStateForTenant(ctx, ursmTenantID, req.CredentialID, req.RawModel); err != nil {
				slog.Warn("emergency_repair: ursm.v2 clear_state failed", "error", err, "cred", req.CredentialID)
				beforeAfter["ursm_v2_cleared"] = false
			} else {
				beforeAfter["ursm_v2_cleared"] = true
			}
		}

	case "reset_errors":
		// Reset consecutive_failures on credentials + flip cmb row.
		// C2 修复: must also flip cmb.available back to TRUE so the binding
		// is immediately re-admitted to routing (binding-level cols are
		// what v_routable_credential_models consults).
		var currentFailures int
		err := h.db.QueryRow(ctx,
			"SELECT COALESCE(consecutive_failures, 0) FROM credentials WHERE id = $1",
			req.CredentialID,
		).Scan(&currentFailures)
		if err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, http.StatusNotFound, "credential not found")
			} else {
				writeError(w, http.StatusInternalServerError, "query failed: "+err.Error())
			}
			return
		}

		tx, err := h.db.Begin(ctx)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "begin tx failed: "+err.Error())
			return
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx, `
			UPDATE credentials SET
				consecutive_failures = 0,
				state_updated_at = NOW()
			WHERE id = $1
		`, req.CredentialID); err != nil {
			writeError(w, http.StatusInternalServerError, "update credentials failed: "+err.Error())
			return
		}
		if req.RawModel != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE credential_model_bindings cmb
				SET available = TRUE,
					unavailable_reason = NULL,
					unavailable_at = NULL,
					unavailable_recover_at = NULL,
					updated_at = NOW()
				FROM provider_models pm
				WHERE cmb.credential_id = $1
				  AND pm.id = cmb.provider_model_id
				  AND pm.raw_model_name = $2
			`, req.CredentialID, req.RawModel); err != nil {
				writeError(w, http.StatusInternalServerError, "update cmb failed: "+err.Error())
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			writeError(w, http.StatusInternalServerError, "commit failed: "+err.Error())
			return
		}
		beforeAfter["previous_consecutive_failures"] = currentFailures
		beforeAfter["new_consecutive_failures"] = 0
		beforeAfter["cmb_available"] = true

		// Also clear fail counters in URSM v2 Redis
		if h.ursmV2 != nil && req.RawModel != "" {
			if err := h.ursmV2.ClearStateForTenant(ctx, ursmTenantID, req.CredentialID, req.RawModel); err != nil {
				slog.Warn("emergency_repair: ursm.v2 clear_state failed", "error", err, "cred", req.CredentialID)
				beforeAfter["ursm_v2_cleared"] = false
			} else {
				beforeAfter["ursm_v2_cleared"] = true
			}
		}
	}

	// Write audit log
	h.logAudit(r, "emergency_repair."+req.Action, beforeAfter)

	// Invalidate routing caches so changes take effect immediately.
	invalidateRoutingCaches(ctx, h.db, "credentials", req.CredentialID)
	// The available-models catalog has an independent process-local cache.
	// Emergency repair changes credential/CMB visibility, so leave no stale
	// taxonomy response behind for the UI's next refresh.
	InvalidateAvailableModelsCache()

	// M2 修复: surface the URSM v2 outcome in the response so the UI
	// can warn when PG was updated but Redis state was skipped.
	ursmCleared, _ := beforeAfter["ursm_v2_cleared"].(bool)
	ursmAdmin, _ := beforeAfter["ursm_v2_admin_applied"].(bool)
	cmbAvailable, _ := beforeAfter["cmb_available"].(bool)
	cmbRows, _ := beforeAfter["cmb_rows_updated"].(int64)
	probeRows, _ := beforeAfter["node_probe_rows_updated"].(int64)

	writeJSON(w, http.StatusOK, map[string]any{
		"message":                 "emergency repair applied: " + req.Action,
		"credential_id":           req.CredentialID,
		"action":                  req.Action,
		"actor":                   actor,
		"cmb_available":           cmbAvailable,
		"cmb_rows_updated":        cmbRows,
		"node_probe_rows_updated": probeRows,
		"ursm_v2_admin_applied":   ursmAdmin,
		"ursm_v2_cleared":         ursmCleared,
	})
}

// resetInMemoryNodeState clears the state layers the emergency-repair DB
// transaction cannot reach: the in-process credential circuit breaker, the
// fpslot per-(credential, model) NodeState cooldown, and (optionally) the
// legacy credentialstate cache. Without this, force_enable / clear_circuit
// return HTTP 200 while the router still filters the node out until the
// in-memory cooling windows expire (breaker cooling / fpslot 300s cooldown /
// credentialstate Redis TTL up to 5 min) — the "上游可用但网关长期排除节点"
// complaint (see docs/会话优化v3/29 §A1).
//
// It returns the model list it covered (rawModel if given, otherwise every
// binding model of the credential) plus a outcome map for the audit trail.
// Every step is best-effort: a failure is logged and reported, never fatal.
func (h *Handler) resetInMemoryNodeState(ctx context.Context, credentialID, providerID int, rawModel string, includeCredState bool) ([]string, map[string]any) {
	outcome := map[string]any{}

	if h.circuitResetter != nil && providerID > 0 {
		h.circuitResetter.Reset(providerID, credentialID)
		outcome["in_memory_circuit_reset"] = true
	}

	models := make([]string, 0, 4)
	if rawModel != "" {
		models = append(models, rawModel)
	} else {
		rows, err := h.db.Query(ctx, `
			SELECT pm.raw_model_name
			FROM credential_model_bindings cmb
			JOIN provider_models pm ON pm.id = cmb.provider_model_id
			WHERE cmb.credential_id = $1
		`, credentialID)
		if err != nil {
			slog.Warn("emergency_repair: enumerate binding models failed",
				"error", err, "cred", credentialID)
		} else {
			for rows.Next() {
				var m string
				if err := rows.Scan(&m); err == nil {
					models = append(models, m)
				}
			}
			rows.Close()
		}
	}

	if h.fpSlots != nil {
		reset := 0
		for _, m := range models {
			// A zero NodeState is "usable": Disabled=false, no cooldown, empty
			// sliding window — router.filterHealthyNodes re-admits the node.
			if err := h.fpSlots.SetNodeState(ctx, &credentialfpslot.NodeState{
				CredentialID: credentialID,
				Model:        m,
			}); err == nil {
				reset++
			} else {
				slog.Warn("emergency_repair: reset fp node state failed",
					"error", err, "cred", credentialID, "model", m)
			}
		}
		if reset > 0 {
			outcome["fp_node_state_models_reset"] = reset
		}
	}

	if includeCredState && h.credStateRecoverer != nil {
		now := time.Now()
		for _, m := range models {
			// Probe-success semantics: a non-nil LastSuccessAt makes
			// UpdateFromProbe treat this as authoritative recovery evidence
			// (clears ConsecutiveFails / LastError) and flips Available=true.
			h.credStateRecoverer.UpdateFromProbe(ctx, &credentialstate.State{
				CredentialID:  credentialID,
				Model:         m,
				Available:     true,
				HealthStatus:  "healthy",
				LastSuccessAt: &now,
				LastUpdatedAt: now,
				Source:        "manual",
			})
		}
		outcome["cred_state_recovered_models"] = len(models)
	}

	return models, outcome
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
			currency, standardizedName                              *string
			providerID, credentialID, tier, weight, p95             int
			providerEnabled, available                              bool
			successRate, balanceUSD                                 *float64
			availabilityRecoverAt, quotaRecoverAt                   *time.Time
			effectiveAt, expiresAt, coolingUntil                    *time.Time
			priceIn, priceOut                                       *float64
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
			Series      string             `json:"series"`
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

// defaultPopularModelsLookupWindow is how far back we look in request_logs_hot
// for "popular models" suggestions unless the environment overrides it.
// request_logs_hot is the hot standalone
// table (rule 33 / migration 341) — older rows are migrated nightly into
// the monthly partitioned request_logs table by promote_request_logs_hot_to_partition.
// Querying request_logs_hot directly avoids the columnar monthly partitions
// that the previous request_logs_with_current_month UNION dragged in.
const defaultPopularModelsLookupWindow = 7 * 24 * time.Hour

// popularModelsHotSQL returns up to 10 (model_key, count) rows from
// request_logs_hot covering the configured lookup window.
//
// The cutoff is passed as a plan-time literal ($1) computed in Go so the
// hot-table scan is bounded by an index range on (ts) rather than the
// previous `NOW() - INTERVAL '7 days'` predicate that could not prune
// against the partitioned parent view.
const popularModelsHotSQL = `
SELECT model_key, cnt FROM (
    SELECT
        COALESCE(mc.canonical_name, mc2.canonical_name, rl.client_model) AS model_key,
        COUNT(*) AS cnt
    FROM request_logs_hot rl
    LEFT JOIN models_canonical mc ON mc.id = rl.canonical_id
    LEFT JOIN LATERAL (
        SELECT canonical_id
        FROM model_aliases
        WHERE raw_name = lower(rl.client_model)
          AND status = 'active'
        LIMIT 1
    ) ma ON TRUE
    LEFT JOIN models_canonical mc2 ON mc2.id = ma.canonical_id
    WHERE rl.ts >= $1
      AND rl.success = TRUE
      AND ($2 = '' OR rl.tenant_id = $2)
      AND rl.client_model IS NOT NULL AND rl.client_model != ''
    GROUP BY model_key
) u
ORDER BY cnt DESC
LIMIT 10`

// livePopularModels queries Redis for "currently accessed models" using the
// live-stream dimension queues indexed at llmgw:live:dim:index:global.
// Each lane queue key llmgw:live:dim:model:<normalized> has ZSET members
// (one per recent request-id in that lane). ZCARD is a cheap O(1) proxy
// for "currently in flight / recently active". Results are returned sorted
// by cardinality desc, capped at topLivePopularLimit.
//
// This is the fast path that lets the dashboard's "凭据路由模型" picker
// surface *what is being routed right now* without touching the request_logs
// tables at all. Best-effort: errors are swallowed and an empty slice
// is returned so callers fall back to the SQL path.
func livePopularModels(ctx context.Context, rdb *redis.Client, limit int) []popularModelEntry {
	if rdb == nil || limit <= 0 {
		return nil
	}
	keys, err := rdb.SMembers(ctx, liveStreamDimIndexKey("", true)).Result()
	if err != nil || len(keys) == 0 {
		return nil
	}
	const dimModelPrefix = liveStreamDimPrefix + "model:"
	type modelCount struct {
		name  string
		count int64
	}
	bucket := make([]modelCount, 0, len(keys))
	for _, key := range keys {
		if !strings.HasPrefix(key, dimModelPrefix) {
			continue
		}
		n, err := rdb.ZCard(ctx, key).Result()
		if err != nil || n <= 0 {
			continue
		}
		bucket = append(bucket, modelCount{
			name:  strings.TrimPrefix(key, dimModelPrefix),
			count: n,
		})
	}
	if len(bucket) == 0 {
		return nil
	}
	sort.Slice(bucket, func(i, j int) bool {
		if bucket[i].count != bucket[j].count {
			return bucket[i].count > bucket[j].count
		}
		return bucket[i].name < bucket[j].name
	})
	if len(bucket) > limit {
		bucket = bucket[:limit]
	}
	out := make([]popularModelEntry, 0, len(bucket))
	for _, b := range bucket {
		c := int(b.count)
		out = append(out, popularModelEntry{
			CanonicalName: b.name,
			DisplayName:   b.name,
			Source:        "live",
			Count:         &c,
		})
	}
	return out
}

// recentlyUsedModelsKeyPrefix namespaces one Redis ZSET per tenant. Each key
// records canonical model names hit by a real (non-probe) successful request
// over the last RecentlyUsedModelsTTL. This is the primary fast path for the
// "凭据路由模型" dashboard widget — O(log N) writes (ZINCRBY) on the
// request hot path, O(log N + M) reads (ZREVRANGE) here.
//
// Why a dedicated key (vs the live-stream dim queue ZCARD used by
// livePopularModels):
//   - TTL exactly matches the 7-day hot-popular window, no extra
//     "we measured 24h but the UI asks for 7d" drift.
//   - Writes are gated on is_probe=false + success=true so node
//     probes, model probes, and self-check tiles cannot pollute the
//     ranking.
//   - Score is the real request counter (not lane-queue cardinality
//     which mixes real requests with idle markers and capped lanes).
//   - Persistent across gateway restarts via AOF/RDB (lane queue
//     contents live in process memory only).
const (
	recentlyUsedModelsKeyPrefix = "llmgw:routing:recently_used_models:"
	RecentlyUsedModelsTTL       = 7 * 24 * time.Hour
	recentlyUsedModelsLimit     = 10
)

// recentlyUsedPopularModels reads the top-N recently-used canonical model
// names for one tenant. An empty tenant intentionally has no Redis aggregate:
// super-admin reads remain DB-backed and cannot be served by a global key.
// Best-effort: returns nil on any Redis error so callers fall back to SQL.
func recentlyUsedModelsKey(tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return ""
	}
	return recentlyUsedModelsKeyPrefix + tenantID
}

func recentlyUsedPopularModels(ctx context.Context, rdb *redis.Client, tenantID string, limit int) []popularModelEntry {
	key := recentlyUsedModelsKey(tenantID)
	if rdb == nil || key == "" || limit <= 0 {
		return nil
	}
	pairs, err := rdb.ZRevRangeWithScores(ctx, key, 0, int64(limit-1)).Result()
	if err != nil || len(pairs) == 0 {
		return nil
	}
	out := make([]popularModelEntry, 0, len(pairs))
	for _, p := range pairs {
		name, _ := p.Member.(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		c := int(p.Score)
		out = append(out, popularModelEntry{
			CanonicalName: name,
			DisplayName:   name,
			Source:        "recent",
			Count:         &c,
		})
	}
	return out
}

// RecordRecentlyUsedModel bumps the canonical model's score in the
// recently-used ZSET and refreshes the key TTL. Safe to call from
// the request hot path: errors are swallowed and reported through a
// counter (no log spam per request).
//
// Probe / selfcheck / pre-flight requests must pass isProbe=false so
// health-check traffic does not skew the dashboard ranking. tenantID is
// required so model popularity never crosses tenant boundaries.
func RecordRecentlyUsedModel(ctx context.Context, rdb *redis.Client, tenantID, canonical string, isProbe bool) {
	if rdb == nil || isProbe {
		return
	}
	key := recentlyUsedModelsKey(tenantID)
	if key == "" {
		return
	}
	canonical = normalizeModelKey(canonical)
	if canonical == "" || canonical == "unknown" {
		return
	}
	pipe := rdb.Pipeline()
	pipe.ZIncrBy(ctx, key, 1, canonical)
	pipe.Expire(ctx, key, RecentlyUsedModelsTTL)
	if _, err := pipe.Exec(ctx); err != nil {
		// Best-effort: do not log per-request (would flood). A future
		// counter hook (RecentlyUsedRedisErrors) can catch chronic issues.
		_ = err
	}
}

func (h *Handler) queryPopularModels(ctx context.Context, featuredModels []string, byCanonical map[string]*availableVersionEntry, tenantID string, limit int) []popularModelEntry {
	if limit <= 0 {
		return nil
	}
	popular := make([]popularModelEntry, 0, 32)
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

	// Live source: Redis dimension queues. Inserted before SQL so "currently
	// accessed models" take precedence over the slower 7-day aggregate when
	// the SQL path is cold / unavailable.
	if rc, ok := h.redisClient.(*redis.Client); ok {
		if tenantID == "" {
			for _, live := range livePopularModels(ctx, rc, 5) {
				add(live.CanonicalName, live.DisplayName, "live", live.Count)
			}
		}
		// Recent source: dedicated recently-used ZSET (TTL 7d, real request
		// counter, probe-gated). This is the primary fast path for the
		// dashboard — covers the same window as the SQL aggregate but at
		// Redis latency instead of a request_logs_hot scan.
		for _, recent := range recentlyUsedPopularModels(ctx, rc, tenantID, recentlyUsedModelsLimit) {
			add(recent.CanonicalName, recent.DisplayName, "recent", recent.Count)
		}
	}

	if len(popular) >= limit {
		return popular[:limit]
	}
	cutoff := time.Now().UTC().Add(-popularModelsLookupWindow())
	usageRows, err := h.db.Query(ctx, popularModelsHotSQL, cutoff, tenantID)
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
	if len(popular) > limit {
		return popular[:limit]
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
		tenantID := EffectiveTenantIDAll(r)
		if cached, ok := globalAvailableModelsCache.get(time.Now(), tenantID); ok {
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
			-- 2026-07-14: provider_models.canonical_raw_name is the lowercase key
			-- mirrored into model_aliases.raw_name; compare directly.
			WHERE raw_name = mo.canonical_raw_name
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

	tenantID := EffectiveTenantIDAll(r)
	popular := h.queryPopularModels(ctx, featuredModels, byCanonical, tenantID, 20)

	resp := map[string]any{
		"families":  familiesOut,
		"popular":   popular,
		"unmapped":  unmapped,
		"total_raw": totalRaw,
	}
	if r.Method == http.MethodGet {
		globalAvailableModelsCache.set(time.Now(), tenantID, resp)
	}
	writeJSON(w, http.StatusOK, resp)
}

// invalidateAvailableModelsCache is called by paths that mutate the
// data this endpoint aggregates: featured-model policy edit, provider
// refresh, the model_offer standardized_name PATCH in admin/providers.go,
// and emergency repair (which flips credential/CMB visibility and would
// otherwise leave the UI reading a stale catalog for up to the 30s TTL).
// Keep this exported so the post-deploy hot-paths above can simply call
// admin.InvalidateAvailableModelsCache() without importing the cache.
func InvalidateAvailableModelsCache() {
	globalAvailableModelsCache.mu.Lock()
	globalAvailableModelsCache.entries = nil
	globalAvailableModelsCache.mu.Unlock()
}

// 2026-06-19 audit: cache unit-test hooks — these let regression tests
// (admin/routing_cache_test.go) exercise the hit/miss/invalidate
// lifecycle without spinning up a real DB.
func setAvailableModelsCacheForTest(value map[string]any) {
	globalAvailableModelsCache.set(time.Now(), "", value)
}

func getAvailableModelsCacheForTest() (map[string]any, bool) {
	return globalAvailableModelsCache.get(time.Now(), "")
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
			"ts":                    ts,
			"request_id":            reqID,
			"idempotency_key":       idempotencyKey,
			"tenant_id":             tenantID,
			"api_key_id":            apiKeyID,
			"model":                 mdl,
			"chosen_credential_id":  credID,
			"chosen_provider_id":    provID,
			"tier":                  tier,
			"candidates_tried":      tried,
			"latency_ms":            latency,
			"success":               success,
			"error_class":           errorClass,
			"prompt_tokens":         pTok,
			"completion_tokens":     cTok,
			"cost_usd":              cost,
			"request_bytes":         reqBytes,
			"response_bytes":        respBytes,
			"client_model":          clientModel,
			"resolved_raw_model":    resolvedRawModel,
			"outbound_model":        outboundModel,
			"sticky_hit":            stickyHit,
			"client_profile":        clientProfile,
			"request_mode":          requestMode,
			"identity_hash":         identityHash,
			"transform_rule_id":     transformRuleID,
			"egress_protocol":       egressProtocol,
			"failure_stage":         failureStage,
			"failure_detail_code":   failureDetailCode,
			"resolution_path":       resolutionPath,
			"canonical_model":       canonicalModel,
			"resolution_raw_models": rawModels,
			"decision_trace":        trace,
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
		CredentialID           int        `json:"credential_id"`
		Label                  string     `json:"label"`
		Status                 string     `json:"status"`
		CircuitState           string     `json:"circuit_state"`
		ConsecutiveFailures    int        `json:"consecutive_failures"`
		CircuitOpenCountWindow int        `json:"circuit_open_count_window"`
		CoolingUntil           *time.Time `json:"cooling_until"`
		ProviderName           string     `json:"provider_name"`
		CatalogCode            *string    `json:"catalog_code"`
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
		       c.secret_ciphertext,
		       COALESCE(mo.outbound_model_name, mo.raw_model_name),
		       COALESCE(mo.outbound_model_name, mo.raw_model_name)
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id AND c.status = 'active'
		JOIN providers p ON p.id = c.provider_id AND p.enabled = TRUE
		WHERE mo.available = TRUE
		  -- 2026-07-14: client-side columns are persisted lowercase.
		  AND (mo.canonical_raw_name = lower($1) OR mo.standardized_name = lower($1))
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

	// 2026-07-14: canonical_raw_name is the lowercase client-facing key.
	tag, err := h.db.Exec(ctx, `
		UPDATE model_offers SET manual_priority = $1
		WHERE credential_id = $2 AND canonical_raw_name = lower($3)
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
			COALESCE(mo.billing_mode, 'per_token') AS billing_mode,
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
		  -- 2026-07-14: client-side columns are persisted lowercase.
		  AND (mo.canonical_raw_name = lower($1) OR mo.standardized_name = lower($1))
		  AND mo.available IS TRUE
		ORDER BY
			CASE COALESCE(mo.billing_mode, 'per_token')
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
		d.CompositeScore = executors.CalculateCompositeScore(provider.Candidate{
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
		return executors.CompareCandidatePriority(
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

func scoringWeightsFromMap(m map[string]float64) executors.ScoringWeights {
	w := executors.DefaultScoringWeights()
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

	popular := h.queryPopularModels(ctx, featuredModels, nil, EffectiveTenantIDAll(r), 20)

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
			COUNT(mo.id) FILTER (WHERE mo.unit_price_in_per_1m = 0 AND mo.unit_price_out_per_1m = 0) AS free_offers,
			COALESCE((
				SELECT COUNT(*) FROM credential_keys ck
				WHERE ck.credential_id = c.id AND ck.status = 'active'
			), 0) + 1 AS key_count
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
		// KeyCount (P2 2026-08-11): 1 primary + N active extras in
		// credential_keys. >1 means this credential is pooled for multi-key
		// rotation (KeyRotator round-robins across them).
		KeyCount   int      `json:"key_count"`
		Models     []any    `json:"models"`
		ModelNames []string `json:"model_names"`
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
			&e.KeyCount,
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
			c.quota_state,
			COALESCE(fqt.request_count, 0) AS quota_used,
			COALESCE(fqt.corrected_limit, 0) AS quota_total,
			COALESCE(fqt.is_exhausted, FALSE) AS quota_exhausted,
			fqt.auto_reset_at AS quota_reset_at
		FROM model_offers mo
		JOIN credentials c ON c.id = mo.credential_id
		JOIN providers p ON p.id = c.provider_id
		LEFT JOIN free_quota_tracker fqt ON fqt.credential_id = c.id
			AND fqt.provider_code = p.catalog_code
			AND fqt.model_id = mo.raw_model_name
			AND fqt.window_type = 'day-1'
			AND fqt.window_start <= now() AND fqt.window_end >= now()
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
				quotaUsed, quotaTotal                                            int
				quotaExhausted                                                   bool
				quotaResetAt                                                     sql.NullTime
			)
			if err := modelRows.Scan(
				&offerID, &rawModel, &stdName, &available, &billingMode, &routingTier,
				&priceIn, &priceOut, &currency, &catalogCode, &providerName,
				&protocol, &baseURL, &credID, &credLabel, &credStatus,
				&availState, &quotaState,
				&quotaUsed, &quotaTotal, &quotaExhausted, &quotaResetAt,
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
				"quota_used":            quotaUsed,
				"quota_total":           quotaTotal,
				"quota_exhausted":       quotaExhausted,
				"quota_reset_at":        nullTimeToAny(quotaResetAt),
				// Runtime health defaults — overwritten by URSM v2 overlay below
				// when available; remain zero/nil when URSM is off.
				"success_rate":         0.0,
				"p95_latency_ms":       0,
				"consecutive_failures": 0,
				"circuit_state":        "closed",
				"cooling_until":        nil,
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

	// URSM v2 runtime health overlay — mirrors handleRoutingResolve's overlay
	// (routing.go ~367-460). Overwrites the zero defaults set above with live
	// success_rate / p95_latency / fail_streak / circuit / cooling from Redis,
	// and re-derives `routable` from the runtime view. When URSM is nil or off,
	// the defaults already in the map stand (graceful degradation, no errors).
	h.overlayFreePoolRuntimeHealth(ctx, models)

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

// overlayFreePoolRuntimeHealth overlays the free-pool page with authoritative
// URSM v2 state. Missing views and an unavailable runtime store are marked
// explicitly so persisted DB eligibility is not presented as a live route.
func (h *Handler) overlayFreePoolRuntimeHealth(ctx context.Context, models []any) {
	ursmManager := h.ursmV2
	if ursmManager == nil || ursmManager.Mode() == api.ModeOff {
		return
	}
	if !ursmManager.Ready(ctx) {
		markFreePoolRuntimeUnknown(models, "ursm_not_ready")
		return
	}

	// Collect candidate seeds keyed by their index in `models`, so we can map
	// the returned NodeViews back to the right map entry.
	type seedWithIdx struct {
		idx  int
		seed v2.CandidateSeed
	}
	seeds := make([]seedWithIdx, 0, len(models))
	for i, m := range models {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		credID, _ := mm["credential_id"].(int)
		rawModel, _ := mm["raw_model_name"].(string)
		if credID == 0 || rawModel == "" {
			continue
		}
		priceIn, _ := mm["unit_price_in_per_1m"].(float64)
		priceOut, _ := mm["unit_price_out_per_1m"].(float64)
		billingMode, _ := mm["billing_mode"].(string)
		seeds = append(seeds, seedWithIdx{idx: i, seed: v2.CandidateSeed{
			CredentialID: credID,
			RawModel:     rawModel,
			TenantID:     "default",
			PriceIn:      priceIn,
			PriceOut:     priceOut,
			BillingMode:  billingMode,
		}})
	}
	if len(seeds) == 0 {
		return
	}

	pureSeeds := make([]v2.CandidateSeed, 0, len(seeds))
	for _, s := range seeds {
		pureSeeds = append(pureSeeds, s.seed)
	}
	views, err := ursmManager.FilterAndScore(ctx, pureSeeds)
	if err != nil {
		slog.Debug("free-pool status: ursm v2 overlay failed", "error", err.Error())
		markFreePoolRuntimeUnknown(models, "ursm_query_failed")
		return
	}

	viewByKey := make(map[string]api.NodeView, len(views))
	for _, v := range views {
		viewByKey[ursmViewKey(v.CredentialID, v.RawModel)] = v
	}
	now := time.Now()
	for _, s := range seeds {
		mm := models[s.idx].(map[string]any)
		v, ok := viewByKey[ursmViewKey(s.seed.CredentialID, s.seed.RawModel)]
		if !ok {
			markFreePoolModelUnavailable(mm, "missing", "ursm_node_missing")
			continue
		}
		mm["ursm_observed"] = true
		mm["runtime_state"] = "observed"
		if v.SR5m > 0 {
			mm["success_rate"] = v.SR5m
		}
		if v.LatP95Ms > 0 {
			mm["p95_latency_ms"] = v.LatP95Ms
		}
		mm["consecutive_failures"] = v.FailStreak
		if !v.CoolUntil.IsZero() && v.CoolUntil.After(now) {
			mm["circuit_state"] = "open"
			s := v.CoolUntil.UTC().Format(time.RFC3339)
			mm["cooling_until"] = s
			// A live cool-down overrides the persisted routable flag.
			mm["routable"] = false
		} else {
			mm["circuit_state"] = "closed"
			mm["cooling_until"] = nil
		}
		// If URSM says the node is unavailable, override routable regardless of
		// the persisted availability_state.
		if !v.Available {
			reason := v.Reason
			if reason == "" {
				reason = "node_unavailable_by_ursm_v2"
			}
			markFreePoolModelUnavailable(mm, "observed", reason)
		}
	}
}

func markFreePoolRuntimeUnknown(models []any, reason string) {
	for _, m := range models {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		markFreePoolModelUnavailable(mm, "unknown", reason)
	}
}

func markFreePoolModelUnavailable(model map[string]any, runtimeState, reason string) {
	model["ursm_observed"] = runtimeState == "observed"
	model["runtime_state"] = runtimeState
	model["routable"] = false
	model["runtime_block_reason"] = reason
}

// nullTimeToAny converts a sql.NullTime to a JSON-friendly value: nil when
// not valid, otherwise an RFC3339 string in UTC. Used by handleFreePoolStatus
// to surface free_quota_tracker.auto_reset_at as quota_reset_at.
func nullTimeToAny(nt sql.NullTime) any {
	if !nt.Valid || nt.Time.IsZero() {
		return nil
	}
	return nt.Time.UTC().Format(time.RFC3339)
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
	var freeCredID int
	for _, model := range req.Models {
		if err := h.db.QueryRow(ctx, `SELECT id FROM credentials WHERE provider_id = $1 AND pool_group = 'free' LIMIT 1`, providerID).Scan(&freeCredID); err != nil {
			continue
		}
		//nolint:errcheck // best-effort exec, non-critical
		h.db.Exec(ctx, `
			INSERT INTO model_offers (credential_id, raw_model_name, available,
				routing_tier, billing_mode, currency, unit_price_in_per_1m, unit_price_out_per_1m,
				pricing_source, pricing_updated_at, admin_protected)
			VALUES ($1, $2, true, 9, 'free', 'CNY', 0, 0, 'free_pool', NOW(), TRUE)
		`, freeCredID, model)
	}
	// Manually-registered offers are admin-protected so batch/auto refresh
	// never updates them.
	h.pinAdminProtectedOffers(ctx, freeCredID, req.Models)

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
			mo.available, COALESCE(mo.billing_mode, 'per_token') AS billing_mode,
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
		// BulkMode (P2 2026-08-11) controls how multiple keys are stored:
		//   "per_credential" (default): pool all keys into ONE credential —
		//     first key → credentials.secret_ciphertext, rest → credential_keys
		//     child rows. The KeyRotator round-robins across them, amplifying
		//     one account's free quota. ONLY correct when all keys belong to
		//     the SAME upstream account.
		//   "per_key": legacy behavior — one credential per key. Use when keys
		//     belong to DIFFERENT accounts (each has its own quota window).
		BulkMode string `json:"bulk_mode"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Protocol == "" {
		req.Protocol = "openai-completions"
	}
	if req.BulkMode == "" {
		req.BulkMode = "per_credential"
	}

	// Filter blanks once.
	keys := make([]string, 0, len(req.APIKeys))
	for _, k := range req.APIKeys {
		if strings.TrimSpace(k) != "" {
			keys = append(keys, k)
		}
	}

	registered := 0
	errors := 0
	results := make([]map[string]any, 0)

	// per_credential: pool all keys into one credential.
	if req.BulkMode == "per_credential" && len(keys) > 0 {
		cfg := freeProviderConfig{
			catalogCode:     req.CatalogCode,
			displayName:     req.DisplayName,
			baseURL:         req.BaseURL,
			protocol:        req.Protocol,
			apiKey:          keys[0],
			models:          req.Models,
			credentialLabel: fmt.Sprintf("%s-free-pool", req.CatalogCode),
			acquisitionMode: "bulk",
			acquisitionDetail: fmt.Sprintf("bulk-register per_credential: 1 primary + %d extras (%d keys total)",
				len(keys)-1, len(keys)),
		}
		if len(keys) > 1 {
			cfg.extraKeys = keys[1:]
		}
		result := h.registerFreeProvider(w, r, cfg)
		if result["status"] == "registered" {
			registered = len(keys) // all keys went into one credential
		} else {
			errors = len(keys)
		}
		result["bulk_mode"] = "per_credential"
		result["pooled_credential"] = true
		results = append(results, result)

		writeJSON(w, http.StatusOK, map[string]any{
			"catalog_code": req.CatalogCode,
			"bulk_mode":    "per_credential",
			"total_keys":   len(keys),
			"registered":   registered,
			"errors":       errors,
			"results":      results,
		})
		return
	}

	// per_key (legacy): one credential per key.
	for i, key := range keys {
		cfg := freeProviderConfig{
			catalogCode:       req.CatalogCode,
			displayName:       req.DisplayName,
			baseURL:           req.BaseURL,
			protocol:          req.Protocol,
			apiKey:            key,
			models:            req.Models,
			credentialLabel:   fmt.Sprintf("%s-free-key-%d", req.CatalogCode, i+1),
			acquisitionMode:   "bulk",
			acquisitionDetail: fmt.Sprintf("bulk-register key %d/%d", i+1, len(keys)),
		}
		result := h.registerFreeProvider(w, r, cfg)
		if result["status"] == "registered" {
			registered++
		} else {
			errors++
		}
		result["key_index"] = i
		result["bulk_mode"] = "per_key"
		results = append(results, result)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"catalog_code": req.CatalogCode,
		"bulk_mode":    "per_key",
		"total_keys":   len(keys),
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
	// 2026-07-15: per-credential client-side RPM cap (migration 407).
	// 0 = unlimited (default). Populated from the free-pool template's
	// rpmLimit field so freshly registered free credentials get the
	// recommended throttle without manual ops intervention.
	rpmLimit int
	// extraKeys (P2 2026-08-11) holds additional decrypted keys for the SAME
	// upstream account, written to the credential_keys child table so the
	// KeyRotator round-robins across them (amplifying one account's free
	// quota). Only meaningful when the keys belong to ONE account — bulk-
	// importing keys from different accounts would wrongly pool their quota.
	extraKeys []string
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
				unit_price_in_per_1m, unit_price_out_per_1m, pricing_source, pricing_updated_at,
				admin_protected)
			VALUES ($1, $2, $3, true, 9, 'free', 'CNY', 0, 0, 'pool_manager', NOW(), TRUE)
		`, credID, canonID, model)
	}
	// Manually-registered offers are admin-protected so batch/auto refresh
	// never updates them.
	h.pinAdminProtectedOffers(ctx, credID, cfg.models)

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

// ursmViewKey builds the map key that pairs a URSM v2 NodeView with a
// resolve candidate. Credential IDs cannot contain the NUL separator, so
// the pair (credential_id, raw_model) is collision-free.
func ursmViewKey(credentialID int, rawModel string) string {
	return strconv.Itoa(credentialID) + "\x00" + rawModel
}

// resolveRuntimeDefaults back-fills display-only runtime columns when URSM v2
// is disabled. Authoritative URSM misses, readiness failures, and query failures
// are handled separately so the response never presents DB eligibility as runtime
// reachability.
func resolveRuntimeDefaults(c *resolveCandidate) {
	c.Available = true
	c.CircuitState = "closed"
	c.CoolingUntil = nil
	c.ConsecutiveFailures = 0
	c.CredentialConsecutiveFailures = 0
	c.RuntimeState = "unobserved"
	if c.SuccessRate == 0 {
		c.SuccessRate = 0.9
	}
	if c.P95LatencyMs == 0 {
		c.P95LatencyMs = 9999
	}
}

func markResolveRuntimeUnknown(candidates []resolveCandidate, reason string) {
	for i := range candidates {
		c := &candidates[i]
		c.RuntimeState = "unknown"
		c.URSMObserved = false
		if !c.DBEligible {
			continue
		}
		c.Available = false
		c.RuntimeRoutable = false
		c.Routable = false
		c.BlockReason = reason

	}
}

// applyURSMOverlay merges URSM v2 runtime NodeViews into the resolve
// candidates. Two 2026-08-18 fixes live here:
//
//  1. FilterAndScore sorts views by score before returning them, so views
//     are NOT guaranteed to stay in seed order. The previous index pairing
//     mismatched rows whenever the sort order differed (log spam: "ursm v2
//     view out of order") and silently skipped the overlay for every
//     misaligned candidate. Views are now paired by (credential_id,
//     raw_model).
//  2. The previous loop iterated `for i, c := range candidates` and mutated
//     the loop COPY c — defaults and overlay writes were discarded, so the
//     emitted candidates always carried zero-valued runtime fields (and
//     Routable never reflected runtime state). Mutations now go through a
//     pointer into the slice.
//
// Redis/mirror misses are explicitly marked as missing so the diagnostic
// surface agrees with authoritative request routing.
func applyURSMOverlay(candidates []resolveCandidate, views []api.NodeView, now time.Time) {
	viewByKey := make(map[string]api.NodeView, len(views))
	for _, v := range views {
		viewByKey[ursmViewKey(v.CredentialID, v.RawModel)] = v
	}
	for i := range candidates {
		c := &candidates[i]
		resolveRuntimeDefaults(c)
		v, ok := viewByKey[ursmViewKey(c.CredentialID, c.ModelName)]
		if !ok {
			if !c.DBEligible {
				continue
			}
			c.Available = false

			c.RuntimeRoutable = false
			c.Routable = false
			c.RuntimeState = "missing"
			c.BlockReason = "ursm_node_missing"
			continue
		}
		c.URSMObserved = true
		c.RuntimeState = "observed"

		c.Available = v.Available
		c.ConsecutiveFailures = v.FailStreak
		c.CredentialConsecutiveFailures = v.FailStreak
		if v.SR5m > 0 {
			c.SuccessRate = v.SR5m
		}
		if v.LatP95Ms > 0 {
			c.P95LatencyMs = v.LatP95Ms
		}
		if !v.CoolUntil.IsZero() && v.CoolUntil.After(now) {
			c.CircuitState = "open"
			s := v.CoolUntil.UTC().Format(time.RFC3339)
			c.CoolingUntil = &s
		} else {
			c.CircuitState = "closed"
			c.CoolingUntil = nil
		}
		// Re-derive Routable: SQL view covers schema gating (provider
		// enabled, plan compatibility, etc.); URSM v2 covers runtime
		// gating. AND them together.
		if !c.Routable {
			continue // SQL view 已经否决 —— 保持它的原因
		}
		if !v.Available {
			c.Routable = false
			c.RuntimeRoutable = false
			if v.Reason != "" {
				c.BlockReason = v.Reason
			} else {
				c.BlockReason = "node_unavailable_by_ursm_v2"
			}
			continue
		}
		if c.CircuitState == "open" {
			c.Routable = false
			c.RuntimeRoutable = false
			if v.Reason != "" {
				c.BlockReason = v.Reason
			} else {
				c.BlockReason = "node_in_cool_until"
			}
			continue
		}
	}
}
