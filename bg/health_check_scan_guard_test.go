package bg

// 健康面「查得到问题、报不出内容」这条缺陷的判据。
//
// # 缺陷（真库实测，非推断）
//
// runChecks 里每条检查的扫描是**按 CheckID 逐个 switch 特判**的。缺 case 时
// switch 走空，entityID / entityName / detail / fixSQL 全保持零值，而循环
// **照样把这一行 INSERT 进 routing_health_checks**，并按声明的 severity 计一次
// 告警。⇒ 症状是「检查确实发现了问题、也确实计了告警、但健康面里那一行
// entity_id=0、entity_name=''、detail=''」——运维看到的是一条没有内容、点不开
// 的一键修复入口。
//
// 真库读数（2026-10-04，查询命中 1 行时）：
//
//	修前： entity_id=0  entity_name=""  detail=""  fix_sql=""
//	修后： entity_id=1  entity_name="1:m-no"  detail="no vendor baseline price ..."
//
// 这类缺陷的性质与「恒真的判据」同源：**它不会报错、不会空集、计数照涨**。
// 绿色的一轮健康检查可以同时是一条完全无法行动的信息。
//
// # 为什么上一轮没抓到
//
// 判据验的是**查询**（命中几行、shape 对不对），而缺陷在**扫描分支**上。
// 「查询正确」与「查询结果能被读出来」之间隔着那个 switch，中间没有断言。

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestEveryHealthCheckHasScanBranch 钉住「每条 CheckID 都有对应的 case」。
//
// 为什么用**文本扫描**而不是反射或真库行为：Go 的 switch 不能被反射枚举，
// 而真库行为判据需要为每条检查各造一份能让它命中一行的数据（8 条检查各依赖
// 不同的表与列），成本远高于它防的回退。文本扫描抓的正是那个具体回退——
// 「加了一条 CheckID，忘了加 case」。
//
// 同样的取舍见 bg/observation_health_test.go 的 TestReconcileWorkerRecordsEveryFetchOutcome
// 与 db/ensure_chain_order_test.go：形状钉子不能证明语义正确，只防「有人把
// 这几行当冗余删掉」。这里防的是**少写**，不是写错。
func TestEveryHealthCheckHasScanBranch(t *testing.T) {
	src, err := os.ReadFile("routing_health_checks.go")
	if err != nil {
		t.Fatalf("read routing_health_checks.go: %v", err)
	}
	// ★ 正则必须认**逗号分隔的多值 case**（2026-10-05 被它自己抓到一次）。
	//
	// 原式是 `case\s+"([a-z_]+)"\s*:` —— 它要求闭引号后面**紧跟冒号**，所以
	// 写成 `case "a", "b":` 时**一个都读不到**。而两个 ID 共用一个 case body
	// （逐绑定聚合的那几条健康检查形状完全一样）是这里的自然写法，于是我
	// 合并 case 之后，守卫把 `supplier_price_drift` 与
	// `supplier_price_currency_mismatch` **双双报成「缺 case」**。
	//
	// ⇒ 症状是量具坏了而**被测物是对的**。当时有两个选择：把 case 拆成两份
	// 复制 body（迁就量具），或者让量具认识多值形态（选后者）。判据的价值在于
	// 它测的是**真实 dispatch 形状**，迁就它等于把量具的错误固化成代码形状。
	caseRE := regexp.MustCompile(`(?m)^\s*case\s+((?:"[a-z_]+"\s*,?\s*)+):`)
	idRE := regexp.MustCompile(`"([a-z_]+)"`)
	present := make(map[string]bool)
	for _, m := range caseRE.FindAllStringSubmatch(string(src), -1) {
		for _, id := range idRE.FindAllStringSubmatch(m[1], -1) {
			present[id[1]] = true
		}
	}
	if len(present) == 0 {
		t.Fatal("no case labels found — the regex no longer matches the source, so this test " +
			"would report every check as missing and tell you nothing")
	}

	// 量具自证：case 集合必须**真的**从源码里读出来，且数量合理。
	// 如果将来 switch 的写法变了（例如改成 map 分派），这里会读到 0 个 case。
	if len(present) < 6 {
		t.Fatalf("only %d case labels parsed from the source, want at least 6 — the regex or the "+
			"dispatch shape changed and this test is no longer measuring what it claims", len(present))
	}

	var missing []string
	for _, chk := range AllHealthChecks() {
		if !present[chk.CheckID] {
			missing = append(missing, chk.CheckID)
		}
	}
	if len(missing) > 0 {
		t.Errorf("these checks have no scan branch in runChecks, so a hit row is inserted with "+
			"entity_id=0, entity_name='', detail='' — the check finds the problem and then reports "+
			"an empty one: %v", missing)
	}
}

// TestBaselinePriceChecksReportNonEmptyRows 是行为判据：钉住「命中的行读出来
// 四列非空」，也就是上面那个缺陷的**反面**。
//
// 复用 supplierViewCardinalityTest.go 的夹具（同一个包、同一批表），因为偏差
// 视图正是这两条检查的数据源。夹具已含 826 建的对账表，views 在下面单独应用。
func TestBaselinePriceChecksReportNonEmptyRows(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	// ★ 清理注册在建表**之前**：建到一半失败会留残桩，下一轮安全闸看到
	// 「表已存在」直接 SKIP，人看到的是「通过」，实际一次都没跑。
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;
			DROP TABLE IF EXISTS public.model_baseline_price_reconciliation;
			DROP TABLE IF EXISTS public.model_baseline_price_observation_health;
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
	// autoFixCanonicalID 写 provider_models.canonical_cleared_at；夹具不建它。
	if _, err := pool.Exec(ctx, `ALTER TABLE public.provider_models
		ADD COLUMN IF NOT EXISTS canonical_cleared_at timestamptz`); err != nil {
		t.Fatalf("add canonical_cleared_at: %v", err)
	}

	viewSQL, err := os.ReadFile("../sql/migrations/startup/826_model_baseline_price.sql")
	if err != nil {
		t.Fatalf("read 826: %v", err)
	}
	if _, err := pool.Exec(ctx, string(viewSQL)); err != nil {
		t.Fatalf("apply 826: %v", err)
	}
	// 830 的观察源健康表（baseline_observation_stale 的数据源）
	if _, err := pool.Exec(ctx, observationHealthFixture); err != nil {
		t.Fatalf("create observation-health table: %v", err)
	}

	// 一个**有**基准价、一个**没有** —— 双向是承重的：
	// 只测「缺价的会报」的话，把 WHERE 写成恒真也能过。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, baseline_price_currency,
		                                    baseline_input_price_per_1m, baseline_output_price_per_1m)
		VALUES ('has-base','USD',5.00,25.00), ('no-base',NULL,NULL,NULL);
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, m.raw, mc.id, m.canon
		  FROM public.providers pr,
		       (VALUES ('m-has','has-base'),('m-no','no-base')) AS m(raw,canon)
		  JOIN public.models_canonical mc ON mc.canonical_name = m.canon;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id, 6.00, 30.00, 'USD' FROM public.credentials c, public.provider_models pm;
		-- 观察源：一条已连续失败（应被 baseline_observation_stale 报出）
		INSERT INTO public.model_baseline_price_observation_health
			(source_url, last_attempt_at, consecutive_failures, last_error)
		VALUES ('https://example.invalid/api.json', now(), 3, 'EOF');`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, id := range []string{"baseline_observation_stale", "baseline_price_missing"} {
		var def HealthCheckDef
		found := false
		for _, c := range AllHealthChecks() {
			if c.CheckID == id {
				def, found = c, true
				break
			}
		}
		if !found {
			t.Fatalf("check %s missing from AllHealthChecks()", id)
		}

		// **量具自证（第一道）**：这条检查的查询必须真的命中。
		// 种子写错时它是 0，而下面「0 行时 detail 为空」的断言不会触发 ——
		// 那样测试会绿，而它量的是「什么都没发生时输出为空」，恒真。
		var hit int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+def.Query+`) q`).Scan(&hit); err != nil {
			t.Fatalf("%s: run the check query: %v", id, err)
		}
		if hit == 0 {
			t.Fatalf("%s: the check query matched 0 rows for the seeded data — the fixture never "+
				"reached the check, so every assertion below is vacuous", id)
		}

		// ★ 注意返回顺序：runChecks 返回 (newCritical, newWarning, err)。
		crit, warn, err := runChecks(ctx, pool, []HealthCheckDef{def})
		if err != nil {
			t.Fatalf("%s: runChecks: %v", id, err)
		}
		if want := hit; warn != want || crit != 0 {
			t.Errorf("%s: severity='%s' and %d matched rows, got newCritical=%d newWarning=%d — "+
				"both checks under test declare severity=warning, so a nonzero critical here means "+
				"the counting is off", id, def.Severity, want, crit, warn)
		}

		var entityID int64
		var name, detail, fix string
		if err := pool.QueryRow(ctx, `SELECT entity_id, entity_name, detail, fix_sql
			FROM public.routing_health_checks WHERE check_id=$1 LIMIT 1`, id).
			Scan(&entityID, &name, &detail, &fix); err != nil {
			t.Fatalf("%s: read the inserted row (the check matched %d rows but inserted none): %v",
				id, hit, err)
		}
		if entityID == 0 {
			t.Errorf("%s: inserted entity_id=0 — that is the no-scan-branch signature: the query "+
				"matched a row, but the switch had no case for this CheckID so the row was inserted "+
				"with every column at its zero value", id)
		}
		if name == "" {
			t.Errorf("%s: inserted entity_name='' — the check found the problem and then reported an "+
				"unactionable one", id)
		}
		if detail == "" {
			t.Errorf("%s: inserted detail='' — an empty detail is not actionable in the health surface", id)
		}
	}

	// 双向的另一半：baseline_price_missing **不得**报出那个有基准价的模型。
	// 没有这一条，WHERE 写成恒真（`WHERE true`）也能通过上面所有断言 ——
	// 而那正是「误报淹没真信号」的方向。
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='baseline_price_missing' AND entity_name LIKE '%m-has%'`).Scan(&leaked); err != nil {
		t.Fatalf("count leaked rows: %v", err)
	}
	if leaked != 0 {
		t.Errorf("baseline_price_missing reported %d row(s) for m-has, which HAS a baseline price "+
			"(5.00/25.00) — the check is reporting models that are fine", leaked)
	}
}

// TestTextHashIsStableAndDistinct 钉住 textHash 的两条性质。
//
// 稳定性是承重的：它决定「同一个 source_url 跨轮刷新是否落到同一行」。
// 不稳定 ⇒ UPSERT 收敛不了 ⇒ 每轮插一条新记录，旧行留在 status='open' 变成
// 永不消失的僵尸告警。
//
// 相异性不是承重但值得钉：退化到常量会让所有观察源挤进同一行
// （UNIQUE(check_id, entity_type, entity_id) 下互相覆盖）。
func TestTextHashIsStableAndDistinct(t *testing.T) {
	// 稳定性：同一输入必须同一输出。
	a := textHash("https://example.invalid/api.json")
	b := textHash("https://example.invalid/api.json")
	if a != b {
		t.Errorf("textHash is not stable: same input hashed to %d then %d — the same observation "+
			"source would land on a new health row every cycle", a, b)
	}
	// 不得退化：两个明显不同的 url 必须分开。
	if textHash("https://a.invalid/api.json") == textHash("https://b.invalid/api.json") {
		t.Error("textHash collides on two one-character-different URLs — every observation source " +
			"would overwrite the previous one's health row")
	}
}

// TestBaselinePriceMissingCollapsesToOneRowWhenGapIsSystemic 钉「缺失面大时只报
// 一行汇总」这半边。
//
// 为什么单独一条：TestBaselinePriceChecksReportNonEmptyRows 只种了 2 个模型，走
// 的是 ≤20 那条「逐个列名」的分支。**汇总分支当时没有任何覆盖** —— 而它恰恰
// 是本轮改动要解决的那个问题。
//
// 背景（2026-10-04 真库实测）：那台库 186 个有价绑定 / 163 个模型，SSOT 尚空，
// 逐绑定 LIMIT 50 的写法会**每一轮都报满 50 行**，而那 50 行说的是同一件事。
//
// 双向承重：
//
//	· 缺失 > 20 ⇒ 恰好 1 行，且 entity_name 以 '(bulk)' 开头。
//	· 缺失 ≤ 20 ⇒ 不出现 '(bulk)'，逐个列名。
//
// 只测前者的话，把 `<= 20` 的分支删掉也能过。
func TestBaselinePriceMissingCollapsesToOneRowWhenGapIsSystemic(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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
	if _, err := pool.Exec(ctx, `ALTER TABLE public.provider_models
		ADD COLUMN IF NOT EXISTS canonical_cleared_at timestamptz`); err != nil {
		t.Fatalf("add canonical_cleared_at: %v", err)
	}
	viewSQL, err := os.ReadFile("../sql/migrations/startup/826_model_baseline_price.sql")
	if err != nil {
		t.Fatalf("read 826: %v", err)
	}
	if _, err := pool.Exec(ctx, string(viewSQL)); err != nil {
		t.Fatalf("apply 826: %v", err)
	}

	// 25 个模型，**全部**没有基准价（模拟 SSOT 尚空）⇒ 应落在 >20 那条分支。
	// 25 > 20 是刻意选的：贴着阈值，阈值若被改成 24 这条会立刻红。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.models_canonical (canonical_name) SELECT 'm-'||g FROM generate_series(1,25) g;
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, 'raw-'||mc.id, mc.id, mc.canonical_name
		  FROM public.providers pr CROSS JOIN public.models_canonical mc;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id, 6.00, 30.00, 'USD' FROM public.credentials c, public.provider_models pm;`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var def HealthCheckDef
	found := false
	for _, c := range AllHealthChecks() {
		if c.CheckID == "baseline_price_missing" {
			def, found = c, true
			break
		}
	}
	if !found {
		t.Fatal("baseline_price_missing missing from AllHealthChecks()")
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+def.Query+`) q`).Scan(&n); err != nil {
		t.Fatalf("run the check query: %v", err)
	}
	if n != 1 {
		t.Errorf("the check returned %d rows for 25 models with no baseline price, want exactly 1 — "+
			"this is the noise shape the change was meant to remove (it used to report 50 rows "+
			"saying the same thing every cycle)", n)
	}

	// 查询的四个输出列都是**无名的**（SELECT 里没有 AS），所以只能按位置取。
	var qID int64
	var name, qDetail, qFix string
	if err := pool.QueryRow(ctx, `SELECT q.* FROM (`+def.Query+`) q`).Scan(&qID, &name, &qDetail, &qFix); err != nil {
		t.Fatalf("read the single row: %v", err)
	}
	if !strings.HasPrefix(name, "(bulk) ") {
		t.Errorf("the single row is %q, want a '(bulk) …' summary — a one-row result that lists "+
			"an individual model means the collapse happened for the wrong reason", name)
	}
	// 汇总行必须带缺失数与总量，否则运维拿不到工作量。
	if !strings.Contains(name, "25") {
		t.Errorf("the summary row %q does not state how many models are missing — "+
			"it must carry the workload, not just the fact", name)
	}

	// 走一遍 runChecks，确认汇总行也能被正常读出（entity_id 非 0，否则就是
	// 扫描分支缺失的特征值）。
	crit, warn, err := runChecks(ctx, pool, []HealthCheckDef{def})
	if err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	if crit != 0 || warn != 1 {
		t.Errorf("got newCritical=%d newWarning=%d for 1 summary row on a severity=warning check", crit, warn)
	}
	var entityID int64
	var detail string
	if err := pool.QueryRow(ctx, `SELECT entity_id, detail FROM public.routing_health_checks
		WHERE check_id='baseline_price_missing' LIMIT 1`).Scan(&entityID, &detail); err != nil {
		t.Fatalf("read the inserted row: %v", err)
	}
	if entityID == 0 {
		t.Errorf("the summary row was inserted with entity_id=0 — that is the no-scan-branch " +
			"signature; the summary row must use a non-zero sentinel (it uses -1)")
	}
	if detail == "" {
		t.Error("the summary row has an empty detail — an empty row is not actionable")
	}
}
