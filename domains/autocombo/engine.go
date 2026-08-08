package autocombo

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/kaixuan/llm-gateway-go/provider"
)

// Engine 评分与选择引擎
type Engine struct {
	weights ScoringWeights
}

// NewEngine 创建引擎
//
// round 3 audit M2 修复: 在 unmarshal 后校验 sum(weights) ∈ [0.99, 1.01],
// 否则返回 error. 旧实现对全 0 weights 静默生成全 0 评分, 候选平局, 行为
// 不可预测. 真实生产中 weights 一般由 resolver 内置模板或 DB 行提供,
// 校验后能给运维更明确的错误信息 ("weight sum 0.85, expected ~1.0").
func NewEngine(weightsJSON json.RawMessage) (*Engine, error) {
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

	return &Engine{weights: weights}, nil
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
	// 简单轮换（实际应使用持久化的 round-robin state）
	// TODO: 实现带权重的轮换或基于评分的概率选择
	idx := rand.Intn(len(tier))
	return &tier[idx].Candidate
}

// estimateTaskFit 估算任务适配度
func (e *Engine) estimateTaskFit(c Candidate) float64 {
	// TODO: 根据模型特性和任务类型计算适配度
	// 目前简单返回 1.0
	// 未来可以基于：
	// - 模型 ID 包含的关键词 (coding, chat, reasoning 等)
	// - 历史任务表现数据
	return 1.0
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
func (e *Engine) sortCandidates(pool []provider.Candidate) []provider.Candidate {
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
		score := e.weights.HealthScore*health +
			e.weights.LatencyP95*(1-normLatency) +
			e.weights.Cost*(1-normCost) +
			e.weights.TierAffinity*providerTierAffinity(c)
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
