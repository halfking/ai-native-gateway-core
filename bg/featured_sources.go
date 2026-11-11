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
// re-implementing a single-source query.
//
// ★ 2026-11-11 审计 B-6（★ 注释与代码直接矛盾）：★ 本注释此前写的是
// 「Query errors fail EMPTY（★ 同样的 fail-open 姿态）」，
// ★ ★ 而代码是 `return nil, err` ⇒ ★★ 注释是**假**的。
// ⇒ ★★ 现在按**真实行为**写，并明确区分两种「空」：
//
//	① db == nil        ⇒ 返回 (nil, nil) —— ★ **真的**「空」，★ 无 DB 可问。
//	② 查询/扫描出错   ⇒ 返回 (nil, err) —— ★ **不是**空，★ 是**失败**。
//
// ★ 为什么 ② 必须**向上抛**（★ fail-closed）而不是退化成「无 featured 偏好」：
// ★★ featured 集合决定**自检主模型**；★ 若读不到它却当作「没有 featured」
// ★ ★⇒ ★★ 会退化成「按其它信号随便挑一个主模型去探」★★ ⇒ ★★ 那是在
// ★★ **没有依据**的情况下发起上游调用（★ 租户可能压根没有 featured 模型）。
// ★★ 与之相对，同一文件里 `recentInUsageWindow` 的失败是 **fail-open**
// ★★（★ 它只是**收窄**一个优化用的榜单，★ 读不到就保留原榜单）。
// ★★ 两者姿态不同是因为**语义不同**（★ 一个决定**探谁**、★ 一个只决定**排序**）。
//
// ★ 调用方（★ bg/credential_selfcheck.go 的 selfcheckFeatured）★ 遇错
// ★ **放弃整轮自检**（★ `pickModelsResult{}, err`）★ ⇒ ★★ 上面这条
// ★★ fail-closed 语义在调用链上是**兑现**的（★ 而不是被悄悄吞掉）。
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
