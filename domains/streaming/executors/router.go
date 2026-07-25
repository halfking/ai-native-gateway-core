package executors

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialfpslot"
	"github.com/kaixuan/llm-gateway-go/domains/credential"      //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/credentialstate" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/ursm"
	ursmv2 "github.com/kaixuan/llm-gateway-go/domains/ursm/v2"
	ursmv2api "github.com/kaixuan/llm-gateway-go/domains/ursm/v2/api"
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
	// rrCounter is a round-robin counter for load balancing when multiple
	// candidates have equal routing scores. Prevents all requests from
	// always selecting the first candidate in a sorted list.
	rrCounter atomic.Uint64

	// 新增：状态管理器引用（向后兼容）
	StateManager credentialstate.StateProvider

	// 新增：URSM统一路由状态管理器
	URSM *ursm.Manager

	// URSMv2 (2026-07-21, URSM v2 plan T16): 在 canary / authoritative 模式下，
	// PlanCandidates 会委托 v2 Manager.Plan 重新排序/过滤候选，v2 返回 nil 时
	// 回退到原有 ordered 切片。Nil 即保留所有旧行为（main.go 未 wire，运行时为 nil）。
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
	candidates = deduplicateCandidates(candidates)
	if len(candidates) == 0 {
		return nil
	}

	// 新增：优先使用URSM路由（如果可用）
	if r.URSM != nil && r.URSM.Enabled() {
		return r.planWithURSM(candidates, stickyCredentialID, policy, egressPreference)
	}

	// 2026-07-21, URSM v2 plan T20 (convergence): 在 mode=authoritative 模式下
	// 完全关掉旧 credentialstate / IsAvailable 读取，把"哪些候选可用"的判定
	// 委托给 v2 FilterAndScore。Ready 为 false / 调用出错时回退到旧路径，保留
	// 现有行为，避免一次错误的 v2 调用造成全量 503。URSMv2 == nil 时代码路径
	// 是死的（main.go 默认 URSM_V2_MODE=off 时根本不会 wire）。
	//
	// TODO(T20+): PlanCandidates 签名没有 tenantID / canonical / reqID；
	// FilterAndScore 的 seed 里 TenantID 暂传空串，与 T16 planWithURSMv2
	// 的 "" 透传一致，等待后续任务把请求级上下文接进来。
	if r.URSMv2 != nil && r.URSMv2.Mode() == ursmv2api.ModeAuthoritative {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		if r.URSMv2.Ready(ctx) {
			seeds := make([]ursmv2.CandidateSeed, 0, len(candidates))
			for _, c := range candidates {
				seeds = append(seeds, ursmv2.CandidateSeed{
					ProviderID:   c.ProviderID,
					CredentialID: c.CredentialID,
					RawModel:     c.RawModel,
					Canonical:    c.StandardizedName,
					TenantID:     "",
					PriceIn:      derefPrice(c.PriceInPer1M),
					PriceOut:     derefPrice(c.PriceOutPer1M),
					BillingMode:  c.BillingMode,
					Trust:        0,
					BaseURLMs:    c.P50LatencyMs,
				})
			}
			views, err := r.URSMv2.FilterAndScore(ctx, seeds)
			if err != nil {
				// URSM v2 FilterAndScore 失败时，记录错误并继续使用原始候选
				// 这是 fail-open 设计：优先保证可用性，不因 Redis 问题阻塞路由
				slog.Warn("router: URSM v2 FilterAndScore failed, failing open",
					"error", err,
					"seed_count", len(seeds),
					"mode", r.URSMv2.Mode(),
				)
			} else {
				allow := make(map[int]bool, len(views))
				for _, v := range views {
					if v.Available {
						allow[v.CredentialID] = true
					}
				}
				filtered := candidates[:0]
				for _, c := range candidates {
					if allow[c.CredentialID] {
						filtered = append(filtered, c)
					}
				}
				candidates = filtered
				if len(candidates) == 0 {
					return nil
				}
				}
			}
		}

		// 2026-07-24 Phase 2.3: 应用压力惩罚（feature flag 控制）
		// 在 URSM v2 过滤和评分之后，根据 FpSlots/Limiter 压力调整权重
		// 使用独立的 pressureCtx 避免遮蔽外层 ctx
		if r.PressureAwareEnabled && len(candidates) > 0 {
			pressureCtx, pressureCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			r.applyPressurePenalty(pressureCtx, candidates)
			pressureCancel()
		}

		// 2026-07-24 Phase 1: 使用统一的状态后端接口，消除散落的条件判断。
	// 一次性决定使用 URSM v2 / StateManager / DB-only 哪套系统。
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stateBackend := selectStateBackend(r.URSMv2, r.StateManager, ctx)
	available := stateBackend.FilterAvailable(ctx, candidates)

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
			if reason == "" && r.StateManager != nil && r.StateManager.Enabled() {
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
	ordered := r.planByTier(round1, policy)
	if len(round2) > 0 {
		ordered = append(ordered, r.planByTier(round2, policy)...)
	}

	// Note (2026-07-07 audit): an earlier "session-aware" round-robin rotation
	// of the full `ordered` slice lived here. It was added in cf65803f to spread
	// new-session traffic when loadScore values tie, but it rotated ACROSS tiers
	// — which violates tier priority semantics (a tier-3 candidate could be
	// tried before a tier-1 candidate). Per-tier load balancing is already
	// performed inside planByTier (rotateCandidates per tier bucket), so the
	// cross-tier rotation here was both redundant and harmful. Removed to
	// restore correct tier ordering; the per-tier rotation in planByTier still
	// provides even distribution for candidates within the same tier.

	if stickyCredentialID != nil {
		ordered = prioritizeSticky(ordered, *stickyCredentialID)
	}

	if len(egressPreference) > 0 {
		ordered = applyProtocolAffinity(ordered, egressPreference)
	}

	// 2026-07-21, URSM v2 plan T16: 在 canary / authoritative 模式下把 ordering
	// 委托给 v2 Manager.Plan。v2 返回 nil 时回退到上面的 ordered 切片，保留旧
	// 行为。URSMv2 == nil 时整段不进入；main.go 尚未 wire，运行时为 nil。
	//
	// T16 暂不传入 tenant / canonical / reqID：PlanCandidates 签名没有这些参数
	// 且现有调用方没传递；URSMv2 == nil 时代码路径本来就是死的。等到 T20 等
	// 后续任务把 tenant/canonical/reqID 透传过来，再接入 r.URSMv2.ShouldUseV2
	// 的金丝雀白名单过滤。当前仅按 Mode 粗粒度生效 (Canary/Authoritative)。
	if r.URSMv2 != nil {
		mode := r.URSMv2.Mode()
		if mode == ursmv2api.ModeCanary || mode == ursmv2api.ModeAuthoritative {
			if v2Ordered := r.planWithURSMv2(ordered); v2Ordered != nil {
				ordered = v2Ordered
			}
		}
	}

	return ordered
}

// planWithURSMv2 asks the v2 Manager to re-rank the input candidates. It
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
		key := seedLookupKey(c.CredentialID, c.RawModel)
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
		key := seedLookupKey(s.CredentialID, s.RawModel)
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

func seedLookupKey(credentialID int, rawModel string) string {
	return strconv.Itoa(credentialID) + "|" + rawModel
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

// planWithURSM 使用URSM路由（新增）
func (r *Router) planWithURSM(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	if len(candidates) == 0 {
		return nil
	}

	// 提取model和sessionID
	model := candidates[0].RawModel
	sessionID := "" // TODO: 从context或请求参数中获取

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// 调用URSM获取可用节点
	nodes, err := r.URSM.GetAvailableNodes(ctx, model, sessionID)
	if err != nil {
		slog.Warn("ursm get nodes failed, fallback to legacy",
			"error", err,
			"model", model)
		// 回退到旧逻辑
		return r.planLegacy(candidates, stickyCredentialID, policy, egressPreference)
	}

	// 转换RouteNode到Candidate
	result := make([]provider.Candidate, 0, len(nodes))
	for _, node := range nodes {
		// 在原始candidates中找到匹配的Candidate
		for _, cand := range candidates {
			if cand.CredentialID == node.CredentialID && cand.RawModel == node.RawModel {
				result = append(result, cand)
				break
			}
		}
	}

	// 应用sticky偏好
	if stickyCredentialID != nil {
		result = prioritizeSticky(result, *stickyCredentialID)
	}

	// 应用协议偏好
	if len(egressPreference) > 0 {
		result = applyProtocolAffinity(result, egressPreference)
	}

	return result
}

// planLegacy 保留旧逻辑（向后兼容）
func (r *Router) planLegacy(
	candidates []provider.Candidate,
	stickyCredentialID *int,
	policy *provider.Policy,
	egressPreference []string,
) []provider.Candidate {
	// 使用状态管理器过滤（如果启用）
	var available []provider.Candidate
	if r.StateManager != nil && r.StateManager.Enabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		available = r.filterAvailableWithStateManager(ctx, candidates)
	} else {
		available = filterAvailable(candidates)
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
			if reason == "" && r.StateManager != nil && r.StateManager.Enabled() {
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
		slog.Warn("router: all candidates unavailable",
			"total", len(candidates),
			"reasons", reasonCounts,
			"sample", sampleReasons,
		)
		return nil
	}

	available = r.filterHealthyNodes(available)

	// Round 1: token_plan / code_plan / agent_plan / free — always before PAYG.
	// Round 2: token (按量). Executor skips saturated round-1 creds and falls through.
	round1, round2 := splitByBillingRound(available)
	ordered := r.planByTier(round1, policy)
	if len(round2) > 0 {
		ordered = append(ordered, r.planByTier(round2, policy)...)
	}

	if stickyCredentialID != nil {
		ordered = prioritizeSticky(ordered, *stickyCredentialID)
	}

	if len(egressPreference) > 0 {
		ordered = applyProtocolAffinity(ordered, egressPreference)
	}

	return ordered
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

func (r *Router) planByTier(candidates []provider.Candidate, policy *provider.Policy) []provider.Candidate {
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

		// Apply round-robin rotation when multiple candidates exist
		// This prevents always selecting the first candidate when scores are equal
		if len(sorted) > 1 {
			// Keep the first plan in score order. Subsequent plans rotate
			// across the bucket so the initial routing decision remains
			// deterministic while repeated requests are balanced.
			counter := r.rrCounter.Add(1) - 1
			offset := int(counter % uint64(len(sorted)))
			slog.Info("ROUND_ROBIN_DEBUG", "counter", r.rrCounter.Load(), "offset", offset, "bucket_size", len(sorted))
			sorted = rotateCandidates(sorted, offset)
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

// rotateCandidates circularly shifts the candidate slice by offset positions.
// This ensures fair load distribution when multiple candidates have equal scores.
func rotateCandidates(cands []provider.Candidate, offset int) []provider.Candidate {
	if offset == 0 || len(cands) <= 1 {
		return cands
	}
	offset = offset % len(cands)
	out := make([]provider.Candidate, len(cands))
	for i := range cands {
		out[i] = cands[(i+offset)%len(cands)]
	}
	return out
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
			// 2026-06-25: When load scores are equal (e.g., both free local
			// mocks with same routing_score), P2C previously always picked 'a'
			// (the first random sample). This biased load distribution toward
			// whichever candidate happened to be drawn first, causing 83/17
			// splits instead of 50/50. Fix: randomize on equal scores to
			// match the round-robin rotation done at the planByTier level.
			if rand.Intn(2) == 0 {
				chosen = b
			}
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
		// Tie-breaker: credential ID
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
			}
			// 错误时 fpPressure 保持为 0（fail-open）
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
func (r *Router) applyPressurePenalty(ctx context.Context, candidates []provider.Candidate) {
	if len(candidates) == 0 {
		return
	}

	for i := range candidates {
		// 获取压力信号
		fpPressure, limiterPressure := r.getPressureSignals(ctx, candidates[i])

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

			// 记录惩罚日志（仅在惩罚 > 10% 时）
			if penalty > 0.1 {
				slog.Debug("router: applied pressure penalty",
					"credential_id", candidates[i].CredentialID,
					"provider_id", candidates[i].ProviderID,
					"raw_model", candidates[i].RawModel,
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
	// 注意：这里假设 Weight 越高越优先，如果相反则需要调整
	sortCandidatesByWeight(candidates)
}

// sortCandidatesByWeight 按权重降序排序候选节点
// Weight 越高，优先级越高
func sortCandidatesByWeight(candidates []provider.Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Weight > candidates[j].Weight
	})
}
