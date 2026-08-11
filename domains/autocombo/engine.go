package autocombo

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// resetWindowHorizon is the "full reset cycle" over which resetWindowAffinity
// decays from 1 (just reset) toward 0. Mirrors OmniRoute's per-window horizon
// (monthly=30d default); we use a single 7d horizon since most free tiers
// reset daily/weekly. A credential that resets in <7d scores >0; beyond that
// it trends toward 0 (still waiting on a far-future reset).
const resetWindowHorizon = 7 * 24 * time.Hour

// Engine 评分与选择引擎
type Engine struct {
	weights ScoringWeights
	// variant: 用于 TaskFit 维度的 keyword 启发式. 不传时 TaskFit 退化为
	// 常量 0.85 (round 4 L7 之前的旧 behavior, 保持向后兼容).
	variant Variant
}

// NewEngine 创建引擎
//
// round 3 audit M2 修复: 在 unmarshal 后校验 sum(weights) ∈ [0.99, 1.01],
// 否则返回 error. 旧实现对全 0 weights 静默生成全 0 评分, 候选平局, 行为
// 不可预测. 真实生产中 weights 一般由 resolver 内置模板或 DB 行提供,
// 校验后能给运维更明确的错误信息 ("weight sum 0.85, expected ~1.0").
func NewEngine(weightsJSON json.RawMessage) (*Engine, error) {
	return NewEngineWithVariant(weightsJSON, "")
}

// NewEngineWithVariant 创建带 variant 的引擎. variant 用于 TaskFit 启发式,
// 是 round 4 L7 的扩展; 旧调用 NewEngine 等价于 variant="" 通用模式.
func NewEngineWithVariant(weightsJSON json.RawMessage, variant Variant) (*Engine, error) {
	var weights ScoringWeights
	if err := json.Unmarshal(weightsJSON, &weights); err != nil {
		return nil, fmt.Errorf("unmarshal scoring weights: %w", err)
	}

	total := weights.HealthScore + weights.LatencyP95 +
		weights.QuotaRemaining + weights.Cost +
		weights.TaskFit + weights.TierAffinity
	if total < 0.99 || total > 1.01 {
		return nil, fmt.Errorf("scoring weights sum %.3f outside [0.99, 1.01] (raw: %+v)",
			total, weights)
	}

	return &Engine{weights: weights, variant: variant}, nil
}

// SelectCandidate 从候选池选择一个候选（保留以兼容 autocombo.Candidate 单元测试）
func (e *Engine) SelectCandidate(pool []Candidate) (*Candidate, error) {
	if len(pool) == 0 {
		return nil, fmt.Errorf("empty candidate pool")
	}

	// 1. 计算评分
	scored := e.scoreAll(pool)

	// 2. 分层
	tiers := e.splitTiers(scored)

	// 3. 选择策略
	if len(tiers.Top) > 0 {
		winner := tiers.Top[0]
		// 如果有明显优胜者（领先 >= 0.1）
		if len(tiers.Top) > 1 && winner.Score-tiers.Top[1].Score >= 0.1 {
			return &winner.Candidate, nil
		}
		// 否则在 top tier 内轮换
		return e.roundRobin(tiers.Top), nil
	}

	if len(tiers.Mid) > 0 {
		return e.roundRobin(tiers.Mid), nil
	}

	if len(tiers.Rest) > 0 {
		return e.roundRobin(tiers.Rest), nil
	}

	return nil, fmt.Errorf("no viable candidates")
}

// scoreAll 计算所有候选的评分（保留以兼容 autocombo.Candidate 单元测试）
func (e *Engine) scoreAll(pool []Candidate) []ScoredCandidate {
	scored := make([]ScoredCandidate, len(pool))

	// 归一化因子
	maxLatency := 0
	for _, c := range pool {
		if c.LatencyP95 > maxLatency {
			maxLatency = c.LatencyP95
		}
	}
	if maxLatency == 0 {
		maxLatency = 1000 // 默认
	}

	maxCost := 0.0
	for _, c := range pool {
		if c.CostPer1M > maxCost {
			maxCost = c.CostPer1M
		}
	}
	if maxCost == 0 {
		maxCost = 1.0 // 避免除零
	}

	for i, c := range pool {
		// 归一化延迟 (越低越好)
		normLatency := float64(c.LatencyP95) / float64(maxLatency)

		// 归一化成本 (越低越好)
		normCost := c.CostPer1M / maxCost

		// 计算综合评分
		score := e.weights.HealthScore*c.HealthScore +
			e.weights.LatencyP95*(1-normLatency) +
			e.weights.QuotaRemaining*c.QuotaRemain +
			e.weights.Cost*(1-normCost) +
			e.weights.TaskFit*e.estimateTaskFit(c) +
			e.weights.TierAffinity*e.getTierAffinity(c)

		// 限制在 [0, 1] 范围
		if score > 1.0 {
			score = 1.0
		}
		if score < 0.0 {
			score = 0.0
		}

		scored[i] = ScoredCandidate{
			Candidate: c,
			Score:     score,
		}
	}

	// 按评分降序排序
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	return scored
}

// splitTiers 分层
func (e *Engine) splitTiers(scored []ScoredCandidate) Tiers {
	var tiers Tiers

	for _, sc := range scored {
		if sc.Score >= 0.8 {
			tiers.Top = append(tiers.Top, sc)
		} else if sc.Score >= 0.5 {
			tiers.Mid = append(tiers.Mid, sc)
		} else {
			tiers.Rest = append(tiers.Rest, sc)
		}
	}

	return tiers
}

// roundRobin Tier 内轮换
func (e *Engine) roundRobin(tier []ScoredCandidate) *Candidate {
	// 简单轮换。当前不保存跨进程状态, 避免引入热路径外部依赖；
	// 后续如需更强公平性, 可在 Round 6 引入持久化 weighted round-robin。
	idx := rand.Intn(len(tier))
	return &tier[idx].Candidate
}

// estimateTaskFit 估算任务适配度 (旧 autocombo.Candidate 路径)
//
// round 4 L7: 实现 keyword 启发式 — 让 TaskFit 维度真实影响评分. 后续可
// 替换为基于历史任务表现数据的统计模型; 当前用简单 substring 匹配
// (与 candidateMatchesVariant 同源思路).
func (e *Engine) estimateTaskFit(c Candidate) float64 {
	return taskFitFromKeywords(c.ModelID+" "+c.DisplayName, e.variant)
}

// taskFitFromKeywords 给定 haystack (model id + display name) 与 variant,
// 按变体类型关键词匹配返回 0~1 适配分. 0.85 是通用 fallback; 命中关键词
// 给 1.0; 明显不匹配给 0.6~0.7. 这是 round 4 L7 的简单启发式; 真实生产
// 应该用历史任务表现统计, 但当前阶段足够让 TaskFit 维度不再退化为常量.
func taskFitFromKeywords(haystack string, variant Variant) float64 {
	haystack = strings.ToLower(haystack)
	switch variant {
	case VariantCoding:
		if strings.Contains(haystack, "code") || strings.Contains(haystack, "coder") ||
			strings.Contains(haystack, "starcoder") {
			return 1.0
		}
		return 0.6
	case VariantReasoning:
		if strings.Contains(haystack, "reason") || strings.Contains(haystack, "o1") ||
			strings.Contains(haystack, "o3") || strings.Contains(haystack, "deep") {
			return 1.0
		}
		return 0.6
	case VariantCreative:
		if strings.Contains(haystack, "creative") || strings.Contains(haystack, "story") ||
			strings.Contains(haystack, "writer") {
			return 1.0
		}
		return 0.7
	case VariantFast:
		if strings.Contains(haystack, "fast") || strings.Contains(haystack, "mini") ||
			strings.Contains(haystack, "instant") || strings.Contains(haystack, "haiku") {
			return 1.0
		}
		return 0.7
	}
	return 0.85 // generic / no variant
}

// estimateTaskFitProvider 同样的启发式但作用于 provider.Candidate; sortCandidates
// 使用它. 字段映射: StandardizedName + RawModel (取第一个非空).
func (e *Engine) estimateTaskFitProvider(c provider.Candidate) float64 {
	haystack := c.StandardizedName + " " + c.RawModel
	return taskFitFromKeywords(haystack, e.variant)
}

// getTierAffinity 获取层级亲和度
func (e *Engine) getTierAffinity(c Candidate) float64 {
	// 免费模型亲和度最高
	if c.IsFree {
		return 1.0
	}
	// Keyless 次之
	if c.IsKeyless {
		return 0.9
	}
	return 0.5
}

// ScoredProvider 评分后的 provider.Candidate
type ScoredProvider struct {
	Candidate provider.Candidate
	Score     float64
}

// sortCandidates 在 provider.Candidate 上跑评分排序，返回按分数降序的副本。
//
// 评分使用和旧 Engine 相同的 6 维权重 (HealthScore / LatencyP95 /
// QuotaRemaining / Cost / TaskFit / TierAffinity)，但映射到 provider.Candidate
// 的实际字段：P95 延迟、RecentSuccessRate (作为 HealthScore)、单位价格
// (Cost)；BillingMode 决定 tier affinity；其余权重留给 task fit 等
// 后续扩展维度。
// sortCandidates scores and orders the candidate pool by descending score.
//
// quotaSnap (P3 2026-08-11) optionally carries each credential's proactively-
// fetched quota signal (PercentUsed, ResetAt), keyed by CredentialID. When
// present it activates two previously-dead terms:
//   - QuotaRemaining: (1 - PercentUsed), so a near-full credential outscores
//     a near-exhausted one. This is the free-tier's highest-weighted dimension
//     (default 0.25) but before P3 the formula had NO quota term — the weight
//     multiplied into nothing. nil/missing entry → neutral (quotaRemaining=0.5).
//   - ResetWindowAffinity: rewards credentials whose quota just reset (more
//     runway). Mirrors OmniRoute combo/quotaScoring.ts:304-311. Weight defaults
//     to 0 in legacy presets so this is opt-in.
func (e *Engine) sortCandidates(pool []provider.Candidate, quotaSnap map[int]fetchedQuota) []provider.Candidate {
	if len(pool) <= 1 {
		return append([]provider.Candidate(nil), pool...)
	}

	maxLatency := 0
	for _, c := range pool {
		if c.P95LatencyMs > maxLatency {
			maxLatency = c.P95LatencyMs
		}
	}
	if maxLatency == 0 {
		maxLatency = 1000
	}

	maxCost := 0.0
	for _, c := range pool {
		cost := priceAvg(c)
		if cost > maxCost {
			maxCost = cost
		}
	}
	if maxCost == 0 {
		maxCost = 1.0
	}

	scored := make([]ScoredProvider, len(pool))
	for i, c := range pool {
		normLatency := float64(c.P95LatencyMs) / float64(maxLatency)
		normCost := priceAvg(c) / maxCost
		health := c.SuccessRate
		if c.RecentSuccessRate != nil {
			health = *c.RecentSuccessRate
		}
		if health <= 0 {
			health = c.SuccessRate
		}
		if health > 1 {
			health = 1
		}
		// ── P3 quota terms (2026-08-11) ──────────────────────────────────
		// quotaRemaining: 1 = full, 0 = exhausted. Neutral 0.5 when no signal
		// (quotaSnap nil or credential absent) so the term neither helps nor
		// hurts — preserving legacy behavior for deployments without the
		// proactive quota fetcher.
		quotaRemaining := 0.5
		resetWindow := 0.5
		if fq, ok := quotaSnap[c.CredentialID]; ok {
			if fq.PercentUsed > 0 {
				quotaRemaining = 1 - fq.PercentUsed
				if quotaRemaining < 0 {
					quotaRemaining = 0
				}
			}
			resetWindow = computeResetWindowAffinity(fq.ResetAt, resetWindowHorizon)
		}
		score := e.weights.HealthScore*health +
			e.weights.LatencyP95*(1-normLatency) +
			e.weights.Cost*(1-normCost) +
			e.weights.TaskFit*e.estimateTaskFitProvider(c) +
			e.weights.TierAffinity*providerTierAffinity(c) +
			e.weights.QuotaRemaining*quotaRemaining +
			e.weights.ResetWindowAffinity*resetWindow
		if score > 1 {
			score = 1
		}
		if score < 0 {
			score = 0
		}
		scored[i] = ScoredProvider{Candidate: c, Score: score}
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score == scored[j].Score {
			// Tie-break: stable ordering by ProviderID/CredentialID/RawModel.
			a, b := scored[i].Candidate, scored[j].Candidate
			if a.ProviderID != b.ProviderID {
				return a.ProviderID < b.ProviderID
			}
			if a.CredentialID != b.CredentialID {
				return a.CredentialID < b.CredentialID
			}
			return a.RawModel < b.RawModel
		}
		return scored[i].Score > scored[j].Score
	})

	out := make([]provider.Candidate, len(scored))
	for i, s := range scored {
		out[i] = s.Candidate
	}
	return out
}

// computeResetWindowAffinity maps a quota reset time to a 0..1 affinity score.
// Mirrors OmniRoute combo/quotaScoring.ts:304-311:
//   - already reset (resetAt in the past or nil) → 1.0 (full runway)
//   - resets soon → high score (decays linearly over the horizon)
//   - resets far in the future → low score (little remaining runway)
//   - unknown reset time → neutral (caller passes the fallback, usually 0.5)
//
// The linear decay is a simplification of OmniRoute's clamp01(1 - msUntilReset/horizon);
// we clamp to [0,1] so a reset far beyond the horizon scores 0.
func computeResetWindowAffinity(resetAt *time.Time, horizon time.Duration) float64 {
	if resetAt == nil || resetAt.IsZero() {
		return 0.5 // unknown → neutral
	}
	now := time.Now()
	msUntilReset := resetAt.Sub(now)
	if msUntilReset <= 0 {
		return 1.0 // already reset → full runway
	}
	ratio := float64(msUntilReset) / float64(horizon)
	score := 1 - ratio
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}

// priceAvg 估算单 token 平均价, 兼容 PriceIn/Out 任一为空的情况.
func priceAvg(c provider.Candidate) float64 {
	var in, out float64
	if c.PriceInPer1M != nil {
		in = *c.PriceInPer1M
	}
	if c.PriceOutPer1M != nil {
		out = *c.PriceOutPer1M
	}
	if in == 0 && out == 0 {
		return 0
	}
	return (in + out) / 2
}

// providerTierAffinity 给出与 billing mode 相关的亲和度: free / keyless 最高,
// token_plan / code_plan 次之, 其他 0.5; 与 autocombo.Engine.getTierAffinity
// 保持相同取值, 让旧 spec 评分在新代码下保持单调一致.
func providerTierAffinity(c provider.Candidate) float64 {
	switch strings.ToLower(strings.TrimSpace(c.BillingMode)) {
	case "free", "keyless":
		return 1.0
	case "token_plan", "code_plan":
		return 0.9
	}
	return 0.5
}
