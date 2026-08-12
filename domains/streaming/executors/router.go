package executors

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credential"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
	"github.com/kaixuan/llm-gateway-go/domains/ursm/v2/statesource"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
)

var tierOrder = [4]int{1, 2, 3, 9}

type Router struct {
	Sticky  *StickyCache
	Limiter *credential.Limiter
	// FpSlots is the credential-level concurrency tracker. When set,
	// loadScore includes FP slot pressure in its P2C selection.
	FpSlots interface {
		Enabled() bool
		Stats(ctx context.Context, credentialID int, limit *int) (slotLimit, used, free *int)
		GetNodeState(ctx context.Context, credentialID int, model string) (*credentialfpslot.NodeState, error)
	}
	// Bandit is the Thompson Sampling bandit scorer for intelligent credential
	// selection. When set, planByTier uses bandit scoring instead of P2C within
	// each tier. Falls back to P2C if Bandit is nil.
	Bandit *credential.BanditScorer
	// BanditFlusher is the async batch writer for Bandit state. When set,
	// the executor calls MarkDirty after recording success/failure events.
	BanditFlusher interface {
		MarkDirty(credentialID string)
	}
	// weightCounters isolates deterministic weighted selection by candidate set.
	weightCounters sync.Map

	// 新增：状态管理器引用（向后兼容）
	StateManager credentialstate.StateProvider

	// URSMv2 (2026-07-21, URSM v2 plan T16): 在 canary / authoritative 模式下，
	// PlanCandidates 会委托 v2 Manager.Plan 重新排序/过滤候选，v2 返回 nil 时
	// 回退到原有 ordered 切片。Nil 即保留所有旧行为（main.go 未 wire，运行时为 nil）。
	//
	// 2026-07-26 URSM v1→v2 统一: 旧字段 URSM (v1) 已删除，main.go 从未给它
	// 赋值过，运行时恒为 nil。Router 唯一的 URSM 入口是 URSMv2。
	URSMv2 *ursmv2.Manager

	// 新增：路由评分权重配置（Phase 1）
	LoadScoreWeights LoadScoreWeights

	// TimeoutConfig (Phase 2, 2026-07-23): Dynamic timeout calculation
	// based on context size, historical latency, and network conditions.
	// Hot-reloads config from system_settings table every 30 seconds.
	TimeoutConfig TimeoutCalculator

	// PressureAwareEnabled (Phase 2.3, 2026-07-24): 启用压力感知路由
	// 当启用时，Router 会根据 FpSlots/Limiter 的压力信号调整候选节点权重
	// 默认 false，通过环境变量 PRESSURE_AWARE_ROUTING 控制
	PressureAwareEnabled bool

	// ShadowStrategy (GW-03, omni-ref2): 可选的路由策略，仅用于 shadow 评分
	// 对比，不改变实际选中候选。nil = 现状（P2C/bandit 行为零变化）。
	// 非 nil 时，planByTier 在每个 tier bucket 用 ShadowStrategy 独立评分，
	// 记录 agreed/disagreed metric（llmgw_routing_shadow_strategy_outcomes_total）。
	// 通过环境变量 LLM_GATEWAY_ROUTING_SHADOW_STRATEGY 选择策略名构造。
	ShadowStrategy Strategy
}

func NewRouter(sticky *StickyCache, lim *credential.Limiter) *Router {
	return &Router{
		Sticky:           sticky,
		Limiter:          lim,
		LoadScoreWeights: DefaultLoadScoreWeights(), // Phase 1: 使用默认权重
	}
}

func (r *Router) PlanCandidates(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	return r.PlanCandidatesWithContext(context.Background(), candidates, stickyCredentialID, policy, egressPreference, "", "", "")
}

// PlanCandidatesWithContext plans one request using a single URSM recovery
// snapshot. The legacy PlanCandidates wrapper remains for non-request callers.
func (r *Router) PlanCandidatesWithContext(
	requestCtx context.Context,
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
	tenantID string,
	canonical string,
	requestID string,
) []provider.Candidate {
	if requestCtx == nil {
		requestCtx = context.Background()
	}
	candidates = deduplicateCandidates(candidates)
	if len(candidates) == 0 {
		return nil
	}

	// S-3 (Step 5 round 1): decide and record the OUTER
	// routing_state_source label exactly once per request. The inner
	// NodeMirror source (hit/miss/stale) is exposed by
	// FilterAndScoreReadyWithSource and recorded there. The outer
	// label here is the routing decision's source (authoritative /
	// fallback / canary / off); the spec §8.2 + §11.4 require both
	// inner and outer be observable.
	//
	// The stateRecorded flag guards against double-recording on the
	// canary branch (where v2 is consulted but the request also
	// short-circuits to a non-v2 path). One call per
	// PlanCandidatesWithContext invocation is the contract.
	outerSourceRecorded := false
	recordOuterSource := func(src statesource.RoutingStateSource) {
		if outerSourceRecorded {
			return
		}
		outerSourceRecorded = true
		statesource.RecordRoutingStateSource(src)
		// Spec §12 GAP 3: also attribute the outer source to this
		// requestID so recordInitialRequestLog can persist it into
		// request_logs metadata (compression_meta JSONB until a
		// dedicated column is migrated). Best-effort; a dropped entry
		// leaves the row with empty routing metadata, never a failure.
		RecordRoutingSourceForRequest(requestID, src)
	}

	// Capture one bounded Ready result. This value is reused for both v2
	// filtering and backend selection below.
	var readySnapshot *bool
	if r.URSMv2 != nil && (r.URSMv2.Mode() == ursmv2api.ModeAuthoritative || r.URSMv2.Mode() == ursmv2api.ModeCanary) {
		readyCtx, readyCancel := context.WithTimeout(requestCtx, 50*time.Millisecond)
		ready := r.URSMv2.Ready(readyCtx)
		readyCancel()
		readySnapshot = &ready
	}

	if r.URSMv2 != nil && r.URSMv2.Mode() == ursmv2api.ModeAuthoritative && readySnapshot != nil && *readySnapshot {
		ctx, cancel := context.WithTimeout(requestCtx, 50*time.Millisecond)
		defer cancel()
		seeds := make([]ursmv2.CandidateSeed, 0, len(candidates))
		for _, c := range candidates {
			seeds = append(seeds, ursmv2.CandidateSeed{
				ProviderID:   c.ProviderID,
				CredentialID: c.CredentialID,
				RawModel:     c.RawModel,
				Canonical:    firstNonEmpty(c.StandardizedName, canonical),
				TenantID:     tenantID,
				PriceIn:      derefPrice(c.PriceInPer1M),
				PriceOut:     derefPrice(c.PriceOutPer1M),
				BillingMode:  c.BillingMode,
				Trust:        0,
				BaseURLMs:    c.P50LatencyMs,
			})
		}
		// Use the S-3 variant so the inner NodeMirror source is
		// recorded into the shared counter. The returned enum is
		// intentionally not consulted here — the router records the
		// OUTER label, not the inner one.
		views, _, err := r.URSMv2.FilterAndScoreReadyWithSource(ctx, seeds, *readySnapshot)
		if err != nil {
			slog.Warn("router: URSM v2 FilterAndScore failed, failing open",
				"error", err,
				"seed_count", len(seeds),
				"mode", r.URSMv2.Mode(),
			)
			recordOuterSource(statesource.StateSourceFallback)
		} else {
			recordOuterSource(statesource.StateSourceAuthoritative)
			allow := make(map[string]bool, len(views))
			for _, v := range views {
				if v.Available {
					allow[seedLookupKey(v.ProviderID, v.CredentialID, v.RawModel)] = true
				}
			}
			filtered := make([]provider.Candidate, 0, len(candidates))
			for i, c := range candidates {
				if allow[seedLookupKey(seeds[i].ProviderID, c.CredentialID, c.RawModel)] {
					filtered = append(filtered, c)
				}
			}
			candidates = filtered
			if len(candidates) == 0 {
				return nil
			}
		}
	} else if r.URSMv2 != nil && r.URSMv2.Mode() == ursmv2api.ModeAuthoritative {
		// Authoritative but not ready. Per spec §8.2 the legacy
		// state source must NOT be the authoritative health judge,
		// but the router still needs to surface that we did not
		// consult v2 on this request → StateSourceFallback.
		recordOuterSource(statesource.StateSourceFallback)
	}

	// 2026-07-24 Phase 2.3: 应用压力惩罚（feature flag 控制）
	// 在 URSM v2 过滤之后，根据 FpSlots/Limiter 压力调整权重
	// 注意：完整调用在 stateBackend 定义之后（见下方）

	// 2026-07-24 Phase 1: 使用统一的状态后端接口，消除散落的条件判断。
	// 一次性决定使用 URSM v2 / StateManager / DB-only 哪套系统。
	ctx, cancel := context.WithTimeout(requestCtx, 50*time.Millisecond)
	defer cancel()
	stateBackend := selectStateBackendWithReady(r.URSMv2, r.StateManager, ctx, readySnapshot)
	available := stateBackend.FilterAvailable(ctx, candidates)

	// 2026-07-25 Phase 2.4: 标记 Feature flag 状态（供外部观察）
	SetPressureAwareRoutingEnabled(r.PressureAwareEnabled)

	// 2026-07-24 Phase 2.3: 应用压力惩罚（在 StateBackend 过滤之后）
	// 此时可用候选已经确定，对它们应用压力惩罚
	if r.PressureAwareEnabled && len(available) > 0 {
		pressureCtx, pressureCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		r.applyPressurePenalty(pressureCtx, available, stateBackend.Name())
		pressureCancel()
	}

	if len(available) == 0 {
		// Build a per-reason breakdown so the next "all providers failed at
		// the same time" outage can be root-caused from this log line alone.
		//
		// 2026-07-13 fix: also query StateManager for each candidate's
		// in-memory rejection reason (e.g. cooling/transient). Previously
		// only `c.UnavailableReason()` was consulted, but candidates filtered
		// by the state manager have an empty UnavailableReason() and were
		// logged as `unknown`. We now surface the actual StateManager reason
		// so operators can see *why* the candidate was rejected without
		// having to correlate with credential_state_log separately.
		reasonCounts := make(map[string]int, 8)
		var sampleReasons []string
		queryCtx, queryCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer queryCancel()
		for _, c := range candidates {
			reason := c.UnavailableReason()
			// 2026-08-02 (spec §10 Step 5 C-1): authoritative 模式下
			// StateManager 必须零调用。当前靠 URSMv2Backend no-op +
			// PlanCandidates 早期过滤保证"不可达"，此处补显式 guard，
			// 避免任何未来重构意外触发 StateManager 读路径。
			if reason == "" && !stateBackend.IsAuthoritative() && r.StateManager != nil && r.StateManager.Enabled() {
				if _, smReason := r.StateManager.IsAvailable(queryCtx, c.CredentialID, c.RawModel); smReason != "" {
					reason = "state:" + smReason
				}
			}
			if reason == "" {
				reason = "unknown"
			}
			reasonCounts[reason]++
			if len(sampleReasons) < 5 {
				sampleReasons = append(sampleReasons, fmt.Sprintf(
					"cred=%d prov=%d reason=%s", c.CredentialID, c.ProviderID, reason,
				))
			}
		}

		// 2026-07-24 Phase 1: 在 authoritative 模式下也保留降级模式。
		// 降级模式是保护机制，用于处理瞬态故障导致的完全失败。
		// URSM v2 authoritative 模式下的冷却决策仍在生效，降级只是最后的保护。
		if len(candidates) <= 2 {
			degradedCandidates := r.tryDegradedMode(queryCtx, candidates)
			if len(degradedCandidates) > 0 {
				slog.Warn("router: degraded mode activated, using transiently unavailable candidates",
					"total_candidates", len(candidates),
					"degraded_count", len(degradedCandidates),
					"reasons", reasonCounts,
					"state_backend", stateBackend.Name(),
				)
				return degradedCandidates
			}
		}

		slog.Warn("router: all candidates unavailable",
			"total", len(candidates),
			"reasons", reasonCounts,
			"sample", sampleReasons,
		)
		return nil
	}

	// 2026-07-24 Phase 1: 仅在非 authoritative 模式下执行 FpSlots 健康检查。
	// URSM v2 authoritative 已包含冷却期和健康状态判断，无需重复过滤。
	if !stateBackend.IsAuthoritative() {
		available = r.filterHealthyNodes(available)
	}

	// Round 1: token_plan / code_plan / agent_plan / free — always before PAYG.
	// Round 2: token (按量). Executor skips saturated round-1 creds and falls through.
	round1, round2 := splitByBillingRound(available)
	stratIn := StrategyInput{
		Policy:           policy,
		EgressPreference: egressPreference,
		TenantID:         tenantID,
		Canonical:        canonical,
		RequestID:        requestID,
		LoadScoreWeights: r.LoadScoreWeights,
	}
	ordered := r.planByTier(requestCtx, round1, policy, stratIn)
	if len(round2) > 0 {
		ordered = append(ordered, r.planByTier(requestCtx, round2, policy, stratIn)...)
	}

	// Note (2026-07-07 audit): an earlier "session-aware" rotation
	// of the full `ordered` slice lived here. It was added in cf65803f to spread
	// new-session traffic when loadScore values tie, but it rotated ACROSS tiers
	// — which violates tier priority semantics (a tier-3 candidate could be
	// tried before a tier-1 candidate). Per-tier weighted selection is performed
	// inside planByTier, so cross-tier rotation remains unnecessary and harmful.

	if stickyCredentialID != nil {
		ordered = prioritizeSticky(ordered, *stickyCredentialID)
	}

	if len(egressPreference) > 0 {
		ordered = applyProtocolAffinity(ordered, egressPreference)
	}

	if r.URSMv2 != nil && r.URSMv2.Mode() == ursmv2api.ModeCanary && r.URSMv2.ShouldUseV2(tenantID, canonical, requestID) {
		if v2Ordered := r.planWithURSMv2Context(ordered, requestCtx, tenantID, canonical, requestID, readySnapshot != nil && *readySnapshot); v2Ordered != nil {
			ordered = v2Ordered
		}
		// S-3 (Step 5 round 1): the canary v2 path was applied to
		// this request — the outer source is Canary. The inner
		// NodeMirror source (hit/miss/stale/fallback) is recorded
		// inside planWithURSMv2Context -> PlanReadyWithSource ->
		// FilterAndScoreReadyWithSource, which auto-records into the
		// shared statesource counter (same contract as the
		// authoritative path). Spec §8.2 requires both inner and
		// outer to be observable; the canary path now satisfies
		// that.
		recordOuterSource(statesource.StateSourceCanary)
	}

	// S-3: if we got here without recording an outer source, the
	// request did not consult v2 (off / shadow / canary-but-rolled-
	// out / v2 manager nil). Record Off so the per-request metric
	// surface stays closed.
	if !outerSourceRecorded {
		recordOuterSource(statesource.StateSourceOff)
	}

	return ordered
}

// planWithURSMv2Context is the request-aware variant used by canary routing.
// It uses PlanReadyWithSource so the inner NodeMirror source
// (hit/miss/stale/fallback) is auto-recorded by the manager (same
// contract as the authoritative path). The router records the outer
// Canary label separately — the S-3 spec §8.2 invariant requires
// both inner and outer to be observable.
func (r *Router) planWithURSMv2Context(fallback []provider.Candidate, requestCtx context.Context, tenant, canonical, requestID string, ready bool) []provider.Candidate {
	if r.URSMv2 == nil || len(fallback) == 0 {
		return nil
	}
	seeds := make([]ursmv2.CandidateSeed, 0, len(fallback))
	lookup := make(map[string]provider.Candidate, len(fallback))
	for _, c := range fallback {
		key := seedLookupKey(c.ProviderID, c.CredentialID, c.RawModel)
		lookup[key] = c
		seeds = append(seeds, ursmv2.CandidateSeed{
			ProviderID: c.ProviderID, CredentialID: c.CredentialID, RawModel: c.RawModel,
			Canonical: firstNonEmpty(c.StandardizedName, canonical), TenantID: tenant,
			PriceIn: derefPrice(c.PriceInPer1M), PriceOut: derefPrice(c.PriceOutPer1M),
			BillingMode: c.BillingMode, BaseURLMs: c.P50LatencyMs,
		})
	}
	ctx, cancel := context.WithTimeout(requestCtx, 50*time.Millisecond)
	defer cancel()
	// Use PlanReadyWithSource so the inner NodeMirror source is
	// recorded via the manager's FilterAndScoreReadyWithSource path.
	// The error path falls back to the existing ordered slice in the
	// caller; the inner source for the error case is empty (the
	// outer router records StateSourceFallback).
	ordered, _, err := r.URSMv2.PlanReadyWithSource(ctx, seeds, tenant, canonical, ready)
	if err != nil {
		return nil
	}
	if len(ordered) == 0 {
		return nil
	}
	out := make([]provider.Candidate, 0, len(ordered))
	for _, s := range ordered {
		if c, ok := lookup[seedLookupKey(s.ProviderID, s.CredentialID, s.RawModel)]; ok {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// returns nil when v2 declines (off mode, not ready, FilterAndScore error,
// empty result) so the caller falls back to the existing ordered slice.
//
// The v2 manager operates on []CandidateSeed, not []provider.Candidate, so
// the router builds the seed list from the *post-filter* candidates (so we
// preserve the legacy availability/health filters that already ran above)
// and then maps the v2-ordered seeds back to the upstream provider.Candidate
// via a (CredentialID, RawModel) lookup.
//
// 2026-07-21, URSM v2 plan T16.
func (r *Router) planWithURSMv2(fallback []provider.Candidate) []provider.Candidate {
	if r.URSMv2 == nil {
		return nil
	}
	// Build CandidateSeeds from the post-filter candidates. We re-use the
	// fallback slice as the seed source rather than the raw input, so v2 only
	// ranks candidates the legacy router considered routable.
	seeds := make([]ursmv2.CandidateSeed, 0, len(fallback))
	lookup := make(map[string]provider.Candidate, len(fallback))
	for _, c := range fallback {
		key := seedLookupKey(c.ProviderID, c.CredentialID, c.RawModel)
		lookup[key] = c
		seeds = append(seeds, ursmv2.CandidateSeed{
			ProviderID:   c.ProviderID,
			CredentialID: c.CredentialID,
			RawModel:     c.RawModel,
			Canonical:    c.StandardizedName,
			TenantID:     "", // TODO(T20): plumb tenant through PlanCandidates.
			PriceIn:      derefPrice(c.PriceInPer1M),
			PriceOut:     derefPrice(c.PriceOutPer1M),
			BillingMode:  c.BillingMode,
			Trust:        0,
			BaseURLMs:    c.P50LatencyMs,
		})
	}
	if len(seeds) == 0 {
		return nil
	}

	// Bounded ctx so v2 slowness can never block the request hot path.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ordered := r.URSMv2.Plan(ctx, seeds, "", "")
	if len(ordered) == 0 {
		return nil
	}

	out := make([]provider.Candidate, 0, len(ordered))
	for _, s := range ordered {
		key := seedLookupKey(s.ProviderID, s.CredentialID, s.RawModel)
		c, ok := lookup[key]
		if !ok {
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		// v2 returned seeds we couldn't map back; treat as no-op so the
		// existing fallback slice stays in use.
		return nil
	}
	return out
}

func seedLookupKey(providerID, credentialID int, rawModel string) string {
	return strconv.Itoa(providerID) + "|" + strconv.Itoa(credentialID) + "|" + rawModel
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func derefPrice(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// deduplicateCandidates keeps one route slot per provider/credential/model.
// Repeated binding rows must not amplify one upstream failure into multiple
// attempts against the same credential or consume its slots repeatedly.
func deduplicateCandidates(candidates []provider.Candidate) []provider.Candidate {
	seen := make(map[string]struct{}, len(candidates))
	result := make([]provider.Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := fmt.Sprintf("%d:%d:%s", candidate.ProviderID, candidate.CredentialID, strings.ToLower(candidate.RawModel))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate)
	}
	return result
}

// planWithURSM 已删除 2026-07-26 (URSM v1→v2 统一)
// v1 入口 (r.URSM) 在 main.go 中从未 wire，运行时恒为 nil，该函数为死代码。

// planLegacy 保留旧逻辑（向后兼容）
// planLegacy 保留旧逻辑（向后兼容） — REMOVED 2026-07-26 (URSM v1→v2 统一)
//
// 唯一调用方 planWithURSM 已删除，planLegacy 本身也成死代码。
// 函数体替换为 deprecated 占位返回 nil，避免任何意外调用导致 nil deref。
// 新代码不应再调用本方法，PlanCandidates 走 selectStateBackend() 统一入口。
//
// DEPRECATED: 2026-07-26 之后将删除此函数（确认无外部引用后）。
func (r *Router) planLegacy(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	_ = candidates
	_ = stickyCredentialID
	_ = policy
	_ = egressPreference
	slog.Warn("planLegacy called after URSM v1→v2 统一 deprecated; returning nil",
		"hint", "PlanCandidates now goes through selectStateBackend() exclusively")
	return nil
}

func splitByBillingRound(cands []provider.Candidate) (round1, round2 []provider.Candidate) {
	for _, c := range cands {
		if provider.IsPreferredPlanBilling(c.BillingMode) {
			round1 = append(round1, c)
		} else {
			round2 = append(round2, c)
		}
	}
	return round1, round2
}

func (r *Router) planByTier(ctx context.Context, candidates []provider.Candidate, policy *provider.Policy, stratIn StrategyInput) []provider.Candidate {
	if len(candidates) == 0 {
		return nil
	}

	byTier := make(map[int][]provider.Candidate)
	for _, c := range candidates {
		byTier[c.Tier] = append(byTier[c.Tier], c)
	}

	tiersUsed := 0
	var ordered []provider.Candidate
	for _, tier := range tierOrder {
		bucket := byTier[tier]
		if len(bucket) == 0 {
			continue
		}

		// Hybrid mode: use Bandit if available, fall back to P2C
		var sorted []provider.Candidate
		if r.Bandit != nil {
			// Thompson Sampling Bandit ordering (with pressure factor)
			sorted = r.banditOrder(bucket)
		} else {
			// Legacy P2C ordering (load-aware)
			sorted = p2cOrder(bucket, r)
		}

		// GW-03: shadow strategy diff（仅观测，不改顺序）。
		// 在 weighted selection 之前对比 ShadowStrategy 首选 vs 实际首选。
		if r.ShadowStrategy != nil {
			r.scoreWithShadow(ctx, bucket, sorted, stratIn)
		}

		// Weight controls the first attempt. Remaining candidates retain the
		// health-aware order produced above for failover.
		if len(sorted) > 1 {
			counter := r.nextWeightCounter(sorted)
			sorted = promoteWeightedCandidate(sorted, counter)
		}

		ordered = append(ordered, sorted...)
		tiersUsed++
		if tiersUsed >= policy.TierFallbackMax {
			break
		}
	}

	maxTotal := 12
	if len(ordered) > maxTotal {
		ordered = ordered[:maxTotal]
	}
	return ordered
}

func (r *Router) nextWeightCounter(cands []provider.Candidate) uint64 {
	identities := make([]string, 0, len(cands))
	for _, c := range cands {
		identities = append(identities, fmt.Sprintf("%d:%d:%s", c.ProviderID, c.CredentialID, c.RawModel))
	}
	sort.Strings(identities)

	counter, _ := r.weightCounters.LoadOrStore(strings.Join(identities, "|"), &atomic.Uint64{})
	return counter.(*atomic.Uint64).Add(1) - 1
}

func promoteWeightedCandidate(cands []provider.Candidate, counter uint64) []provider.Candidate {
	if len(cands) <= 1 {
		return cands
	}

	stable := append([]provider.Candidate(nil), cands...)
	sort.SliceStable(stable, func(i, j int) bool {
		if stable[i].ProviderID != stable[j].ProviderID {
			return stable[i].ProviderID < stable[j].ProviderID
		}
		if stable[i].CredentialID != stable[j].CredentialID {
			return stable[i].CredentialID < stable[j].CredentialID
		}
		return stable[i].RawModel < stable[j].RawModel
	})

	totalWeight := 0
	for _, c := range stable {
		if c.Weight > 0 {
			totalWeight += c.Weight
		}
	}
	if totalWeight == 0 {
		return cands
	}

	position := int(counter % uint64(totalWeight))
	position = position * weightStride(totalWeight) % totalWeight
	winner := stable[0]
	for _, c := range stable {
		if c.Weight <= 0 {
			continue
		}
		if position < c.Weight {
			winner = c
			break
		}
		position -= c.Weight
	}

	winnerIndex := 0
	for i, c := range cands {
		if c.ProviderID == winner.ProviderID && c.CredentialID == winner.CredentialID && c.RawModel == winner.RawModel {
			winnerIndex = i
			break
		}
	}
	if winnerIndex == 0 {
		return cands
	}

	out := append([]provider.Candidate(nil), cands...)
	copy(out[1:winnerIndex+1], out[:winnerIndex])
	out[0] = winner
	return out
}

func weightStride(totalWeight int) int {
	stride := totalWeight*618/1000 + 1
	for greatestCommonDivisor(stride, totalWeight) != 1 {
		stride++
	}
	return stride
}

func greatestCommonDivisor(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func filterAvailable(cands []provider.Candidate) []provider.Candidate {
	var out []provider.Candidate
	for _, c := range cands {
		if c.IsAvailable() {
			out = append(out, c)
		}
	}
	return out
}

// DEPRECATED: filterAvailableWithStateManager 将被 StateBackend 接口替代。
// 2026-07-24 Phase 1: 此方法已通过 LegacyStateBackend 封装，不应直接调用。
// 保留用于向后兼容，未来版本将移除。
//
// filterAvailableWithStateManager 使用状态管理器优先判断可用性
func (r *Router) filterAvailableWithStateManager(ctx context.Context, cands []provider.Candidate) []provider.Candidate {
	// 2026-07-21, URSM v2 plan T21: in mode=authoritative, the v2 Manager
	// already filtered the candidate set upstream (PlanCandidates step 1);
	// skip the legacy StateManager/IsAvailable read entirely to avoid a
	// second source-of-truth on top of v2. URSMv2 == nil keeps the legacy
	// path live (main.go default URSM_V2_MODE=off leaves v2 un-wired).
	if r.URSMv2 != nil && r.URSMv2.Mode() == ursmv2api.ModeAuthoritative {
		return cands
	}
	var out []provider.Candidate
	for _, c := range cands {
		// 优先查询状态管理器
		if r.StateManager != nil && r.StateManager.Enabled() {
			available, reason := r.StateManager.IsAvailable(ctx, c.CredentialID, c.RawModel)
			if !available {
				slog.Debug("router: filtered by state manager",
					"credential_id", c.CredentialID,
					"model", c.RawModel,
					"reason", reason)
				continue
			}
		}

		// 回退到原有逻辑
		if c.IsAvailable() {
			out = append(out, c)
		}
	}
	return out
}

func (r *Router) filterHealthyNodes(candidates []provider.Candidate) []provider.Candidate {
	if r.FpSlots == nil || !r.FpSlots.Enabled() {
		return candidates
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	healthy := make([]provider.Candidate, 0, len(candidates))
	filtered := make([]provider.Candidate, 0)
	now := time.Now()
	for _, cand := range candidates {
		state, err := r.FpSlots.GetNodeState(ctx, cand.CredentialID, cand.RawModel)
		if err != nil {
			healthy = append(healthy, cand)
			continue
		}
		if state == nil || state.IsUsable(now) {
			healthy = append(healthy, cand)
			continue
		}
		slog.Debug("router: route node filtered out",
			"credential_id", cand.CredentialID,
			"model", cand.RawModel,
			"disabled", state.Disabled,
			"failure_count", state.FailureCount,
			"consecutive_failures", state.ConsecutiveFailureStreak(now),
		)
		filtered = append(filtered, cand)
	}
	if len(healthy) == 0 && len(filtered) > 0 {
		slog.Warn("router: all route nodes filtered by health state, failing open",
			"total", len(candidates),
			"filtered", len(filtered),
		)
		return candidates
	}
	return healthy
}

func p2cOrder(cands []provider.Candidate, r *Router) []provider.Candidate {
	if len(cands) <= 1 {
		return cands
	}

	pool := make([]provider.Candidate, len(cands))
	copy(pool, cands)
	out := make([]provider.Candidate, 0, len(pool))

	ctx := context.Background()

	for len(pool) > 0 {
		if len(pool) == 1 {
			out = append(out, pool[0])
			break
		}

		minCost := cheapestCost(pool)
		closePool := make([]provider.Candidate, 0, len(pool))
		for _, c := range pool {
			cost := blendedCost(c)
			if cost == 0 || cost <= minCost*1.10 {
				closePool = append(closePool, c)
			}
		}
		samplePool := closePool
		if len(samplePool) < 2 {
			samplePool = pool
		}

		a, b := randomPair(samplePool)
		scoreA := loadScore(a, r, ctx)
		scoreB := loadScore(b, r, ctx)
		chosen := a
		if scoreB < scoreA {
			chosen = b
		} else if scoreB == scoreA {
			// 2026-06-25: When load scores are equal, P2C previously always
			// picked 'a' (first random sample), biasing distribution 83/17.
			// 2026-08-13: extend the tie-break to be capacity-proportional so
			// failover ordering stays weighted by provider concurrency capacity
			// (Candidate.Weight). Each side wins with probability ∝ its Weight;
			// equal weights collapse to the original 50/50 anti-bias coin flip.
			chosen = pickWeightedTie(a, b)
		}

		out = append(out, chosen)
		pool = removeCandidate(pool, chosen)
	}
	return out
}

func blendedCost(c provider.Candidate) float64 {
	in := 0.0
	out := 0.0
	if c.PriceInPer1M != nil {
		in = *c.PriceInPer1M
	}
	if c.PriceOutPer1M != nil {
		out = *c.PriceOutPer1M
	}
	return in + out
}

func cheapestCost(pool []provider.Candidate) float64 {
	min := 0.0
	for _, c := range pool {
		cost := blendedCost(c)
		if cost > 0 {
			if min == 0 || cost < min {
				min = cost
			}
		}
	}
	return min
}

func loadScore(c provider.Candidate, r *Router, ctx context.Context) float64 {
	// Phase 1 改进：使用新的评分方法
	return calculateLoadScore(c, r, ctx, r.LoadScoreWeights)
}

func randomPair(pool []provider.Candidate) (provider.Candidate, provider.Candidate) {
	n := len(pool)
	i := rand.Intn(n)
	j := rand.Intn(n - 1)
	if j >= i {
		j++
	}
	return pool[i], pool[j]
}

// pickWeightedTie breaks a P2C load-score tie by capacity weight (Candidate.Weight,
// derived from the credential's concurrency capacity). Each candidate wins with
// probability proportional to its Weight, so the failover sequence remains
// capacity-proportional instead of an unweighted coin flip. Equal (or both
// unknown/zero) weights collapse to the original 50/50 anti-bias behavior, so
// homogeneous pools still distribute evenly.
func pickWeightedTie(a, b provider.Candidate) provider.Candidate {
	wa := a.Weight
	wb := b.Weight
	if wa <= 0 && wb <= 0 {
		if rand.Intn(2) == 0 {
			return b
		}
		return a
	}
	if wa <= 0 {
		wa = 1
	}
	if wb <= 0 {
		wb = 1
	}
	if rand.Float64() < float64(wa)/float64(wa+wb) {
		return a
	}
	return b
}

func removeCandidate(pool []provider.Candidate, target provider.Candidate) []provider.Candidate {
	for i, c := range pool {
		if c.CredentialID == target.CredentialID && c.ProviderID == target.ProviderID {
			return append(pool[:i], pool[i+1:]...)
		}
	}
	return pool
}

func prioritizeSticky(ordered []provider.Candidate, stickyID int) []provider.Candidate {
	var sticky, rest []provider.Candidate
	for _, c := range ordered {
		if c.CredentialID == stickyID {
			sticky = append(sticky, c)
		} else {
			rest = append(rest, c)
		}
	}
	return append(sticky, rest...)
}

func applyProtocolAffinity(ordered []provider.Candidate, pref []string) []provider.Candidate {
	if len(pref) == 0 || len(ordered) <= 1 {
		return ordered
	}
	prefIndex := make(map[string]int, len(pref))
	for i, p := range pref {
		prefIndex[p] = i
	}
	defaultRank := len(prefIndex)

	sort.SliceStable(ordered, func(i, j int) bool {
		ri := prefIndex[ordered[i].Protocol]
		if _, ok := prefIndex[ordered[i].Protocol]; !ok {
			ri = defaultRank
		}
		rj := prefIndex[ordered[j].Protocol]
		if _, ok := prefIndex[ordered[j].Protocol]; !ok {
			rj = defaultRank
		}
		if ri != rj {
			return ri < rj
		}
		return ordered[i].SuccessRate > ordered[j].SuccessRate
	})
	return ordered
}

// ScoringWeights defines the weights for composite score calculation
type ScoringWeights struct {
	Price           float64 `json:"price"`
	SessionLoad     float64 `json:"session_load"`
	FailurePenalty  float64 `json:"failure_penalty"`
	DefaultPriceCNY float64 `json:"default_price_cny"`
	DefaultPriceUSD float64 `json:"default_price_usd"`
}

// DefaultScoringWeights returns the default scoring weights
func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Price:           10,
		SessionLoad:     5,
		FailurePenalty:  20,
		DefaultPriceCNY: 5.0,
		DefaultPriceUSD: 5.0,
	}
}

// CalculateCompositeScore computes the composite score for a candidate
// Lower score = higher priority. Free models (cost=0) get score=0 (highest priority)
func CalculateCompositeScore(c provider.Candidate, weights ScoringWeights) float64 {
	// Free models get highest priority (score=0)
	cost := blendedCost(c)
	if cost == 0 {
		return 0
	}

	// Start with manual priority (1-99)
	score := float64(c.ManualPriority)

	// Normalize cost based on currency
	var defaultPrice float64
	if c.Currency == "CNY" {
		defaultPrice = weights.DefaultPriceCNY
	} else {
		defaultPrice = weights.DefaultPriceUSD
	}
	if defaultPrice <= 0 {
		defaultPrice = 5.0
	}
	score += (cost / defaultPrice) * weights.Price

	// Session load (0-1)
	if c.ConcurrencyLimit != nil && *c.ConcurrencyLimit > 0 {
		load := float64(c.ActiveSessions) / float64(*c.ConcurrencyLimit)
		if load > 1 {
			load = 1
		}
		score += load * weights.SessionLoad
	}

	// Failure penalty
	score += float64(c.ConsecutiveFailures) * weights.FailurePenalty

	return score
}

// CompareCandidatePriority returns true when a should sort before b.
// Billing round (plan/free before PAYG) takes precedence over composite score.
func CompareCandidatePriority(a, b provider.Candidate) bool {
	ra, rb := provider.BillingRound(a.BillingMode), provider.BillingRound(b.BillingMode)
	if ra != rb {
		return ra < rb
	}
	if a.CompositeScore != b.CompositeScore {
		return a.CompositeScore < b.CompositeScore
	}
	if a.ManualPriority != b.ManualPriority {
		return a.ManualPriority < b.ManualPriority
	}
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	return a.CredentialID < b.CredentialID
}

// SortByCompositeScore sorts candidates by billing round then composite score (ascending).
func SortByCompositeScore(candidates []provider.Candidate, weights ScoringWeights) []provider.Candidate {
	for i := range candidates {
		candidates[i].CompositeScore = CalculateCompositeScore(candidates[i], weights)
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return CompareCandidatePriority(candidates[i], candidates[j])
	})

	return candidates
}

// banditOrder orders candidates using Thompson Sampling bandit algorithm.
// This provides intelligent credential selection based on historical performance.
// Falls back to P2C if any step fails.
func (r *Router) banditOrder(cands []provider.Candidate) []provider.Candidate {
	if len(cands) <= 1 || r.Bandit == nil {
		return cands
	}

	// Score each candidate using Bandit + pressure factor
	type scoredCandidate struct {
		cand  provider.Candidate
		score float64
	}
	scored := make([]scoredCandidate, 0, len(cands))

	for _, c := range cands {
		// Get bandit score (0-1, higher is better)
		credID := fmt.Sprintf("%d", c.CredentialID)
		banditScore := r.Bandit.Sample(credID)

		// Apply pressure factor to avoid overloading high-performing credentials
		pressureFactor := 1.0
		if r.Limiter != nil {
			cred := r.Limiter.Credential(c.ProviderID, c.CredentialID)
			capacity := cred.Capacity()
			if capacity > 0 {
				pressure := float64(cred.Used()) / float64(capacity)
				if pressure > 1.0 {
					pressure = 1.0
				}
				// pressureFactor: 1.0 when empty, 0.0 when saturated
				pressureFactor = 1.0 - pressure
			}
		}

		// Final score: bandit × pressure
		// If credential is saturated (pressureFactor=0), score becomes 0
		finalScore := banditScore * pressureFactor

		scored = append(scored, scoredCandidate{
			cand:  c,
			score: finalScore,
		})
	}

	// Sort by score descending (higher is better)
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		// Tie-breaker: capacity weight (Candidate.Weight, the credential's
		// concurrency capacity) descending so failover ordering stays
		// capacity-proportional; CredentialID is the final stable tiebreak.
		if scored[i].cand.Weight != scored[j].cand.Weight {
			return scored[i].cand.Weight > scored[j].cand.Weight
		}
		return scored[i].cand.CredentialID < scored[j].cand.CredentialID
	})

	// Extract sorted candidates
	result := make([]provider.Candidate, len(scored))
	for i, sc := range scored {
		result[i] = sc.cand
	}

	return result
}

// tryDegradedMode 尝试在单候选者场景下启用降级模式。
// 当唯一的候选者因为瞬态原因（cooling, rate_limited, suspicious）被过滤时，
// 降级使用该候选者而不是返回 model_not_found，避免完全失败。
//
// 这是针对 2026-07-03 minimax-m3 model_not_found 问题的修复：
// 当只有1个候选者且因瞬态原因不可用时，系统会进入完全失败状态，
// 即使该候选者可能在几秒后恢复。降级模式允许在这种情况下继续使用该候选者。
//
// 2026-07-04: 单候选者降级逻辑
//
// 2026-07-14: 增加 StateManager 感知。filterAvailableWithStateManager 过滤候选时
// 不修改候选结构体，被内存态（state:timeout / state:rate_limit 等）过滤掉的候选
// UnavailableReason() 仍是空串。若不在此处补查 StateManager，单点候选被瞬态内存态
// 拒绝时降级永远不触发，直接 0 节点 503（生产事故 ba9fc64f 即此路径）。
// 查询的 reason 与 PlanCandidates 统计分支（router.go reasonCounts）同源：
// state.LastError == string(errKind)，见 credentialstate/manager.go:218,437。
func (r *Router) tryDegradedMode(ctx context.Context, candidates []provider.Candidate) []provider.Candidate {
	var degradedCandidates []provider.Candidate

	for _, c := range candidates {
		reason := c.UnavailableReason()

		// 候选 DB 字段全可用（UnavailableReason 为空）时，回落到 StateManager
		// 查内存态原因。reason 以 "state:" 前缀标记来源，与 PlanCandidates 统计
		// 分支的 "state:"+smReason 拼法保持一致，便于日志关联。
		if reason == "" && r.StateManager != nil && r.StateManager.Enabled() {
			if _, smReason := r.StateManager.IsAvailable(ctx, c.CredentialID, c.RawModel); smReason != "" {
				reason = "state:" + smReason
			}
		}

		if isTransientUnavailableReason(reason) {
			slog.Info("router: degraded mode candidate accepted",
				"credential_id", c.CredentialID,
				"provider_id", c.ProviderID,
				"model", c.RawModel,
				"reason", reason,
			)
			degradedCandidates = append(degradedCandidates, c)
		}
	}

	return degradedCandidates
}

// isTransientUnavailableReason 判断不可用原因是否为瞬态的。
// 瞬态原因包括：
// - availability:cooling - 冷却期，通常几分钟后恢复
// - availability:rate_limited - 速率限制，通常几秒到几分钟后恢复
// - availability:suspended - 临时暂停，可能很快恢复
//
// 以及 StateManager 内存态原因（state:<errKind>，对应 credentialstate/manager.go
// isTransient 四个错误种类，触发 5min cooling）：
//   - state:timeout / state:stream_timeout - 上游超时
//   - state:rate_limit - 上游限流
//   - state:upstream_down - 上游不可用
//   - state:empty_response - 上游 200 + 空流 (NIM 13% 偶发)，同次请求内可能恢复；
//     errorsx.KindEmptyResponse 设计意图明确说"a transient empty burst must
//     not hard-exclude the credential"（classify.go line 60-78）。把这个 kind
//     加到瞬态分支，让单候选降级继续尝试，避免 cred 19 (NIM/endless) 这种
//     "上游正常但 StateManager 标了 empty_response → 0 候选 503" 的误判。
//
// 永久原因（不应降级使用）：
// - availability:auth_failed - 认证失败，需要人工修复
// - quota:balance_exhausted - 余额耗尽，需要充值
// - lifecycle:disabled - 已禁用，需要人工启用
// - state:auth / state:auth_revoked / state:model_not_found / state:quota_permanent
//
// 2026-07-04: 单候选者降级逻辑
// 2026-07-14: 增加 state:<errKind> 瞬态分支。用 errorsx 常量字符串值而非魔法串，
// 与 credentialstate/manager.go:248-251 的 isTransient 集合保持一致，避免漂移。
// 2026-07-18: 增加 state:empty_response（对齐 KindEmptyResponse 设计意图）。
// 2026-07-18: 增加 state:probe_direct_timeout，避免 active probe 探测超时
// 把单候选降级拒之门外。
func isTransientUnavailableReason(reason string) bool {
	switch reason {
	case "availability:cooling",
		"availability:rate_limited",
		"availability:suspended":
		return true
	case "state:" + string(errorsx.KindTimeout),
		"state:" + string(errorsx.KindStreamTimeout),
		"state:" + string(errorsx.KindRateLimit),
		"state:" + string(errorsx.KindUpstreamDown),
		// Without this the single-candidate degraded-mode rescue stops
		// covering overload-shaped 502s and the request fails with no
		// candidates even though the upstream is only shedding load.
		"state:" + string(errorsx.KindUpstreamOverloaded),
		"state:" + string(errorsx.KindEmptyResponse),
		"state:probe_direct_timeout":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Pressure-Aware Routing - Phase 2.3 (2026-07-24)
// ---------------------------------------------------------------------------

// getPressureSignals 获取候选节点的压力信号
// 返回 FpSlots 压力和 Limiter 压力（0-1 之间）
func (r *Router) getPressureSignals(
	ctx context.Context,
	candidate provider.Candidate,
) (fpPressure, limiterPressure float64) {
	// FpSlots 压力
	if r.FpSlots != nil && candidate.FpSlotLimit != nil && *candidate.FpSlotLimit > 0 {
		// 类型断言获取 FingerprintSlotManager
		if fpManager, ok := r.FpSlots.(interface {
			GetPressure(ctx context.Context, credentialID int, slotLimit int) (float64, error)
		}); ok {
			pressure, err := fpManager.GetPressure(ctx, candidate.CredentialID, *candidate.FpSlotLimit)
			if err == nil {
				fpPressure = pressure
			} else {
				// 错误时 fpPressure 保持为 0（fail-open）
				slog.Debug("fpslot pressure query failed",
					"credential_id", candidate.CredentialID,
					"error", err,
				)
				RecordPressureQueryFailure("fpslot")
			}
		}
	}

	// Limiter 压力
	if r.Limiter != nil {
		limiterPressure = r.Limiter.GetPressure(candidate.CredentialID, candidate.ProviderID, "")
	}

	return fpPressure, limiterPressure
}

// applyPressurePenalty 根据压力信号调整候选节点的权重
// 注意：这会修改 candidates 的 Weight 字段
// 调用方需确保已检查 PressureAwareEnabled == true
//
// 参数:
//   - ctx: 上下文
//   - candidates: 候选节点列表
//   - backendName: StateBackend 名称（用于 Prometheus 标签）
func (r *Router) applyPressurePenalty(ctx context.Context, candidates []provider.Candidate, backendName string) {
	if len(candidates) == 0 {
		return
	}

	if backendName == "" {
		backendName = "unknown"
	}

	for i := range candidates {
		// 获取压力信号
		fpPressure, limiterPressure := r.getPressureSignals(ctx, candidates[i])

		// 2026-07-25 Phase 2.4: 记录压力信号到 Prometheus
		if fpPressure > 0 {
			RecordPressureSignal("fp_slots", candidates[i].CredentialID, fpPressure)
		}
		if limiterPressure > 0 {
			RecordPressureSignal("limiter", candidates[i].CredentialID, limiterPressure)
		}

		// 计算压力惩罚
		penalty := calculatePressurePenalty(fpPressure, limiterPressure)

		// 调整权重（降低高压力节点的权重）
		if penalty > 0 {
			originalWeight := candidates[i].Weight
			// 原始权重为 0 时保持不变（不参与压力调整）
			if originalWeight <= 0 {
				continue
			}
			newWeight := int(float64(originalWeight) * (1 - penalty))
			if newWeight < 1 {
				newWeight = 1 // 保留最小权重 1
			}
			candidates[i].Weight = newWeight

			// 2026-07-25 Phase 2.4: 记录到 Prometheus
			RecordPressurePenalty(backendName, candidates[i].RawModel, penalty)

			// 记录惩罚日志（仅在惩罚 > 10% 时）
			if penalty > 0.1 {
				slog.Debug("router: applied pressure penalty",
					"credential_id", candidates[i].CredentialID,
					"provider_id", candidates[i].ProviderID,
					"raw_model", candidates[i].RawModel,
					"backend", backendName,
					"fp_pressure", fpPressure,
					"limiter_pressure", limiterPressure,
					"penalty", penalty,
					"weight_before", originalWeight,
					"weight_after", newWeight,
				)
			}
		}
	}

	// 重新排序（按调整后的权重）
	sortCandidatesByWeight(candidates)
}

// sortCandidatesByWeight 按权重降序排序候选节点
// Weight 越高，优先级越高
func sortCandidatesByWeight(candidates []provider.Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Weight > candidates[j].Weight
	})
}
