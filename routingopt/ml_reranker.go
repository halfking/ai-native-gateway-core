package routingopt

// ml_reranker.go — P2.5: 把ML预测接进候选重排序。
//
// 策略（保守优先，任何不匹配都不干预）:
//   1. Predict失败或置信度低于MinConfidence → 保持原序（规则引擎结果）
//   2. 预测label与候选精确匹配（CanonicalName/RawModel，忽略大小写）
//      → 该候选稳定提升到第一位
//   3. 仅前缀兼容（如label "gpt-4" vs 候选 "gpt-4-2024xxxx"）→ 同样提升
//   4. 无候选匹配 → 保持原序
//
// 这样ML只改变候选间的相对顺序，永不删除/新增候选，也不改变分数语义。

import (
	"context"
	"log/slog"
	"strings"
)

// MLReranker reorders ModelCandidate lists using ONNX predictions.
type MLReranker struct {
	selector      *MLSelector
	MinConfidence float64
}

// NewMLReranker wraps a selector with the re-rank policy.
// minConfidence <= 0 means "always apply when matched" (discouraged; the
// default wiring uses settings.RoutingMLFlags.MinConfidence).
func NewMLReranker(selector *MLSelector, minConfidence float64) *MLReranker {
	return &MLReranker{selector: selector, MinConfidence: minConfidence}
}

// Enabled reports whether the reranker can actually run.
func (r *MLReranker) Enabled() bool { return r != nil && r.selector != nil }

// Rerank returns the candidate list reordered by ML prediction.
// The returned slice may alias input memory; callers must not mutate it.
func (r *MLReranker) Rerank(ctx context.Context, cands []ModelCandidate,
	feat MLRouteFeatures) ([]ModelCandidate, *MLPrediction) {
	if !r.Enabled() || len(cands) < 2 {
		return cands, nil
	}
	pred, err := r.selector.Predict(ctx, feat)
	if err != nil {
		slog.WarnContext(ctx, "routingopt: ML predict failed, keeping rule-engine order",
			"err", err)
		return cands, nil
	}
	if pred.MaxProbability < r.MinConfidence {
		return cands, pred
	}
	idx := MatchCandidateIndex(pred.Label, cands)
	if idx <= 0 {
		// -1: no match; 0: top candidate already the prediction — no-op.
		return cands, pred
	}
	out := make([]ModelCandidate, 0, len(cands))
	out = append(out, cands[idx])
	out = append(out, cands[:idx]...)
	out = append(out, cands[idx+1:]...)
	return out, pred
}

// MatchCandidateIndex finds the best candidate matching label (exact
// CanonicalName/RawModel match first, then prefix compatibility).
// Returns -1 when nothing matches.
func MatchCandidateIndex(label string, cands []ModelCandidate) int {
	if label == "" || len(cands) == 0 {
		return -1
	}
	want := strings.ToLower(label)
	// Pass 1: exact match.
	for i, c := range cands {
		if strings.EqualFold(c.CanonicalName, want) ||
			(c.RawModel != "" && strings.EqualFold(c.RawModel, want)) {
			return i
		}
	}
	// Pass 2: prefix compatibility (label is a version-less family name, or
	// the candidate carries a date suffix: "gpt-4" vs "gpt-4-2024-11-20").
	for i, c := range cands {
		cn := strings.ToLower(c.CanonicalName)
		if cn != "" && (strings.HasPrefix(cn, want) || strings.HasPrefix(want, cn)) {
			return i
		}
	}
	return -1
}
