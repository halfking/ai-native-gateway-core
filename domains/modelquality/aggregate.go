package modelquality

import (
	"context"
	"fmt"
	"math"
	"sort"
)

// 本文件提供两个聚合视图（纯内存，读 MonitorStorage 已落盘的评分）：
//   1. CatalogModelIQ  —— 模型目录平均智商：同一模型跨所有提供它的凭据节点的平均。
//   2. NodeIQ          —— 单凭据节点智商：按 (provider, credentialID) 聚合其下所有模型。
//
// "智商" = QualityScore.OverallScore（沿用综合分：准确率60%+稳定性30%+延迟10%）。

// CatalogModelSummary 模型目录平均智商摘要（一个 canonical/model 一行）。
type CatalogModelSummary struct {
	CanonicalModel string             `json:"canonical_model"` // 目录模型名（缺省时回落到 ModelName）
	Provider       string             `json:"provider"`        // 代表性供应商（取最新一条）
	DisplayModel   string             `json:"display_model"`   // 用于展示的模型名
	SampleCount    int                `json:"sample_count"`    // 评分样本数
	NodeCount      int                `json:"node_count"`      // 不同凭据节点数（含经网关的）
	AvgIQ          float64            `json:"avg_iq"`          // 平均智商 OverallScore
	StdIQ          float64            `json:"std_iq"`          // 标准差
	MinIQ          float64            `json:"min_iq"`
	MaxIQ          float64            `json:"max_iq"`
	Grade          string             `json:"grade"`         // 按 AvgIQ 映射的等级
	ByProvider     map[string]float64 `json:"by_provider"`   // 各供应商下的平均智商
	ByProbeKind    map[string]float64 `json:"by_probe_kind"` // 2026-08-10: 各调用路径(gateway/direct/mock)下的平均智商
}

// NodeSummary 单凭据节点智商摘要。
type NodeSummary struct {
	Provider     string  `json:"provider"`
	CredentialID int     `json:"credential_id"`
	Label        string  `json:"label,omitempty"` // 凭据标签（若评分里有记录）
	DisplayNode  string  `json:"display_node"`    // provider:credentialID
	SampleCount  int     `json:"sample_count"`
	ModelCount   int     `json:"model_count"` // 该节点上不同模型数
	AvgIQ        float64 `json:"avg_iq"`
	MinIQ        float64 `json:"min_iq"`
	MaxIQ        float64 `json:"max_iq"`
	WeakestModel string  `json:"weakest_model"`
	BestModel    string  `json:"best_model"`
	Grade        string  `json:"grade"`
	ProbeKind    string  `json:"probe_kind,omitempty"` // 2026-08-10: gateway/direct/mock
}

// catalogKey 模型归属键：优先 CanonicalModel，回落 ModelName。
func catalogKey(s *QualityScore) string {
	if s.CanonicalModel != "" {
		return s.CanonicalModel
	}
	return s.ModelName
}

// CatalogModelIQ 计算模型目录平均智商（一个模型一行）。
// limit<=0 表示读全部历史评分；否则只读最近 limit 条。
func CatalogModelIQ(ctx context.Context, storage MonitorStorage, limit int) ([]*CatalogModelSummary, error) {
	scores, err := storage.ListAllScores(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list scores: %w", err)
	}
	return catalogModelIQFromScores(scores), nil
}

// catalogModelIQFromScores 纯函数版（便于测试）。
func catalogModelIQFromScores(scores []*QualityScore) []*CatalogModelSummary {
	calc := &ScoreCalculator{}
	type agg struct {
		keys   []float64
		byProv map[string][]float64
		nodes  map[int]struct{}
		byKind map[string][]float64 // probe_kind -> []IQ
		last   *QualityScore
		model  string
	}
	bucket := make(map[string]*agg)
	order := make([]string, 0)

	for _, s := range scores {
		if s == nil {
			continue
		}
		key := catalogKey(s)
		a, ok := bucket[key]
		if !ok {
			a = &agg{byProv: map[string][]float64{}, nodes: map[int]struct{}{}, byKind: map[string][]float64{}}
			bucket[key] = a
			order = append(order, key)
		}
		a.keys = append(a.keys, s.OverallScore)
		a.byProv[s.Provider] = append(a.byProv[s.Provider], s.OverallScore)
		kind := string(s.ProbeKind)
		if kind == "" {
			kind = "unknown"
		}
		a.byKind[kind] = append(a.byKind[kind], s.OverallScore)
		a.nodes[s.CredentialID] = struct{}{}
		if s.ModelName != "" {
			a.model = s.ModelName
		}
		// 保留最新一条（ListAllScores 已按时间倒序，第一个即最新）
		if a.last == nil {
			a.last = s
		}
	}

	out := make([]*CatalogModelSummary, 0, len(order))
	for _, key := range order {
		a := bucket[key]
		sum := 0.0
		minV, maxV := math.Inf(1), math.Inf(-1)
		for _, v := range a.keys {
			sum += v
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		n := float64(len(a.keys))
		mean := 0.0
		if n > 0 {
			mean = sum / n
		}
		var sumSq float64
		for _, v := range a.keys {
			sumSq += (v - mean) * (v - mean)
		}
		std := 0.0
		if n > 1 {
			std = math.Sqrt(sumSq / n)
		}
		byProvider := make(map[string]float64, len(a.byProv))
		for p, vs := range a.byProv {
			ps := 0.0
			for _, v := range vs {
				ps += v
			}
			byProvider[p] = ps / float64(len(vs))
		}
		byProbeKind := make(map[string]float64, len(a.byKind))
		for k, vs := range a.byKind {
			ks := 0.0
			for _, v := range vs {
				ks += v
			}
			byProbeKind[k] = ks / float64(len(vs))
		}
		display := a.model
		if display == "" {
			display = key
		}
		out = append(out, &CatalogModelSummary{
			CanonicalModel: key,
			Provider:       a.last.Provider,
			DisplayModel:   display,
			SampleCount:    len(a.keys),
			NodeCount:      len(a.nodes),
			AvgIQ:          mean,
			StdIQ:          std,
			MinIQ:          minV,
			MaxIQ:          maxV,
			Grade:          calc.scoreToGrade(mean),
			ByProvider:     byProvider,
			ByProbeKind:    byProbeKind,
		})
	}
	// 按平均智商降序
	sort.Slice(out, func(i, j int) bool { return out[i].AvgIQ > out[j].AvgIQ })
	return out
}

// NodeIQ 计算单凭据节点智商（按 provider+credentialID 聚合）。
// 注意：CredentialID==0（经网关聚合）的评分不参与节点聚合——它们没有节点维度。
func NodeIQ(ctx context.Context, storage MonitorStorage, limit int) ([]*NodeSummary, error) {
	scores, err := storage.ListAllScores(ctx, limit)
	if err != nil {
		return nil, fmt.Errorf("list scores: %w", err)
	}
	return nodeIQFromScores(scores), nil
}

// nodeIQFromScores 纯函数版（便于测试）。
func nodeIQFromScores(scores []*QualityScore) []*NodeSummary {
	calc := &ScoreCalculator{}
	type agg struct {
		keys         []float64
		models       map[string]float64 // model -> 该模型最新智商
		provider     string
		credentialID int
		label        string
		probeKind    string
	}
	bucket := make(map[string]*agg) // key: provider:credentialID
	order := make([]string, 0)

	putModel := func(m map[string]float64, name string, v float64) {
		// ListAllScores 按时间倒序，第一条即最新，已存在则不覆盖
		if _, ok := m[name]; !ok {
			m[name] = v
		}
	}

	for _, s := range scores {
		if s == nil || s.CredentialID == 0 {
			continue // 经网关聚合的评分无节点维度，跳过
		}
		key := fmt.Sprintf("%s:%d", s.Provider, s.CredentialID)
		a, ok := bucket[key]
		if !ok {
			a = &agg{models: map[string]float64{}, provider: s.Provider, credentialID: s.CredentialID}
			bucket[key] = a
			order = append(order, key)
		}
		a.keys = append(a.keys, s.OverallScore)
		if s.ModelName != "" {
			putModel(a.models, s.ModelName, s.OverallScore)
		}
		if a.label == "" {
			a.label = s.Provider
		}
		// 2026-08-10: 记录该节点的调用路径（节点测试目前都是 direct，但保留字段）
		if a.probeKind == "" && s.ProbeKind != "" {
			a.probeKind = string(s.ProbeKind)
		}
	}

	out := make([]*NodeSummary, 0, len(order))
	for _, key := range order {
		a := bucket[key]
		sum := 0.0
		minV, maxV := math.Inf(1), math.Inf(-1)
		for _, v := range a.keys {
			sum += v
			if v < minV {
				minV = v
			}
			if v > maxV {
				maxV = v
			}
		}
		mean := 0.0
		if len(a.keys) > 0 {
			mean = sum / float64(len(a.keys))
		}
		// 最弱/最强模型
		weakest, best := "", ""
		weakestV, bestV := math.Inf(1), math.Inf(-1)
		for m, v := range a.models {
			if v < weakestV {
				weakestV, weakest = v, m
			}
			if v > bestV {
				bestV, best = v, m
			}
		}
		out = append(out, &NodeSummary{
			Provider:     a.provider,
			CredentialID: a.credentialID,
			Label:        a.label,
			DisplayNode:  key,
			SampleCount:  len(a.keys),
			ModelCount:   len(a.models),
			AvgIQ:        mean,
			MinIQ:        minV,
			MaxIQ:        maxV,
			WeakestModel: weakest,
			BestModel:    best,
			Grade:        calc.scoreToGrade(mean),
			ProbeKind:    a.probeKind,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AvgIQ > out[j].AvgIQ })
	return out
}
