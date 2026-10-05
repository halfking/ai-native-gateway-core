package bg

// 第 13 条健康检查 supplier_price_missing_from_cost 的判据。
//
// # 这条检查在补什么洞
//
// `domains/streaming.CalcCost`（usage.go:208，经 AssignRequestCost 调用）有守卫
// `if priceIn == 0 && priceOut == 0 { return nil }`。⇒ **零价绑定的成本是
// 「算不出来」而不是 0**，与「真的免费」在账上分不开。
// ⚠️ 勿归给 `provider.Candidate.CalcCost`：那是死代码且 return 0，语义不同。
// 真库（127.0.0.1:5432，只读）实测：
// 可路由 + per_token 的绑定里**有价的是 0 条**，而零价的大约 150 绑定 / 129 模型
// ⇒ 当前能被路由的按 token 计费流量，**没有一条算得出成本**。
//
// # 判据的五个形态，各自钉的是哪一句话
//
//	A 无价 + per_token + 可路由     ⇒ 必须报（这条检查存在的理由）
//	B 有价 + per_token + 可路由     ⇒ 必须不报（「有价」不能被扫进来）
//	C 无价 + free       + 可路由    ⇒ 必须不报（★ 全部价值所在：已知的 0 是对的）
//	D 无价 + per_token + 不可路由   ⇒ 必须不报（★ 总体必须是 is_routable）
//	E 无价 + per_token + 可路由
//	  但 pricing_plans 有生效兜底价  ⇒ 必须不报（★ 兜底 EXISTS 是承重的）
//
// B/C/D/E 都不是「顺手加的」：每一件都对应本文件头注释里一条具体的主张。
// C 去掉，这条检查就退化成噪声源；D 去掉，总体就变成「所有绑定」，那正是第一版
// 手抄谓词量出 858 条的错误；E 去掉，某天有人填了 pricing_plans，这条检查就会对着
// **有价**的绑定喊没价 —— 那是比不报更坏的假告警。

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// missingCostFixture 搭出「真视图 + 最小表集」，并把五个形态一次种齐。
//
// ★ 用**真**视图定义（sql/objects/views/v_routable_credential_models.sql）而不是
// 手抄那份合取：这条检查的价值恰恰是「总体取自路由 SSOT」，手抄就等于把要证明的
// 东西写进了夹具。表只补视图与查询真正引用的列。
//
// ★ 清理用 t.Cleanup 且**先注册池再注册 DROP**，靠 LIFO 让 DROP 跑在关池之前。
func missingCostFixture(t *testing.T, ctx context.Context) (*pgxpool.Pool, map[string]int64) {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close) // 先注册 ⇒ 后执行

	tables := []string{"routing_health_checks", "pricing_plans", "model_aliases",
		"node_probe_state", "credential_model_bindings", "provider_models",
		"credentials", "providers", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
			DROP VIEW IF EXISTS public.v_routable_credential_models;
			DROP TABLE IF EXISTS public.routing_health_checks;
			DROP TABLE IF EXISTS public.pricing_plans;
			DROP TABLE IF EXISTS public.model_aliases;
			DROP TABLE IF EXISTS public.node_probe_state;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;`)
	})

	for _, stmt := range []struct{ name, sql string }{
		{"routing_health_checks", `CREATE TABLE public.routing_health_checks (
			check_id text, severity text, entity_type text, entity_id bigint,
			entity_name text, detail text, fix_sql text, status text,
			created_at timestamptz, updated_at timestamptz,
			UNIQUE (check_id, entity_type, entity_id))`},
		{"providers", `CREATE TABLE public.providers (
			id bigserial PRIMARY KEY, code text NOT NULL, display_name text NOT NULL,
			base_url text NOT NULL, protocol text NOT NULL,
			enabled boolean NOT NULL DEFAULT true,
			manual_disabled boolean NOT NULL DEFAULT false)`},
		{"credentials", `CREATE TABLE public.credentials (
			id bigserial PRIMARY KEY, provider_id bigint NOT NULL, label text NOT NULL,
			tenant_id text, fp_slot_limit integer NOT NULL DEFAULT 4,
			status text NOT NULL DEFAULT 'active',
			lifecycle_status text NOT NULL DEFAULT 'active',
			manual_disabled boolean NOT NULL DEFAULT false,
			availability_state text NOT NULL DEFAULT 'ready',
			quota_state text NOT NULL DEFAULT 'ok',
			health_status text, health_checked_at timestamptz,
			plan_type text)`},
		{"provider_models", `CREATE TABLE public.provider_models (
			id bigserial PRIMARY KEY, provider_id bigint NOT NULL,
			raw_model_name text NOT NULL, canonical_raw_name text NOT NULL,
			canonical_id bigint, available boolean NOT NULL DEFAULT true,
			canonical_cleared_at timestamptz)`},
		// runChecks 末尾的 autoFixCanonicalID 会 UPDATE provider_models 并读
		// models_canonical —— 它不在本检查的路径上，但**每一轮 runChecks 都会跑**，
		// 缺这张表会让本检查的判据以 42P01 失败（夹具缺陷，不是产品缺陷）。
		{"models_canonical", `CREATE TABLE public.models_canonical (
			id bigserial PRIMARY KEY, canonical_name text)`},
		{"credential_model_bindings", `CREATE TABLE public.credential_model_bindings (
			id bigserial PRIMARY KEY, credential_id bigint NOT NULL, provider_model_id bigint NOT NULL,
			billing_mode text, plan_type_origin text,
			unit_price_in_per_1m numeric, unit_price_out_per_1m numeric,
			available boolean NOT NULL DEFAULT true, unavailable_reason text)`},
		{"node_probe_state", `CREATE TABLE public.node_probe_state (
			credential_id bigint NOT NULL, raw_model_name text NOT NULL,
			last_direct_ok boolean, next_retry_at timestamptz)`},
		{"model_aliases", `CREATE TABLE public.model_aliases (
			raw_name text, canonical_id bigint, status text)`},
		{"pricing_plans", `CREATE TABLE public.pricing_plans (
			model_canonical_id bigint, credential_id bigint, provider_id bigint,
			scope text, effective_from timestamptz, effective_to timestamptz,
			plan_json jsonb)`},
	} {
		if _, err := pool.Exec(ctx, stmt.sql); err != nil {
			t.Fatalf("create %s: %v", stmt.name, err)
		}
	}

	// 真视图：可路由合取（含 availability_state / quota_state / health_status /
	// 探针退避 / pm.available / cmb.available / unavailable_reason）都在里面。
	viewSQL, err := os.ReadFile("../sql/objects/views/v_routable_credential_models.sql")
	if err != nil {
		t.Fatalf("read view SSOT: %v", err)
	}
	if _, err := pool.Exec(ctx, string(viewSQL)); err != nil {
		t.Fatalf("apply v_routable_credential_models: %v", err)
	}

	ids := map[string]int64{}
	var provID int64
	if err := pool.QueryRow(ctx, `INSERT INTO public.providers (code, display_name, base_url, protocol)
		VALUES ('fx','Fixture','https://example.invalid','openai') RETURNING id`).Scan(&provID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	// newCred 建一个凭据；lifecycle 用来造形态 D（不可路由）。
	newCred := func(label, lifecycle string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO public.credentials
			(provider_id, label, lifecycle_status) VALUES ($1,$2,$3) RETURNING id`,
			provID, label, lifecycle).Scan(&id); err != nil {
			t.Fatalf("insert credential %s: %v", label, err)
		}
		return id
	}
	// newBinding 造一个模型 + 绑定。priceIn/priceOut 允许 NULL（真库里 1859/2045 行
	// 是 NULL 而不是 0 —— 判据必须同时吃住这两种形态）。
	newBinding := func(credID int64, model, mode string, priceIn, priceOut any, canonID int64) int64 {
		t.Helper()
		var modelID, bindID int64
		if err := pool.QueryRow(ctx, `INSERT INTO public.provider_models
			(provider_id, raw_model_name, canonical_raw_name, canonical_id)
			VALUES ($1,$2,$2,$3) RETURNING id`,
			provID, model, canonID).Scan(&modelID); err != nil {
			t.Fatalf("insert provider_model %s: %v", model, err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, billing_mode, unit_price_in_per_1m, unit_price_out_per_1m)
			VALUES ($1,$2,$3,$4,$5) RETURNING id`,
			credID, modelID, mode, priceIn, priceOut).Scan(&bindID); err != nil {
			t.Fatalf("insert binding %s: %v", model, err)
		}
		return bindID
	}

	live := newCred("live", "active")
	ids["A_zero_per_token_routable"] = newBinding(live, "model-a-zero", "per_token", nil, nil, 1001)
	ids["B_priced_per_token"] = newBinding(live, "model-b-priced", "per_token", "3.00", "15.00", 1002)
	ids["C_free_zero"] = newBinding(live, "model-c-free", "free", nil, nil, 1003)

	// 形态 E：per_token + 无价 + 可路由，但 pricing_plans 有一条当前生效的兜底价。
	// plan_in 命中即可（CalcCost 只要 priceIn/priceOut 任一非 0 就不会返回 nil）。
	ids["E_plan_fallback"] = newBinding(live, "model-e-planfallback", "per_token", nil, nil, 1005)
	if _, err := pool.Exec(ctx, `INSERT INTO public.pricing_plans
		(model_canonical_id, credential_id, scope, effective_from, effective_to, plan_json)
		VALUES (1005, NULL, 'global', now(), NULL, '{"input_per_1m":"1.50","output_per_1m":"6.00"}')`); err != nil {
		t.Fatalf("insert pricing_plans fallback: %v", err)
	}

	// 形态 D：per_token + 无价，但凭据已下线 ⇒ 视图判不可路由，不该报。
	disabled := newCred("disabled", "disabled")
	ids["D_per_token_not_routable"] = newBinding(disabled, "model-d-disabled", "per_token", nil, nil, 1004)

	return pool, ids
}

func missingCostQuery(t *testing.T) string {
	t.Helper()
	for _, c := range AllHealthChecks() {
		if c.CheckID == "supplier_price_missing_from_cost" {
			return c.Query
		}
	}
	t.Fatal("check supplier_price_missing_from_cost not found in AllHealthChecks()")
	return ""
}

// reportedModels 跑这条检查的查询，返回 (首列, entity_name) 的行集。
func reportedRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, missingCostQuery(t))
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id int64
		var name, detail, fix string
		if err := rows.Scan(&id, &name, &detail, &fix); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// seedBulkUnpriced 追加 n 个「无价 + per_token + 可路由」的无名模型（用来跨过 20 的
// 汇总阈值）。from 让两次调用不撞名。
func seedBulkUnpriced(t *testing.T, ctx context.Context, pool *pgxpool.Pool, from, n int) {
	t.Helper()
	for i := from; i < from+n; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_raw_name, canonical_id)
			SELECT p.id, 'bulk-'||$1::text, 'bulk-'||$1::text, 9000+$1::int
			  FROM public.providers p LIMIT 1`, strconv.Itoa(i)); err != nil {
			t.Fatalf("seed bulk model %d: %v", i, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.credential_model_bindings
				(credential_id, provider_model_id, billing_mode)
			SELECT c.id, pm.id, 'per_token'
			  FROM public.credentials c, public.provider_models pm
			 WHERE pm.raw_model_name = 'bulk-'||$1::text LIMIT 1`, strconv.Itoa(i)); err != nil {
			t.Fatalf("seed bulk binding %d: %v", i, err)
		}
	}
}

// TestSupplierPriceMissingFromCost_OnlyUnpricedRoutablePerToken 是本检查的主判据：
// 五个形态一次种齐，只允许 A 出现在结果里。
func TestSupplierPriceMissingFromCost_OnlyUnpricedRoutablePerToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := missingCostFixture(t, ctx)

	got := reportedRows(t, ctx, pool)
	t.Logf("报出的行：%v", got)

	if len(got) != 1 || !strings.Contains(got[0], "model-a-zero") {
		t.Fatalf("应当只报形态 A（无价 + per_token + 可路由），实得 %v", got)
	}
	// B/C/D/E 任一出现都是假告警，逐个点名，避免「恰好没包含」的糊过去。
	for _, forbidden := range []string{"model-b-priced", "model-c-free",
		"model-d-disabled", "model-e-planfallback"} {
		for _, row := range got {
			if strings.Contains(row, forbidden) {
				t.Errorf("假告警：%s 不该被报出（形态说明见文件头注释）", forbidden)
			}
		}
	}
}

// TestSupplierPriceMissingFromCost_BulkShape 钉 >20 时只报一行汇总。
//
// 为什么要有：真环境约 129 个模型，逐条列名会把告警位占满，而它们说的是同一件事
// （与 baseline_price_missing 的同一取舍）。
func TestSupplierPriceMissingFromCost_BulkShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := missingCostFixture(t, ctx)

	// 再加 20 个无价可路由模型 ⇒ 连同形态 A 共 21 个 ⇒ 跨过 20 的阈值。
	seedBulkUnpriced(t, ctx, pool, 0, 20)

	got := reportedRows(t, ctx, pool)
	if len(got) != 1 {
		t.Fatalf("21 个无价模型应当只报一行汇总，实得 %d 行：%v", len(got), got)
	}
	if !strings.Contains(got[0], "(bulk)") || !strings.Contains(got[0], "21") {
		t.Fatalf("汇总行应当自报形态与计数，实得 %q", got[0])
	}
}

// TestSupplierPriceMissingFromCost_BulkDetailMustNotTellYouToTypeANumber
//
// ★ 纯文本 ratchet，钉的是**建议**而不是行为。
//
// 「routable + per_token + 没价」有两种同样成立的解释：(a) 价没填，
// (b) 供应商就是免费送这个模型（仍按 token 计量）。实测 2026-10-05：
// nvidia 名下 208 条绑定 / 44 个开放模型（llama-3.1-8b-instruct、gemma-3-12b-it、
// gpt-oss-20b、nemotron-*）被价源明确报成 0 价，而它们**当前全部不可路由**，
// 所以今天不触发 —— 但只要有一条恢复可路由，告警就会对上它们。
//
// 这条检查手里没有供应商价目，**分不开这两种**。所以它的 detail 不能说
// 「去把价格列填上」—— 对免费模型那是**错误建议**，而且会诱导运维编一个假价进去。
// 必须先分类（propose-supplier-prices 的 published_price_is_zero 族 = 供应商说免费）。
func TestSupplierPriceMissingFromCost_BulkDetailMustNotTellYouToTypeANumber(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := missingCostFixture(t, ctx)
	seedBulkUnpriced(t, ctx, pool, 0, 20) // 连同形态 A 共 21 ⇒ 走汇总分支

	rows, err := pool.Query(ctx, missingCostQuery(t))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Fatal("应当至少返回一行")
	}
	var id int64
	var name, detail, fix string
	if err := rows.Scan(&id, &name, &detail, &fix); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if id != -1 {
		t.Fatalf("本用例要的是汇总行（首列 -1），实得 %d", id)
	}

	// 必须承认「per_token 且 0 价」可能是真的免费。
	for _, must := range []string{"genuinely right", "free", "classify"} {
		if !strings.Contains(detail, must) {
			t.Errorf("汇总行 detail 必须提到 %q（否则它会诱导运维对免费模型编价），实得：%s", must, detail)
		}
	}
	// 且不能再出现「去把价格列填上」这种一刀切的话。
	for _, banned := range []string{"Fill the two price columns"} {
		if strings.Contains(detail, banned) {
			t.Errorf("汇总行 detail 仍含 %q —— 对供应商明确标 0 价的免费模型，那是错误建议。detail：%s", banned, detail)
		}
	}
}

// TestSupplierPriceMissingFromCost_ScanBranchKeepsSummaryRowStable 钉扫描分支的两件事：
//   - 每条命中都落到健康表（entity_id 不再是「扫描分支缺失」的特征值 0）；
//   - 汇总行用**常量键**，所以读数变化不会在健康表里堆出一串历史行。
//
// 后者是 readiness_floor 已经踩过一次的那个坑（entity_name 是含计数的句子，
// 哈希后每轮换一行）。
func TestSupplierPriceMissingFromCost_ScanBranchKeepsSummaryRowStable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, _ := missingCostFixture(t, ctx)

	check := HealthCheckDef{CheckID: "supplier_price_missing_from_cost",
		Severity: "warning", Optional: true, Query: missingCostQuery(t)}
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{check}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}

	var entityID int64
	var name string
	if err := pool.QueryRow(ctx, `SELECT entity_id, entity_name FROM public.routing_health_checks
		WHERE check_id='supplier_price_missing_from_cost'`).Scan(&entityID, &name); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if entityID == 0 {
		t.Fatalf("entity_id=0 是「扫描分支缺失」的特征值（见 health_check_scan_guard_test.go）")
	}
	t.Logf("单模型形态落库：entity_id=%d entity_name=%q", entityID, name)

	// 阶段 2：把集合推过阈值 → 变成汇总行 → 再跑一轮。
	seedBulkUnpriced(t, ctx, pool, 0, 20)
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{check}); err != nil {
		t.Fatalf("runChecks (bulk): %v", err)
	}

	// 阶段 3：**读数再变一次**（21 → 26），汇总行仍然是汇总行。
	//
	// ★ 这一步是 teeth T5 咬得住的唯一原因。第一版只跑到阶段 2 就断言「2 行」，
	// 而变异（把常量键换成 textHash(name)）同样得到 2 行 —— 因为形态 A 那一行
	// 会**一直留着**，新增的汇总行正好补上第二行，两种写法行数相同。
	// 「常量键 vs 名字键」只有在**两次读数不同的汇总行**之间才可观测：
	// 名字里带计数 ⇒ 名字变 ⇒ 哈希变 ⇒ 名字键会**再堆一行**，常量键则原地刷新。
	// ⇒ 这是 teeth 没红的第三种原因：夹具里没有能让该条件发挥作用的样本。
	seedBulkUnpriced(t, ctx, pool, 20, 5)
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{check}); err != nil {
		t.Fatalf("runChecks (bulk, second reading): %v", err)
	}

	// 应始终是 2 行：形态 A 那一行 + 一行汇总。名字键会变成 3 行
	//（残留的单模型行 + 21 的汇总 + 26 的汇总）。
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='supplier_price_missing_from_cost'`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 2 {
		var dump []string
		r, err := pool.Query(ctx, `SELECT entity_id, entity_name FROM public.routing_health_checks
			WHERE check_id='supplier_price_missing_from_cost' ORDER BY entity_name`)
		if err != nil {
			t.Fatalf("dump: %v", err)
		}
		defer r.Close()
		for r.Next() {
			var id int64
			var nm string
			_ = r.Scan(&id, &nm)
			dump = append(dump, nm)
		}
		t.Fatalf("健康表里应有 2 行（单模型 + 汇总），两次读数后仍是 2 行；实得 %d 行：%v", rows, dump)
	}
}
