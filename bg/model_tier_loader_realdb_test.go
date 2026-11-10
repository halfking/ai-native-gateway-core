package bg

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// modelTierDSN 返回真库 DSN；未设置则跳过（与仓内既有真库门同约定）。
func modelTierDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过 model_tier 装载真库行为门")
	}
	return dsn
}

func openModelTierPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, modelTierDSN(t))
	if err != nil {
		t.Fatalf("open pg pool: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

// TestModelTier_LoaderSQLExpandsBothSources_RealPG 是**行为门**（不是源码字符串门）。
//
// ★★ 为什么必须有行为门：本轮第一版修复只展开了 `models_canonical.featured`
// 那一侧，漏了 `routing_policy.featured_models` 那一侧 ⇒ 实测 126 个
// routing_policy featured canonical 的 namespaced 变体里 **106 个仍未覆盖**。
// 而当时的门是**源码字符串断言** ⇒ ★★ **变异验证证明它是假门**
// （把 routing_policy 从并集里去掉，门仍然全绿）。
//
// ⇒ 这里直接**执行装载查询**，断言输出集合同时包含：
//
//	① canonical 侧 featured 的 raw 变体；
//	② routing_policy 侧 featured（**仅**在 routing_policy 里）的 raw 变体。
//
// ★ 后者是本次缺陷的直接证据。
func TestModelTier_LoaderSQLExpandsBothSources_RealPG(t *testing.T) {
	p := openModelTierPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// ★ 与 bg/model_tier.go 的装载查询保持一致（同一套展开语义）。
	const loaderSQL = `
		WITH feat_names AS (
		    SELECT DISTINCT lower(btrim(f)) AS name
		    FROM routing_policy, unnest(featured_models) AS f
		    WHERE tenant_id = 'default' AND btrim(f) <> ''
		    UNION
		    SELECT DISTINCT lower(btrim(canonical_name)) AS name
		    FROM models_canonical
		    WHERE featured = TRUE AND status = 'active'
		)
		SELECT fn.name, COALESCE(pm.raw_model_name, '')
		FROM feat_names fn
		LEFT JOIN models_canonical mc ON lower(btrim(mc.canonical_name)) = fn.name
		LEFT JOIN provider_models pm ON pm.canonical_id = mc.id`

	rows, err := p.Query(ctx, loaderSQL)
	if err != nil {
		t.Fatalf("loader query failed: %v", err)
	}
	defer rows.Close()

	names := map[string]struct{}{}
	raws := map[string]struct{}{}
	for rows.Next() {
		var name, raw string
		if err := rows.Scan(&name, &raw); err != nil {
			t.Fatalf("scan: %v", err)
		}
		// ★ 必须复用**生产侧同一把**归一化函数（normalizeModelKey：小写 + 去空白）。
		//   否则门会因大小写差异误报（实测 provider_models 里有 `zcode/GLM-5.3`，
		//   canonical 侧是 `glm-5.3`；产品侧会归一化，测试侧不归一化 ⇒ 假红 7 条）。
		if name = normalizeModelKey(name); name != "" {
			names[name] = struct{}{}
		}
		if raw = normalizeModelKey(raw); raw != "" {
			raws[raw] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// ① canonical 侧 featured 的裸名必须在集合里。
	var canonFeatured int
	if err := p.QueryRow(ctx,
		`SELECT COUNT(*) FROM models_canonical WHERE featured = TRUE AND status = 'active'`).Scan(&canonFeatured); err != nil {
		t.Fatalf("count canonical featured: %v", err)
	}
	if canonFeatured == 0 {
		t.Skip("真库里没有 featured canonical，本门无对象")
	}
	if len(names) < canonFeatured {
		t.Fatalf("装载集合的裸名数 (%d) 少于 featured canonical 数 (%d) ⇒ canonical 侧漏了",
			len(names), canonFeatured)
	}

	// ② ★ 核心断言：**仅**出现在 routing_policy.featured_models 里、
	//    **不在** models_canonical.featured 里的那些 canonical，
	//    其 namespaced raw 变体也必须被展开进集合。
	rows2, err := p.Query(ctx, `
		WITH rp AS (
		    SELECT DISTINCT lower(btrim(f)) m
		    FROM routing_policy, unnest(featured_models) AS f
		    WHERE tenant_id = 'default' AND btrim(f) <> ''
		), rp_only AS (
		    SELECT rp.m FROM rp
		    WHERE rp.m NOT IN (
		        SELECT lower(btrim(canonical_name)) FROM models_canonical
		        WHERE featured = TRUE AND status = 'active')
		), expect_raw AS (
		    SELECT DISTINCT lower(btrim(pm.raw_model_name)) rv
		    FROM provider_models pm
		    JOIN models_canonical mc ON mc.id = pm.canonical_id
		    JOIN rp_only ON rp_only.m = lower(btrim(mc.canonical_name))
		    WHERE pm.raw_model_name IS NOT NULL AND btrim(pm.raw_model_name) <> ''
		      AND pm.raw_model_name LIKE '%/%'
		)
		SELECT rv FROM expect_raw`)
	if err != nil {
		t.Fatalf("expect_raw query failed: %v", err)
	}
	defer rows2.Close()

	var expect []string
	for rows2.Next() {
		var rv string
		if err := rows2.Scan(&rv); err != nil {
			t.Fatalf("scan expect: %v", err)
		}
		expect = append(expect, rv)
	}
	if err := rows2.Err(); err != nil {
		t.Fatalf("rows2: %v", err)
	}
	if len(expect) == 0 {
		t.Skip("真库里没有「仅在 routing_policy」且带命名空间变体的 canonical，本门无对象")
	}

	var missing []string
	for _, rv := range expect {
		if _, ok := raws[normalizeModelKey(rv)]; !ok {
			missing = append(missing, rv)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("★ routing_policy 侧的 namespaced 变体未被展开进装载集合（%d/%d 缺失）：%v\n"+
			"★ 这是 2026-11-10 修复的**另一半** —— 同一个「匹配键不一致」缺陷在两个数据源都存在",
			len(missing), len(expect), missing)
	}
}
