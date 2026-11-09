// behavioral.go — 行为指纹核心算法（简化版，供被动分析使用）
//
// 完整的主动探针实现在后续 W1-2 阶段补充。
// 当前只包含被动分析所需的 JSD 计算。
package modelcredibility

import "math"

// ComputeJSD 计算 Jensen-Shannon Divergence（两个概率分布的差异）。
//
// JSD 是基于 Kullback-Leibler divergence 的对称版本，范围 [0, 1] bit：
//   - 0 = 完全相同
//   - 1 = 完全不同
//
// ★ 阈值（用于被动 ITT 分布对比）：
//   - JSD ≤ 0.30: 同模型（match）
//   - 0.30 < JSD ≤ 0.40: 不确定（需更多样本）
//   - JSD > 0.40: 不同模型（mismatch）
//
// 公式: JSD(P||Q) = (KL(P||M) + KL(Q||M)) / 2
//
//	其中 M = (P + Q) / 2
func ComputeJSD(p, q map[string]float64) float64 {
	if len(p) == 0 || len(q) == 0 {
		return 1.0 // 空分布视为完全不同
	}

	// 合并键集合
	allKeys := make(map[string]bool)
	for k := range p {
		allKeys[k] = true
	}
	for k := range q {
		allKeys[k] = true
	}

	// 计算 m = (p + q) / 2
	m := make(map[string]float64)
	for k := range allKeys {
		m[k] = (p[k] + q[k]) / 2.0
	}

	// JSD = (KL(p||m) + KL(q||m)) / 2
	klPM := klDivergence(p, m, allKeys)
	klQM := klDivergence(q, m, allKeys)

	return (klPM + klQM) / 2.0
}

// klDivergence 计算 Kullback-Leibler divergence。
//
// KL(P||Q) = Σ P(x) * log2(P(x) / Q(x))
func klDivergence(p, q map[string]float64, keys map[string]bool) float64 {
	kl := 0.0
	for k := range keys {
		pk := p[k]
		qk := q[k]
		if pk > 0 && qk > 0 {
			kl += pk * math.Log2(pk/qk)
		}
	}
	return kl
}
