package bg

// ★ 2026-11-11（节点状态链断链⑦）：featured 装载口径统一。
//
// 背景：2026-11-10 恢复检测及时性轮已把 model_tier 的 featured 装载改成
// 「双源（routing_policy.featured_models ∪ models_canonical.featured）+
// raw 变体展开」（static 45 → 210，三门同时生效）；但同仓还有两处仍只读
// routing_policy 单源裸名：
//   · credential_selfcheck（深度自检的 primary 选择，selfcheckFeatured）
//   · model_probe（watchdog 两条 SQL 的 static CTE：非 featured 退避延长 +
//     healthy 老化降级）
// ⇒ 「featured」在探针分层、自检覆盖面、watchdog 退避三处各有口径。
//
// 本文件是唯一的静态 featured 口径出处：两源裸 canonical 名 + 每个
// canonical 的全部 provider_models.raw_model_name 变体（命名空间形态）。
// $1 恒为 tenant（与 probe.featured_tenant 同参）。

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// featuredNamesCTE 是双源 featured 裸 canonical 名集合（复用 2026-11-10
// model_tier.refresh 的同一查询体，文本级一致 ⇒ static 集合逐字节同源）。
const featuredNamesCTE = `
		    SELECT DISTINCT lower(btrim(f)) AS name
		    FROM routing_policy, unnest(featured_models) AS f
		    WHERE tenant_id = $1 AND btrim(f) <> ''
		    UNION
		    SELECT DISTINCT lower(btrim(canonical_name)) AS name
		    FROM models_canonical
		    WHERE featured = TRUE AND status = 'active'
`

// FeaturedNamesCTESQL renders the `feat_names(name)` CTE body (without the
// leading WITH/comma) for callers that need the bare canonical names plus
// their raw variants in a two-column shape (model_tier.refresh).
func FeaturedNamesCTESQL() string {
	return featuredNamesCTE
}

// StaticFeaturedCTESQL renders a `static(model)` CTE: the dual-source featured
// set expanded to every raw variant. Drop-in replacement for the legacy
// single-source `SELECT lower(unnest(...))` CTE used by the model_probe
// watchdog statements and the selfcheck featured list — column name `model`
// and $1=tenant are preserved.
//
// ★ 语义变化（有意）：此前只比 routing_policy 裸名 ⇒ 命名空间形态的
// raw（z-ai/glm-5.3-flash 等）在 watchdog 里被当成「非 featured」吃退避
// 延长/老化降级，与探针分层的 210 集合口径分叉。统一后这些变体与
// model_tier 同待遇。
func StaticFeaturedCTESQL() string {
	return `static AS (
		    WITH feat_names AS (` + featuredNamesCTE + `)
		    SELECT name AS model FROM feat_names
		    UNION
		    SELECT lower(pm.raw_model_name)
		    FROM feat_names fn
		    JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.name
		    JOIN provider_models pm ON pm.canonical_id = mc.id
		    WHERE btrim(COALESCE(pm.raw_model_name, '')) <> ''
		)`
}

// featuredQueryDB is the narrow query surface QueryStaticFeaturedModels needs;
// *pgxpool.Pool and the selfcheck worker's test double both satisfy it.
type featuredQueryDB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// QueryStaticFeaturedModels returns the unified static featured set (bare
// canonical names + raw variants, lowercased) for a tenant. Callers that only
// need a Go-side list (selfcheck primary selection) use this instead of
// re-implementing a single-source query. Query errors fail EMPTY (callers
// treat an empty featured set as "no featured primary preference", the same
// fail-open posture the old single-source ErrNoRows path had).
func QueryStaticFeaturedModels(ctx context.Context, db featuredQueryDB, tenant string) ([]string, error) {
	if db == nil {
		return nil, nil
	}
	rows, err := db.Query(ctx, `WITH `+StaticFeaturedCTESQL()+` SELECT model FROM static`, tenant)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out, rows.Err()
}
