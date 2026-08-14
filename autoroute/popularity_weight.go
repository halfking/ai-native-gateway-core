package autoroute

// popularity_weight.go — RT-3（doc 19 轨道 ROUTE）：热门/精选加权接线。
//
// 数据已存在，本文件只做接线（不改数据结构）：
//   - Candidate.PopularityScore：来自 credential_model_bindings.routing_tier
//     （scanIndexRow 派生，0-100），此前只在 legacy Recommend 的 L1 热门池
//     排序里使用，V2 评分路径完全忽略它。
//   - routing_policy.featured_models：管理员配置的精选模型列表，此前只
//     服务 admin UI（/api/routing/featured-models），不参与路由。
//
// 行为：UsePopularityWeight 开启时，在 composite 之上叠加一个加性排序权重
//   boost = PopularityWeight * (PopularityScore/100)
//         + (featured ? FeaturedBonus : 0)
// 并记录到 Breakdown.PopularityBoost。默认关闭；关闭时 composite、排序与
// 序列化字节与 RT-3 之前完全一致。

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// applyPopularityWeighting adds the RT-3 ordering weight in place. No-op
// (byte-identical output) when the flag is off or the weights are zero.
func applyPopularityWeighting(scored []ScoredCandidate, featured map[string]struct{}, flags *FeatureFlags) {
	if flags == nil || !flags.UsePopularityWeight {
		return
	}
	if flags.PopularityWeight <= 0 && flags.FeaturedBonus <= 0 {
		return
	}
	for i := range scored {
		c := &scored[i]
		boost := flags.PopularityWeight * popularityFraction(c.Candidate.PopularityScore)
		if featured != nil {
			if _, ok := featured[c.Candidate.CanonicalName]; ok {
				boost += flags.FeaturedBonus
			}
		}
		if boost == 0 {
			continue
		}
		c.Breakdown.PopularityBoost = boost
		c.Breakdown.Composite += boost
	}
}

// popularityFraction maps the 0-100 PopularityScore to 0-1, clamping OOB
// values (scanIndexRow already clamps, this guards test/手工构造的候选).
func popularityFraction(score float64) float64 {
	if score <= 0 {
		return 0
	}
	if score > 100 {
		return 1
	}
	return score / 100
}

// loadFeaturedModels reads routing_policy.featured_models (tenant 'default' —
// the only row current admin tooling writes). Called best-effort from
// Index.Refresh; errors keep the previous set.
func loadFeaturedModels(ctx context.Context, pool *pgxpool.Pool) (map[string]struct{}, error) {
	if pool == nil {
		return nil, nil
	}
	var models []string
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(featured_models, ARRAY[]::TEXT[])
		FROM routing_policy WHERE tenant_id = 'default' ORDER BY id LIMIT 1
	`).Scan(&models); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return map[string]struct{}{}, nil
		}
		return nil, err
	}
	set := make(map[string]struct{}, len(models))
	for _, m := range models {
		m = strings.ToLower(strings.TrimSpace(m))
		if m != "" {
			set[m] = struct{}{}
		}
	}
	return set, nil
}
