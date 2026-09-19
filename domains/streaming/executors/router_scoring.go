package executors

import (
	"context"
	"log/slog"
	"math"
	"math/rand"
	"os"
	"strconv"
	"time"

	"github.com/kaixuan/llm-gateway-go/modeliqdata"
	"github.com/kaixuan/llm-gateway-go/provider"
)

// LoadScoreWeights 定义路由评分的权重配置（Phase 1）
type LoadScoreWeights struct {
	ConcurrencyWeight float64 // 全局并发压力权重
	IdentityWeight    float64 // 单 identity 压力权重
	LatencyWeight     float64 // 响应延迟权重
	QualityWeight     float64 // 成功率权重
	// CostWeight / IQWeight 是 M2 RT-2 的可选扩展维度，默认 0（关闭）：
	// 关闭时 composite 与历史公式逐字节一致（OffIsByteIdentical 测试钉住）。
	//   - CostWeight：shadow cost-optimized 策略的混合单价维度进入生效评分
	//     （calculateCostPenalty，未知价格=最大惩罚，不是免费）。
	//   - IQWeight：模型标准 IQ（AA Intelligence Index 0-100）进入生效评分
	//     （calculateIQPenalty，未知 IQ=中性 0.5，fail-open）。
	CostWeight float64 // 可选：混合单价（in+out per 1M）惩罚权重
	IQWeight   float64 // 可选：标准 IQ 惩罚权重
}

// DefaultLoadScoreWeights 返回默认权重配置
//
// Audit 2026-09-14 R28 #13: this constructor is the ONLY authority for the
// live-routing hot path (P2C composite in calculateLoadScore ← Router.LoadScoreWeights).
// The admin knob routing_policy.scoring_weights_json (admin /api/routing/
// scoring-weights GET/PATCH) is a DIAGNOSTIC PREVIEW only — it feeds
// /api/routing/resolve and /api/routing/score-details, never this hot path.
// Admin responses carry "display_only":true + "note" to disclose that.
//
// CostWeight/IQWeight（M2 RT-2）默认 0（关闭）：关闭时评分与历史公式完全一致。
// 通过 env 开启：LLM_GATEWAY_ROUTING_W_COST / LLM_GATEWAY_ROUTING_W_IQ
// （沿用本文件 W_HEADROOM/W_CAPACITY 的 env 惯例），非法值回落 0。
func DefaultLoadScoreWeights() LoadScoreWeights {
	return LoadScoreWeights{
		ConcurrencyWeight: 0.4,
		IdentityWeight:    0.1,
		LatencyWeight:     0.3,
		QualityWeight:     0.2,
		CostWeight:        envFloat("LLM_GATEWAY_ROUTING_W_COST", 0),
		IQWeight:          envFloat("LLM_GATEWAY_ROUTING_W_IQ", 0),
	}
}

// calculateLoadScore 计算凭据的综合负载分数
// 分数越低越好（越可能被选中）—— P2C 选择在 router.go 中取 min。
//
// 方向一致性：composite 的每一项都必须是「惩罚」。
//   - concurrency / identity / quality：天然是惩罚。
//   - latencyScore / headroom：calculateLatencyScore / calculateHeadroom 返回
//     「健康度/奖励」（越大越好，见 docs/design/2026-07-20-latency-aware-routing.md
//     §2.1 分段表：< 800ms → 1.0）。此处用 (1 - x) 转成惩罚后相加，
//     否则低延迟/高 headroom 的好凭据 composite 偏高被 P2C 惩罚，与设计意图
//     （偏好低延迟、高 headroom）相反。
//     2026-07-24 审计修正：60809965 重写后直接相加导致方向反转。
func calculateLoadScore(c provider.Candidate, r *Router, ctx context.Context, weights LoadScoreWeights) float64 {
	concurrencyScore := calculateConcurrencyScore(c, r, ctx)
	identityScore := calculateIdentityScore(c, r)
	latencyScore := calculateLatencyScore(c, r)
	qualityScore := calculateQualityScore(c)

	headroom := calculateHeadroom(c, r)
	headroomWeight := envFloat("LLM_GATEWAY_ROUTING_W_HEADROOM", 0.05)
	latencyPenalty := 1.0 - latencyScore
	headroomPenalty := 1.0 - headroom
	// Capacity nudge (2026-08-13): a deliberately tiny term so the credential's
	// concurrency capacity (Candidate.Weight) biases the P2C score in favor of
	// higher-capacity nodes even outside exact ties. It saturates at the default
	// weight (100) so normal pools are unaffected and only sub-default nodes get
	// a small penalty; the dominant load/health terms and the capacity-aware
	// tie-breaks (pickWeightedTie / banditOrder) carry the real weighting. Set
	// LLM_GATEWAY_ROUTING_W_CAPACITY=0 to disable entirely.
	capacityWeight := envFloat("LLM_GATEWAY_ROUTING_W_CAPACITY", 0.02)
	capacityPenalty := capacityPenaltyForWeight(c.Weight)
	composite :=
		concurrencyScore*weights.ConcurrencyWeight +
			identityScore*weights.IdentityWeight +
			latencyPenalty*weights.LatencyWeight +
			qualityScore*weights.QualityWeight +
			headroomPenalty*headroomWeight + // 高 headroom → 低惩罚 → 更易被选中
			capacityPenalty*capacityWeight

	// M2 RT-2 (2026-08-15): optional cost/IQ dimensions from the shadow
	// strategies enter the effective score. Guarded so the default (weights
	// zero / off) hot path stays byte-identical to the pre-RT-2 composite.
	var costPenalty, iqPenalty float64
	if weights.CostWeight > 0 {
		costPenalty = calculateCostPenalty(c)
		composite += costPenalty * weights.CostWeight
	}
	if weights.IQWeight > 0 {
		iqPenalty = calculateIQPenalty(c)
		composite += iqPenalty * weights.IQWeight
	}

	// 2026-09-19 sticky-session load balancing: 新会话节点选择的三个新维度。
	//   - sticky 惩罚：该节点 5 分钟滑窗内的 sticky 会话数 / 并发容量，
	//     会话多且容量小的节点被压低，按容量拉平各节点承载的会话量，
	//     降低上游按并发会话封禁的风险；
	//   - recency 惩罚：节点最近一次请求距今越近惩罚越高（默认 30s
	//     线性衰减到 0），把突发的新会话在毫级粒度上摊开；
	//   - balance 惩罚：按量计费（billing round 2）凭据余额低于水位
	//     线（默认 $5）线性加重，0 余额=1；订阅/免费凭据不适用。
	// sticky/recency 仅在 Router.StickyLoad 接线后生效（未接线=纯 0，
	// 与历史 composite 逐字节一致，测试/旧部署零影响）；balance 只依赖
	// candidate 字段。三者权重均可用 env 置 0 整体关闭。
	stickyWeight := envFloat("LLM_GATEWAY_ROUTING_W_STICKY", 0.15)
	recencyWeight := envFloat("LLM_GATEWAY_ROUTING_W_RECENCY", 0.05)
	balanceWeight := envFloat("LLM_GATEWAY_ROUTING_W_BALANCE", 0.05)
	var stickyPenalty, recencyPenalty float64
	if r != nil && r.StickyLoad != nil {
		if stickyWeight > 0 {
			stickyPenalty = stickySessionPenalty(c, r)
		}
		if recencyWeight > 0 {
			recencyPenalty = recentRequestPenalty(c, r)
		}
	}
	var balancePenalty float64
	if balanceWeight > 0 {
		balancePenalty = balancePenaltyForCandidate(c)
	}
	composite += stickyPenalty*stickyWeight +
		recencyPenalty*recencyWeight +
		balancePenalty*balanceWeight

	if rand.Float64() < 0.1 {
		slog.Info("LOAD_SCORE_V2",
			"credential_id", c.CredentialID,
			"concurrency_score", concurrencyScore,
			"identity_score", identityScore,
			"latency_score", latencyScore,
			"latency_penalty", latencyPenalty,
			"quality_score", qualityScore,
			"headroom", headroom,
			"headroom_penalty", headroomPenalty,
			"capacity_weight", c.Weight,
			"capacity_penalty", capacityPenalty,
			"cost_penalty", costPenalty,
			"iq_penalty", iqPenalty,
			"sticky_penalty", stickyPenalty,
			"recency_penalty", recencyPenalty,
			"balance_penalty", balancePenalty,
			"composite", composite,
		)
	}

	return composite
}

// calculateCostPenalty normalizes the shadow cost-optimized dimension — the
// blended unit price (PriceInPer1M + PriceOutPer1M, USD per 1M tokens) — into
// a 0..1 penalty for calculateLoadScore (M2 RT-2).
//
// Unknown-value semantics (README §5 R1, mirrors strategy_cost.go): an unknown
// price is NOT free → max penalty 1.0, so unpriced candidates never outrank
// cheap known ones when CostWeight is on. Known prices rise linearly with the
// blended price and saturate at the soft cap (env LLM_GATEWAY_ROUTING_COST_CAP,
// default $30/1M blended — roughly the premium-model frontier) so a single
// expensive outlier cannot dominate the composite.
func calculateCostPenalty(c provider.Candidate) float64 {
	if c.PriceInPer1M == nil || c.PriceOutPer1M == nil {
		return 1.0
	}
	cap := envFloat("LLM_GATEWAY_ROUTING_COST_CAP", 30)
	if cap <= 0 {
		cap = 30
	}
	blended := *c.PriceInPer1M + *c.PriceOutPer1M
	if blended <= 0 {
		return 0
	}
	p := blended / cap
	if p > 1.0 {
		p = 1.0
	}
	return p
}

// calculateIQPenalty turns the model's standard IQ (AA Intelligence Index,
// 0-100 — the same reference table as autoroute's RT-1 gate) into a 0..1
// penalty for calculateLoadScore (M2 RT-2): higher IQ → lower penalty.
//
// Unknown-value semantics: an unknown IQ is NEUTRAL 0.5 (fail-open, matching
// RT-1's gate semantics) — an unknown model is neither rewarded nor punished,
// so a stale reference table cannot empty/bias the pool. Resolution order:
// StandardizedName first, then RawModel.
func calculateIQPenalty(c provider.Candidate) float64 {
	name := c.StandardizedName
	if name == "" {
		name = c.RawModel
	}
	iq, found, _ := modeliqdata.LookupStandardIQ(name)
	if !found {
		return 0.5
	}
	if iq < 0 {
		iq = 0
	}
	if iq > 100 {
		iq = 100
	}
	return 1.0 - iq/100.0
}

// capacityPenaltyForWeight turns a candidate's capacity weight (the credential's
// concurrency capacity, see provider.applyCapacityWeightedLB) into a 0..1
// penalty for calculateLoadScore. The default/unknown weight (100) and any
// larger weight map to 0 (no penalty); sub-default weights rise linearly to 1.
// Unknown/zero weights are treated as the default so they are never penalized.
const defaultCapacityWeight = 100

func capacityPenaltyForWeight(weight int) float64 {
	if weight <= 0 {
		weight = defaultCapacityWeight
	}
	if weight >= defaultCapacityWeight {
		return 0
	}
	return 1.0 - float64(weight)/float64(defaultCapacityWeight)
}

// calculateConcurrencyScore 计算全局并发压力分数
// 返回 0.0-1.0，值越大表示压力越大
//
// 2026-09-09 P0 修复：dispatch 路径走 AcquireAllNoCredLayer 绕过了 Limiter
// 的 credential 信号量，所以 r.Limiter.Credential().Used() 在生产环境恒为
// 0，导致 P2C 永远靠 weight tie-break 分摊，而不是按实时并发分摊。
// 优先读 Router.LiveLoad（dispatch 真正 Acquire 的 PeakCollector）；
// 仅在 LiveLoad 不可用或返回 0 时回退到 Limiter 的语义。
func calculateConcurrencyScore(c provider.Candidate, r *Router, ctx context.Context) float64 {
	if r == nil {
		return 0.5
	}
	capacity := concurrencyCapacity(c, r)
	if capacity <= 0 {
		return 0.5
	}

	used := liveInFlight(c, r)
	if used <= 0 && r.Limiter != nil {
		// Fallback: limiter may carry stale-but-non-zero signal in legacy
		// deployments where dispatch_v2 is off and dispatch uses AcquireAll.
		used = int64(r.Limiter.Credential(c.ProviderID, c.CredentialID).Used())
	}
	pressure := float64(used) / float64(capacity)
	if pressure > 1.0 {
		pressure = 1.0
	}
	return pressure
}

// calculateIdentityScore 计算单 identity 压力分数
//
// 同样的修复：优先读 LiveLoad，再回退 Limiter；同一份实时信号被两个
// scoring 维度共用，避免 weight tie-break 替代真正的并发分摊。
func calculateIdentityScore(c provider.Candidate, r *Router) float64 {
	if r == nil {
		return 0.5
	}

	capacity := concurrencyCapacity(c, r)
	if capacity <= 0 {
		return 0.5
	}

	used := liveInFlight(c, r)
	if used <= 0 && r.Limiter != nil {
		used = int64(r.Limiter.Credential(c.ProviderID, c.CredentialID).Used())
	}
	pressure := float64(used) / float64(capacity)
	if pressure > 1.0 {
		pressure = 1.0
	}
	return pressure
}

// concurrencyCapacity returns the per-credential capacity for scoring.
//
// Two valid source paths exist, depending on which dispatch path is wired:
//
//  1. dispatch_v2 (LiveLoad wired): PeakCollector is the source of truth
//     for in-flight counts; the Limiter's credential semaphore is bypassed
//     (AcquireAllNoCredLayer) and its seeded capacity is just the global
//     default (DefaultCredentialLimit = 50). We MUST use the per-credential
//     ConcurrencyLimit from the candidate snapshot (sourced from the DB),
//     otherwise every credential looks underused and the saturation
//     penalty never fires.
//
//  2. Legacy dispatch (LiveLoad nil): Limiter is the source of truth
//     for both count and capacity (AcquireAll acquires the credential
//     semaphore). The Limiter may have been hot-updated via
//     SetCredentialCapacity, so its Capacity() reflects the live
//     effective limit and is preferred over the DB snapshot.
//
// The signal that distinguishes the two is Router.LiveLoad. We do NOT
// prefer Limiter when LiveLoad is wired because its capacity would be
// the global default rather than the per-credential limit.
func concurrencyCapacity(c provider.Candidate, r *Router) int {
	if r.LiveLoad != nil {
		// dispatch_v2 path: take capacity from the candidate snapshot
		// (sourced from credentials.concurrency_limit in the DB).
		if c.ConcurrencyLimit != nil && *c.ConcurrencyLimit > 0 {
			return *c.ConcurrencyLimit
		}
		return 0
	}
	// Legacy path: Limiter is authoritative.
	if r.Limiter != nil {
		if cap := r.Limiter.Credential(c.ProviderID, c.CredentialID).Capacity(); cap > 0 {
			return cap
		}
	}
	if c.ConcurrencyLimit != nil && *c.ConcurrencyLimit > 0 {
		return *c.ConcurrencyLimit
	}
	return 0
}

// liveInFlight returns the live in-flight count for this credential-model
// pair from Router.LiveLoad. Returns 0 when the provider is nil (which is
// the signal for "fall back to Limiter").
func liveInFlight(c provider.Candidate, r *Router) int64 {
	if r.LiveLoad == nil {
		return 0
	}
	return r.LiveLoad.GetLiveConcurrent(int64(c.CredentialID), c.RawModel)
}

// calculateLatencyScore 计算延迟分数
// 使用饱和曲线：快速增长后趋于平缓
// 2026-07-20 P2-#5: concurrency-aware latency score (docs/design/2026-07-20-latency-aware-routing.md §2.1)
// idle slope × queue-amplification
func calculateLatencyScore(c provider.Candidate, r *Router) float64 {
	pressure := candidatePressure(c, r) // 复用 headroom 计算
	p95 := c.P95LatencyMs
	if p95 < 100 {
		return 0.0
	}
	// queue-amplification: pressure 超过 knee (0.6) 越多, p95 被放大
	// alpha=1.2, beta=1.8, knee=0.6 (env LLM_GATEWAY_PRESSURE_* 可改)
	alpha := envFloat("LLM_GATEWAY_PRESSURE_ALPHA", 1.2)
	beta := envFloat("LLM_GATEWAY_PRESSURE_BETA", 1.8)
	knee := envFloat("LLM_GATEWAY_PRESSURE_KNEE", 0.6)
	amp := 1.0
	if pressure > knee {
		delta := pressure - knee
		amp = 1.0 + alpha*mathPow(delta, beta)
	}
	observed := float64(p95) * amp
	// piecewise table (在 amplified observed 上)
	switch {
	case observed < 800:
		return 1.00
	case observed < 1500:
		return lerp(observed, 800, 1500, 1.00, 0.85)
	case observed < 3000:
		return lerp(observed, 1500, 3000, 0.85, 0.65)
	case observed < 10000:
		return lerp(observed, 3000, 10000, 0.65, 0.30)
	case observed < 30000:
		return lerp(observed, 10000, 30000, 0.30, 0.05)
	default:
		return 0.0 // hard block (> block threshold 默认 30s)
	}
}

// mathPow 包装 math.Pow, 处理 x<=0 边界
func mathPow(x, y float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Pow(x, y)
}

// lerp 线性插值
func lerp(x, x0, x1, y0, y1 float64) float64 {
	if x1 == x0 {
		return y0
	}
	return y0 + (y1-y0)*(x-x0)/(x1-x0)
}

// envFloat 从 env 读 float 配 fallback
func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return f
}

// candidatePressure 计算 candidate 当前的并发压力
// (在 0-1.5 范围: 1.0+ 表示超载)
//
// 2026-09-09 P0 修复：和 calculateConcurrencyScore 一样，优先读
// Router.LiveLoad（dispatch 真正 Acquire 的 PeakCollector），再回退
// 到 Limiter、ConcurrencyLimit、ActiveSessions 三级 fallback。
// 之前 dispatch_v2 路径下恒返回 0 / 0.5（取决于 Limiter 与 ConcurrencyLimit
// 是否存在），让 calculateHeadroom / calculateLatencyScore 完全失去信号。
func candidatePressure(c provider.Candidate, r *Router) float64 {
	if r == nil {
		return 0.5
	}
	capacity := concurrencyCapacity(c, r)
	if capacity > 0 {
		used := liveInFlight(c, r)
		if used <= 0 && r.Limiter != nil {
			// Legacy dispatch (AcquireAll) carries non-zero here.
			used = int64(r.Limiter.Credential(c.ProviderID, c.CredentialID).Used())
		}
		if used > 0 {
			p := float64(used) / float64(capacity)
			if p > 1.0 {
				p = 1.0
			}
			return p
		}
		return 0.5 // 有限流但实时 in-flight=0: 中性
	}
	// 无 capacity 线索（既没有 ConcurrencyLimit 也没有 Limiter）: 中性
	return 0.5
}

// calculateHeadroom bonus (P2-#5 §3.1): 奖励并发富裕的 candidate
func calculateHeadroom(c provider.Candidate, r *Router) float64 {
	gamma := envFloat("LLM_GATEWAY_HEADROOM_GAMMA", 1.0)
	pressure := candidatePressure(c, r)
	if pressure > 1.0 {
		return 0.0
	}
	return mathPow(1.0-pressure, gamma)
}

// calculateQualityScore 计算质量分数（基于成功率）
func calculateQualityScore(c provider.Candidate) float64 {
	quality := c.SuccessRate

	// 优先使用最近成功率
	if c.RecentSuccessRate != nil && c.RecentSamples >= 10 {
		quality = *c.RecentSuccessRate
	}

	// 质量低 → 分数高（惩罚）
	// 95% → 0.05, 80% → 0.20, 50% → 0.50
	return 1.0 - quality
}

// stickySessionPenalty 把"该节点 5 分钟滑窗内的 sticky 会话数"按其并发
// 容量归一成 0..1 惩罚（2026-09-19 sticky-session load balancing）。
// 会话数达到容量 → 1.0；无会话/无信号 → 0。容量未知时用保守默认值
// （env LLM_GATEWAY_STICKYLOAD_DEFAULT_CAPACITY，默认 8），避免无
// concurrency_limit 的凭据被误判为无限容量。
func stickySessionPenalty(c provider.Candidate, r *Router) float64 {
	info := r.StickyLoad.Info(c.CredentialID)
	if info.Sessions <= 0 {
		return 0
	}
	capacity := stickySessionCapacity(c, r)
	ratio := float64(info.Sessions) / float64(capacity)
	if ratio > 1.0 {
		ratio = 1.0
	}
	return ratio
}

func stickySessionCapacity(c provider.Candidate, r *Router) int {
	if cap := concurrencyCapacity(c, r); cap > 0 {
		return cap
	}
	return stickyLoadEnvInt("LLM_GATEWAY_STICKYLOAD_DEFAULT_CAPACITY", 8)
}

// recentRequestPenalty 以"节点最近一次请求时间"做突发平滑（2026-09-19）：
// 距今 < 地平线（默认 30s，env LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS）
// 线性衰减 1→0；无信号（从未活跃）= 0（空闲节点最优先）。
func recentRequestPenalty(c provider.Candidate, r *Router) float64 {
	info := r.StickyLoad.Info(c.CredentialID)
	if info.LastActivityMs <= 0 {
		return 0
	}
	ageMs := time.Now().UnixMilli() - info.LastActivityMs
	if ageMs <= 0 {
		return 1.0
	}
	horizonMs := envFloat("LLM_GATEWAY_ROUTING_RECENCY_HORIZON_SECONDS", 30) * 1000
	if horizonMs <= 0 {
		return 0
	}
	if ageMs >= int64(horizonMs) {
		return 0
	}
	return 1.0 - float64(ageMs)/horizonMs
}

// balancePenaltyForCandidate 把按量计费凭据的美元余额归一成 0..1 惩罚
// （2026-09-19）：低于水位线（env LLM_GATEWAY_ROUTING_BALANCE_WATERMARK，
// 默认 $5）线性加重，≤0 → 1.0；≥水位线 → 0。
// 订阅/免费/计划类凭据（billing round 1）余额不是约束 → 0；
// 余额未知（nil）fail-open → 0，不惩罚。
func balancePenaltyForCandidate(c provider.Candidate) float64 {
	if c.BalanceUSD == nil {
		return 0
	}
	if provider.BillingRound(c.BillingMode) != 2 {
		return 0
	}
	bal := *c.BalanceUSD
	if bal <= 0 {
		return 1.0
	}
	watermark := envFloat("LLM_GATEWAY_ROUTING_BALANCE_WATERMARK", 5)
	if watermark <= 0 || bal >= watermark {
		return 0
	}
	return 1.0 - bal/watermark
}
