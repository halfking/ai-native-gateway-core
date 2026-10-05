package bg

// 「Optional 跳过」这件事的**组合**判据：多条检查一次跑、其中一部分依赖的对象
// 在这个库上**确实不存在**。
//
// # 为什么单条判据不够
//
// 本包此前所有真库判据都是一次只跑一条：
//
//	runChecks(ctx, pool, []HealthCheckDef{def})
//
// 而生产走的是 RunChecks → runChecks(ctx, pool, AllHealthChecks())：**10 条一次
// 跑完**，任何一条**非 Optional** 的检查出错就 `return` 中止整轮。
//
// 四条 Optional 检查（modality_verification_stale / baseline_observation_stale /
// baseline_price_missing / supplier_price_drift）依赖的对象分属三个迁移：
//
//	827  v_model_modality_verification_progress   ← modality_verification_stale
//	830  model_baseline_price_observation_health  ← baseline_observation_stale
//	826  v_supplier_price_vs_baseline             ← 后两条共用
//
// 真实环境里它们**不同步**：运维在 826/827/830 之间逐个发布，于是
// 「一半在、一半不在」是常态而不是边角情况。此前**从未有任何一条判据跑过这种
// 组合**。如果 skip 的实现有下列任一形态，这个库是绿的而生产会整轮死掉：
//
//   - 「遇到错误就跳过」（把 Optional + 42P01 两个条件并成一个）；
//   - 「跳过」之后**没有 continue**，或 continue 之后把整轮状态标成成功；
//   - 跳过时**中止了**后面的检查（把「跳过」实现成「这一轮不做了」）。
//
// # 三段式承重
//
//	A 混合场景：两条依赖对象缺失（必须跳过）+ 一条非 Optional 有发现（必须照常
//	  落库）。「照常」是关键：如果跳过会截断整轮，非 Optional 那条就一行都不落，
//	  断言会红。
//	B 核心对象缺失且**不** Optional ⇒ 必须中止并报错。核心表（provider_models
//	  这类）消失绝不能被说成「一切正常」。
//	C Optional 但失败码**不是 42P01** ⇒ 同样必须中止。这半边才是 A 的牙齿：
//	  正是「无条件跳过」这种改法能同时骗过 A 和 B。
//
// # 量具自证（缺了它整条判据是空转）
//
// 「对象缺失」不能只看 to_regclass IS NULL 就断言它是「Optional 想放过的那个
// 缺失」—— 缺的方式有多种，只有 **42P01** 被放过。所以下面先直接跑每条缺失检查
// 的**真实查询**、把 PgError.Code 打出来断言就是 42P01。
//
// 这一步同时钉住源码里那段注释所依赖的前提：数据源必须是**关系**（视图/表）。
// 若有人改成直接读 825 加的列，826/827 未应用时报的是 **42703 undefined_column**
// 而不是 42P01，跳过不成立 ⇒ 整轮中止。这个前提此前只在注释里，判据里没有。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// healthCheckByID 从 AllHealthChecks() 里取一条**真实的**检查定义。
//
// 刻意不在测试里手抄 CheckID/Query/Severity：那样量的是我写的那份 SQL，
// 而不是仓库里真正跑的那份。手抄一份「能跳过的查询」能让这条判据在任何
// 实现下都绿 —— 与「判据要验对象而不是验复刻版」同一条纪律。
func healthCheckByID(t *testing.T, id string) HealthCheckDef {
	t.Helper()
	for _, c := range AllHealthChecks() {
		if c.CheckID == id {
			return c
		}
	}
	t.Fatalf("check %q is missing from AllHealthChecks()", id)
	return HealthCheckDef{}
}

// pgErrCode 跑一条查询并返回 PgError 的 SQLSTATE；不是 PgError 时返回 ""。
func pgErrCode(t *testing.T, pool *pgxpool.Pool, ctx context.Context, label, q string) string {
	t.Helper()
	rows, err := pool.Query(ctx, q)
	if err == nil {
		rows.Close()
		return ""
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: query failed with a non-PgError %v — this test asserts on SQLSTATE, so a "+
			"driver-level failure here means the ruler is broken, not the code under test", label, err)
	}
	return pgErr.Code
}

func TestOptionalSkipIsPerCheckAndOnlyForMissingObject(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{"routing_health_checks", "credential_model_bindings", "provider_models",
		"credentials", "providers", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 清理注册在**建表之前**。清理清单要穷举这次建出来的每一样东西 ——
	// 漏一个，下一轮 CREATE ... IF NOT EXISTS 会静默沿用旧结构。
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;
			DROP TABLE IF EXISTS public.model_baseline_price_reconciliation;
			DROP TABLE IF EXISTS public.routing_health_checks;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, `CREATE TABLE public.routing_health_checks (
		check_id text, severity text, entity_type text, entity_id bigint,
		entity_name text, detail text, fix_sql text, status text,
		created_at timestamptz, updated_at timestamptz,
		UNIQUE (check_id, entity_type, entity_id))`); err != nil {
		t.Fatalf("create routing_health_checks: %v", err)
	}
	if _, err := pool.Exec(ctx, supplierViewFixture(t)); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// runChecks 收尾无条件跑 autoFixCanonicalID，它读这一列。
	if _, err := pool.Exec(ctx, `ALTER TABLE public.provider_models
		ADD COLUMN IF NOT EXISTS canonical_cleared_at timestamptz`); err != nil {
		t.Fatalf("add canonical_cleared_at: %v", err)
	}
	// 只应用 826 —— 刻意**不**应用 827 / 830，于是它们的视图与表在这个库上真的
	// 不存在。这正是「迁移逐个发布」的中间态。
	mig826, err := os.ReadFile("../sql/migrations/startup/826_model_baseline_price.sql")
	if err != nil {
		t.Fatalf("read 826: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig826)); err != nil {
		t.Fatalf("apply 826: %v", err)
	}

	// 三个模型，各服务一条检查：
	//   m-pricey    基准 5.00/25.00，供应商 15.00/75.00 ⇒ 3.0x ⇒ supplier_price_drift 报
	//   m-nobase    **没有**基准价，供应商 6.00/30.00     ⇒ baseline_price_missing 报
	//   qwen-3-plus 基准 5.00/25.00，无供应商绑定        ⇒ family_unknown 报
	//                （models_canonical.family 可空无默认，插入不写即为 NULL）
	// 前两个模型名不匹配 family_unknown 的族正则，所以那条恰好 1 行。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, baseline_price_currency,
		                                    baseline_input_price_per_1m, baseline_output_price_per_1m)
		VALUES ('m-pricey','USD',5.00,25.00), ('qwen-3-plus','USD',5.00,25.00);
		INSERT INTO public.models_canonical (canonical_name) VALUES ('m-nobase');
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, v.raw, mc.id, mc.canonical_name
		  FROM public.providers pr,
		       (VALUES ('raw-pricey','m-pricey'),('raw-nobase','m-nobase')) AS v(raw, canon)
		  JOIN public.models_canonical mc ON mc.canonical_name = v.canon;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id,
		       CASE WHEN pm.raw_model_name = 'raw-pricey' THEN 15.00 ELSE 6.00 END,
		       CASE WHEN pm.raw_model_name = 'raw-pricey' THEN 75.00 ELSE 30.00 END,
		       'USD'
		  FROM public.credentials c, public.provider_models pm;`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	absentModality := healthCheckByID(t, "modality_verification_stale")
	absentObservation := healthCheckByID(t, "baseline_observation_stale")
	presentMissing := healthCheckByID(t, "baseline_price_missing")
	presentDrift := healthCheckByID(t, "supplier_price_drift")
	nonOptional := healthCheckByID(t, "family_unknown")

	// ---- 量具自证 ----
	//
	// 承重对象上钉的四件事，缺一件下面三段就都是空转。
	var present int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname='public' AND c.relname = ANY($1)`,
		[]string{"v_model_modality_verification_progress", "model_baseline_price_observation_health"}).
		Scan(&present); err != nil {
		t.Fatalf("probe 827/830 objects: %v", err)
	}
	if present != 0 {
		t.Fatalf("%d of the 827/830 objects exist in this database — the two "+
			"\"missing object\" checks are not testing the skip path at all", present)
	}
	var have826 bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.v_supplier_price_vs_baseline') IS NOT NULL`).
		Scan(&have826); err != nil {
		t.Fatalf("probe 826 view: %v", err)
	}
	if !have826 {
		t.Fatal("public.v_supplier_price_vs_baseline is absent after applying 826 — the two " +
			"\"present object\" checks are not testing anything")
	}

	// ★ 最关键的一条自证：缺的那两个对象，缺的方式必须**正是** Optional 放过的方式。
	//
	// 刻意**不**在这里断言 `def.Optional == true`。那会造出一个循环依赖：本判据的
	// 前提是「这两条被声明为 Optional」，而 A 段要验的正是「声明了 Optional 就
	// 真的跳过」。一旦生产里有人把 Optional 去掉，先撞上的是这条自证，报出来的是
	// 「测试的前提不成立」—— 而真实后果是**那些没应用 826/827/830 的环境整轮健康
	// 面死掉**。让它落到 A 段去红，那里的判词说的是生产上的后果。
	//
	// 2026-10-05 第一次写的时候这里有条 `!def.Optional → Fatalf`，teeth 立刻暴露了
	// 这个问题：把 Optional 改成 false，变异是红的，但红在**量具**上而不是承重的
	// A 段上。变异「红了」不等于它验证了目标。
	for _, absent := range []HealthCheckDef{absentModality, absentObservation} {
		if code := pgErrCode(t, pool, ctx, absent.CheckID, absent.Query); code != "42P01" {
			t.Fatalf("%s fails with SQLSTATE %q on this database, want 42P01. Optional only forgives "+
				"42P01, so this database is NOT the \"optional object missing\" shape this test needs. "+
				"If this is 42703, the check has started reading a column instead of a relation and "+
				"the whole skip design no longer covers it", absent.CheckID, code)
		}
	}

	// 三条会报的检查，各自必须真的命中行 —— 否则「落了 1 行」是空集自洽。
	wantHits := map[string]int{
		nonOptional.CheckID: 1, presentMissing.CheckID: 1, presentDrift.CheckID: 1,
	}
	for _, def := range []HealthCheckDef{nonOptional, presentMissing, presentDrift} {
		var hit int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+def.Query+`) q`).Scan(&hit); err != nil {
			t.Fatalf("run %s: %v", def.CheckID, err)
		}
		if hit != wantHits[def.CheckID] {
			t.Fatalf("%s matches %d rows, want %d — the seed did not land as intended and every "+
				"assertion about \"the round still ran\" below is vacuous", def.CheckID, hit, wantHits[def.CheckID])
		}
	}

	healthRows := func() map[string]int {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT check_id, count(*) FROM public.routing_health_checks
			GROUP BY check_id`)
		if err != nil {
			t.Fatalf("read the health table: %v", err)
		}
		defer rows.Close()
		out := map[string]int{}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatalf("scan: %v", err)
			}
			out[id] = n
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows: %v", err)
		}
		return out
	}

	// ---- A：混合场景。缺失的两条跳过，其余照常落库 ----
	//
	// 顺序是**刻意**的：缺失的那两条排在前面。这样一旦「跳过」被实现成
	// 「中止」，后面三条就一行都不落，A 段的断言立刻红。
	crit, warn, err := runChecks(ctx, pool, []HealthCheckDef{
		absentModality, absentObservation, nonOptional, presentMissing, presentDrift,
	})
	if err != nil {
		t.Fatalf("a round with two missing optional objects returned an error: %v — an environment "+
			"that has not applied 826/827/830 yet would lose its ENTIRE health surface, which is "+
			"exactly what HealthCheckDef.Optional exists to prevent", err)
	}

	got := healthRows()
	for _, skipped := range []HealthCheckDef{absentModality, absentObservation} {
		if n := got[skipped.CheckID]; n != 0 {
			t.Errorf("%s wrote %d health row(s) although its source object does not exist — a "+
				"check that reports on a table it cannot read is reporting nothing useful", skipped.CheckID, n)
		}
	}
	for _, fired := range []HealthCheckDef{nonOptional, presentMissing, presentDrift} {
		if got[fired.CheckID] == 0 {
			t.Errorf("%s wrote 0 health rows. It runs after the two skipped checks in the same "+
				"round, so a skip that aborts (instead of continuing) would produce exactly this. "+
				"The optional checks are not supposed to truncate the rest of the round",
				fired.CheckID)
		}
	}
	// 返回的计数与表里的行必须对得上：同时挡住「计了数没落库」与「落了库没计数」。
	if crit != 0 {
		t.Errorf("newCritical=%d, want 0 — none of these five checks is severity=critical", crit)
	}
	if warn != 3 {
		t.Errorf("newWarning=%d, want 3 (one row each from %s / %s / %s); the health table holds "+
			"those counts, so a mismatch means counting and persisting disagree",
			warn, nonOptional.CheckID, presentMissing.CheckID, presentDrift.CheckID)
	}

	// ---- B：核心对象缺失且不 Optional ⇒ 必须中止 ----
	//
	// 这半边是「缺表不许被说成一切正常」的正面证据：把一条真检查的 Optional
	// 强行置 false，它就必须报错。没有它，「A 段通过」与「无脑跳过一切错误」
	// 是同一种观测。
	if _, err := pool.Exec(ctx, `DELETE FROM public.routing_health_checks`); err != nil {
		t.Fatalf("clear the health table: %v", err)
	}
	required := absentModality
	required.Optional = false
	crit, warn, err = runChecks(ctx, pool, []HealthCheckDef{required, presentDrift})
	if err == nil {
		t.Errorf("a NON-optional check whose source object is missing completed the round with no "+
			"error (newCritical=%d newWarning=%d) — a vanished core object is now indistinguishable "+
			"from a healthy system", crit, warn)
	}
	if healthRows()[presentDrift.CheckID] != 0 {
		t.Errorf("%s still wrote rows after the round aborted — runChecks returned an error but "+
			"kept going, so \"the round failed\" would not mean \"the surface is stale\"",
			presentDrift.CheckID)
	}

	// ---- C：Optional 但失败码不是 42P01 ⇒ 同样必须中止 ----
	//
	// 这里的查询是**故意写坏**的：这是当场造出一个非 42P01 失败的唯一办法，而
	// 被测对象是 runChecks 里那个跳过判据，不是这条 SQL。
	//
	// 为什么必须单独一段：若把 skip 改成「Optional 就跳过」（去掉 42P01 条件），
	// A 段照绿、B 段照绿（它根本不 Optional），只有这一段会红。它是整条判据的
	// 牙齿所在。
	if _, err := pool.Exec(ctx, `DELETE FROM public.routing_health_checks`); err != nil {
		t.Fatalf("clear the health table: %v", err)
	}
	broken := HealthCheckDef{
		CheckID:  "fixture_broken_optional_query",
		Severity: "warning",
		Optional: true,
		// models_canonical 存在，但这一列不存在 ⇒ 42703 undefined_column。
		Query: `SELECT id, canonical_name, canonical_name, canonical_name
		          FROM models_canonical WHERE no_such_column_here IS NULL`,
	}
	if code := pgErrCode(t, pool, ctx, "fixture broken query", broken.Query); code != "42703" {
		t.Fatalf("the deliberately broken query fails with SQLSTATE %q, want 42703 — the "+
			"non-42P01 half of this test is not being exercised", code)
	}
	crit, warn, err = runChecks(ctx, pool, []HealthCheckDef{broken, presentDrift})
	if err == nil {
		t.Errorf("an Optional check that failed with 42703 undefined_column was skipped silently "+
			"(newCritical=%d newWarning=%d). Optional must forgive **missing objects** only: a "+
			"query that references a column which is not there is a bug in the check, and skipping "+
			"it turns a real defect into a silent hole in the health surface", crit, warn)
	}
	if healthRows()[presentDrift.CheckID] != 0 {
		t.Errorf("%s wrote rows even though the round before it aborted on 42703", presentDrift.CheckID)
	}
}
