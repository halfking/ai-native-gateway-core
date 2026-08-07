package autocombo

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
)

// Engine 评分与选择引擎
type Engine struct {
	weights ScoringWeights
}

// NewEngine 创建引擎
func NewEngine(weightsJSON json.RawMessage) (*Engine, error) {
	var weights ScoringWeights
	if err := json.Unmarshal(weightsJSON, &weights); err != nil {
		return nil, fmt.Errorf("unmarshal scoring weights: %w", err)
	}

	return &Engine{weights: weights}, nil
}

// SelectCandidate 从候选池选择一个候选
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

// scoreAll 计算所有候选的评分
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
