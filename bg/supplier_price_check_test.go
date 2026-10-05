package bg

// 迁移 833：供应商价格列非负约束的真库判据。
//
// 背景（2026-10-04 真库实测）
//
// 「准确控制模型实际成本」有两个前提：基准价与供应商价。826 给基准价列加了
// CHECK（非负），而供应商侧在**全 schema 上零 CHECK、零 NOT NULL、零枚举**。
// 实测三条脏数据 rc=0 全部落库，灌进 826 的偏差视图后：
//
//	供应商 in/out     | 倍率 in  | 倍率 out | 问题
//	------------------|----------|----------|------------------------------
//	-5.00 / -25.00    | -1.0000  | -1.0000  | 负倍率，读起来像「便宜 100%」
//	100.00 / 1.00     | 20.0000  | 0.0400   | 同一行两个方向自相矛盾
//
// 这类缺陷不会报错、不会空集、报表照常出数 —— 与本包
// health_check_scan_guard_test.go 钉的那条属同一族「看起来做了」。
//
// # 判据取舍
//
// 承重的是**双向**：负价必须被拦、合法价（含全 NULL、全 0）必须不被拦。
// 只测单向的话，把 CHECK 写成恒真也能过。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// supplierPriceFixture 建一张与真表同名列的最小替身。
//
// 为什么不用真 schema：那需要灌 00-prereqs + 01-schema（数百个对象），而本
// 判据只关心四个价格列的约束行为。
//
// ⚠ 这里**刻意不写**任何 CHECK —— 判据要验的正是「833 加上去的那几个」，
// 替身自带 CHECK 会让「拦得住」恒真。真相是：迁移 833 的字节被直接应用
// （见下），替身负责的只是「一张没有这四个约束的表」。
const supplierPriceFixture = `
CREATE TABLE public.credential_model_bindings (
    id                      bigserial PRIMARY KEY,
    credential_id           bigint NOT NULL,
    provider_model_id       bigint NOT NULL,
    unit_price_in_per_1m   numeric,
    unit_price_out_per_1m  numeric,
    cache_read_price_per_1m  numeric,
    cache_write_price_per_1m numeric,
    currency                text DEFAULT 'USD',
    billing_mode            text DEFAULT 'per_token',
    UNIQUE (credential_id, provider_model_id)
)
`

// TestSupplierPriceNonnegCheckRejectsDirtyAndAcceptsLegal 钉 833 的行为。
func TestSupplierPriceNonnegCheckRejectsDirtyAndAcceptsLegal(t *testing.T) {
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

	// 安全闸：必须真的能建表，否则下面量的是残留结构。
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='credential_model_bindings'`).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skip("credential_model_bindings already exists — this test drops it")
	}

	// ★ 清理注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到
	// 「表已存在」直接 SKIP，人看到的是「通过」，实际一次都没跑。
	defer func() {
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.credential_model_bindings`)
	}()

	if _, err := pool.Exec(ctx, supplierPriceFixture); err != nil {
		t.Fatalf("create fixture: %v", err)
	}

	// ★ 迁移 833 的**真实字节**，不是手抄的约束 —— 与 provider/modality_gate_alignment_test.go
	// 应用真 825、真 827 同一取舍。手抄会把「迁移里到底写了哪几个约束」这件事
	// 从判据里摘出去。
	mig, err := os.ReadFile("../sql/migrations/startup/833_supplier_price_nonneg_check.sql")
	if err != nil {
		t.Fatalf("read 833: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 833: %v", err)
	}

	// **量具自证**：四个约束必须真的在表上。
	// 漏了这一步，「负价被拦」可能只是因为别的约束恰好也拦了它。
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.credential_model_bindings'::regclass
		  AND conname LIKE 'cmb_price_nonneg%'`).Scan(&n); err != nil {
		t.Fatalf("count constraints: %v", err)
	}
	if n != 4 {
		t.Fatalf("833 applied but %d nonneg constraints are present, want 4 — the assertions "+
			"below would be measuring something other than this migration", n)
	}

	// 承重之一：四列的负价**逐列**都必须被拦。
	// 逐列而不是一次性四个 —— 只拦住其中一列时，一次性断言会误以为全过。
	for _, col := range []string{
		"unit_price_in_per_1m", "unit_price_out_per_1m",
		"cache_read_price_per_1m", "cache_write_price_per_1m",
	} {
		var id int64
		insErr := pool.QueryRow(ctx, `INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, `+col+`) VALUES (1, 1, -1.00)
			RETURNING id`).Scan(&id)
		if insErr == nil {
			t.Errorf("a negative %s was accepted — without this constraint a -5.00 supplier price "+
				"against a 5.00 baseline yields ratio -1.0, which a cost report reads as "+
				"\"100%% cheaper than list\"", col)
		} else if !strings.Contains(insErr.Error(), "cmb_price_nonneg") {
			t.Errorf("%s: negative value rejected, but not by a cmb_price_nonneg* constraint: %v",
				col, insErr)
		}
	}

	// 承重之二：合法价**不得**被误伤。三种形态各自有理由：
	//   · 正常价
	//   · 全 NULL（「没定价」是合法状态，!= 0）
	//   · 全 0（免费档，仓里 billing_mode='free' 的凭据走这条路）
	legal := []struct {
		name            string
		in, out, cr, cw any
	}{
		{"normal", 5.00, 25.00, 0.50, 6.25},
		{"all null", nil, nil, nil, nil},
		{"all zero", 0, 0, 0, 0},
	}
	for i, l := range legal {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m,
			 cache_read_price_per_1m, cache_write_price_per_1m)
			VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
			1+i, 1+i, l.in, l.out, l.cr, l.cw).Scan(&id); err != nil {
			t.Errorf("legal price row %q was rejected: %v — the constraint is over-broad", l.name, err)
		}
	}

	// 承重之三：迁移里那条盘点 SQL 必须**原样可用**。
	//
	// 为什么单独钉：上生产前必须先知道有多少脏行，而 833 的注释把这段 SQL
	// 写成了「必做前置」。如果它哪天与迁移里的实际列名脱节，运维照着跑会
	// 得到一个假的安全感。2026-10-04 真库已实测：先写一行 -1.00，再跑
	// 盘点得到 1，再 ADD CONSTRAINT 得到
	// `check constraint "cmb_price_nonneg_in" ... is violated by some row`。
	var dirty int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.credential_model_bindings
		WHERE unit_price_in_per_1m < 0 OR unit_price_out_per_1m < 0
		   OR cache_read_price_per_1m < 0 OR cache_write_price_per_1m < 0`).Scan(&dirty); err != nil {
		t.Fatalf("the inventory query from the 833 header does not run: %v", err)
	}
	if dirty != 0 {
		t.Errorf("the inventory query reports %d dirty row(s) on a table that has never accepted a "+
			"negative price — the query is not measuring the four constrained columns", dirty)
	}
}

// TestSupplierPriceNonnegCheckDownRemovesOnlyConstraints 钉 down。
//
// down 的正确性是**双向**的：约束要真被移除（否则回滚不掉），且**数据要留着**
// （down 只回滚结构，绝不碰价格 —— 价格是钱）。
func TestSupplierPriceNonnegCheckDownRemovesOnlyConstraints(t *testing.T) {
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

	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name='credential_model_bindings'`).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skip("credential_model_bindings already exists — this test drops it")
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DROP TABLE IF EXISTS public.credential_model_bindings`)
	}()

	if _, err := pool.Exec(ctx, supplierPriceFixture); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	for _, f := range []string{
		"../sql/migrations/startup/833_supplier_price_nonneg_check.sql",
		"../sql/migrations/startup/833_supplier_price_nonneg_check.down.sql",
		"../sql/migrations/startup/833_supplier_price_nonneg_check.down.sql", // 幂等重放
		"../sql/migrations/startup/833_supplier_price_nonneg_check.sql",      // 再 up
	} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}

	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.credential_model_bindings'::regclass
		  AND conname LIKE 'cmb_price_nonneg%'`).Scan(&n); err != nil {
		t.Fatalf("count constraints: %v", err)
	}
	if n != 4 {
		t.Errorf("after up → down → down → up there are %d nonneg constraints, want 4 — one of "+
			"those four steps is not idempotent", n)
	}

	// 保留一行**合法**数据，验证 down 不碰数据。
	if _, err := pool.Exec(ctx, `INSERT INTO public.credential_model_bindings
		(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m)
		VALUES (7, 7, 3.00, 15.00)`); err != nil {
		t.Fatalf("seed a legal row: %v", err)
	}

	// ★ 真的应用 down 文件（不是手写 DROP CONSTRAINT —— 那样量的是我写的
	// SQL，不是仓库里那份 down），然后证明负价**能**写了。
	// 这是「约束确实被移除」的正面证据，比只查 pg_constraint 计数更难造假。
	down, err := os.ReadFile("../sql/migrations/startup/833_supplier_price_nonneg_check.down.sql")
	if err != nil {
		t.Fatalf("read 833 down: %v", err)
	}
	if _, err := pool.Exec(ctx, string(down)); err != nil {
		t.Fatalf("apply 833 down: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid = 'public.credential_model_bindings'::regclass
		  AND conname LIKE 'cmb_price_nonneg%'`).Scan(&n); err != nil {
		t.Fatalf("count constraints after down: %v", err)
	}
	if n != 0 {
		t.Fatalf("the down migration left %d cmb_price_nonneg* constraints behind", n)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.credential_model_bindings
		(credential_id, provider_model_id, unit_price_in_per_1m) VALUES (8, 8, -1.00)`); err != nil {
		t.Errorf("a negative price is still rejected after the down migration ran — something else "+
			"is enforcing this, so the down migration is not what removed it: %v", err)
	}
	var kept int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.credential_model_bindings
		WHERE credential_id = 7 AND unit_price_in_per_1m = 3.00`).Scan(&kept); err != nil {
		t.Fatalf("verify the legal row survived: %v", err)
	}
	if kept != 1 {
		t.Errorf("the legal price row did not survive — the down path must remove constraints only, " +
			"never touch prices (prices are money)")
	}
}

// TestSupplierPriceDriftAlertsOnlyActionableOverpricing 钉「偏差被告警」这半边。
//
// 2026-10-05 盘点出的缺口：826 的偏差视图与对账台账**没有任何生产代码读取**
// （台账只写不读），所以供应商涨价只会被记进台账、**没有人被告知**。这条检查
// 补的是成本控制闭环的最后一环：度量 → 记录 → **告警**。
//
// 承重是**双向**的：比原厂贵 1.5 倍以上的要报；比原厂便宜或持平的、
// 以及**币种不可比**的，一律不报。
//
//	· 只测单向的话，把 WHERE 写成恒真也能过；
//	· 币种不可比那一支尤其重要：倍率是拿两种货币直接相除算出来的，它没有意义
//	  （826 的视图专门为它留了 currency_comparable 标记），把它报成告警会让人
//	  拿着一堆数字噪声去找供应商。
func TestSupplierPriceDriftAlertsOnlyActionableOverpricing(t *testing.T) {
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

	// 四个模型，基准价一律 5.00/25.00：
	//   m-pricey   供应商 15.00/75.00 ⇒ 3.0x  ⇒ **该报**
	//   m-slight   供应商  5.50/27.50 ⇒ 1.1x  ⇒ 阈值下，**不该报**
	//   m-cheap    供应商  1.00/ 5.00 ⇒ 0.2x  ⇒ **不该报**（比原厂便宜不是问题）
	//   m-fx       供应商 15.00/75.00 但**币种 CNY** ⇒ 3.0x 不可比 ⇒ **不该报**
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, baseline_price_currency,
		                                    baseline_input_price_per_1m, baseline_output_price_per_1m)
		VALUES ('m-pricey','USD',5.00,25.00), ('m-slight','USD',5.00,25.00),
		       ('m-cheap','USD',5.00,25.00), ('m-fx','USD',5.00,25.00);
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, v.raw, mc.id, mc.canonical_name
		  FROM public.providers pr,
		       (VALUES ('raw-pricey','m-pricey'),('raw-slight','m-slight'),
		               ('raw-cheap','m-cheap'),('raw-fx','m-fx')) AS v(raw, canon)
		  JOIN public.models_canonical mc ON mc.canonical_name = v.canon;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id,
		       CASE WHEN pm.raw_model_name = 'raw-pricey' OR pm.raw_model_name = 'raw-fx' THEN 15.00
		            WHEN pm.raw_model_name = 'raw-slight' THEN 5.50 ELSE 1.00 END,
		       CASE WHEN pm.raw_model_name = 'raw-pricey' OR pm.raw_model_name = 'raw-fx' THEN 75.00
		            WHEN pm.raw_model_name = 'raw-slight' THEN 27.50 ELSE 5.00 END,
		       CASE WHEN pm.raw_model_name = 'raw-fx' THEN 'CNY' ELSE 'USD' END
		  FROM public.credentials c, public.provider_models pm;`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// **量具自证**：四种形态必须真的落到位，否则下面的断言全是空转。
	var ratios int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.v_supplier_price_vs_baseline
		WHERE has_baseline AND input_price_ratio > 1.5 AND currency_comparable`).Scan(&ratios); err != nil {
		t.Fatalf("probe view: %v", err)
	}
	if ratios != 1 {
		t.Fatalf("the fixture produced %d comparable over-priced rows, want exactly 1 — the seed "+
			"did not land as intended and every assertion below is vacuous", ratios)
	}

	var def HealthCheckDef
	found := false
	for _, c := range AllHealthChecks() {
		if c.CheckID == "supplier_price_drift" {
			def, found = c, true
			break
		}
	}
	if !found {
		t.Fatal("supplier_price_drift missing from AllHealthChecks() — a supplier raising its " +
			"price 3x over list would be recorded in the ledger and never surfaced to anyone")
	}

	var hit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+def.Query+`) q`).Scan(&hit); err != nil {
		t.Fatalf("run the check query: %v", err)
	}
	if hit != 1 {
		t.Errorf("the check matched %d rows, want exactly 1 (only m-pricey) — cheap suppliers, "+
			"mild overcharges and currency-incomparable rows must not be reported", hit)
	}

	crit, warn, err := runChecks(ctx, pool, []HealthCheckDef{def})
	if err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	if crit != 0 || warn != 1 {
		t.Errorf("got newCritical=%d newWarning=%d for 1 matched row on a severity=warning check", crit, warn)
	}

	var entityID int64
	var name, detail, fix string
	if err := pool.QueryRow(ctx, `SELECT entity_id, entity_name, detail, fix_sql
		FROM public.routing_health_checks WHERE check_id='supplier_price_drift' LIMIT 1`).
		Scan(&entityID, &name, &detail, &fix); err != nil {
		t.Fatalf("read the inserted row: %v", err)
	}
	if entityID == 0 {
		t.Errorf("inserted entity_id=0 — the no-scan-branch signature")
	}
	if name != "p1:raw-pricey" {
		t.Errorf("entity_name=%q, want \"p1:raw-pricey\" (the provider CODE, not its id)", name)
	}
	// 判词必须带上「贵到几倍」和两侧的数，否则运维拿到一条「有点贵」不知道查什么。
	if !strings.Contains(detail, "3") {
		t.Errorf("detail=%q does not state the multiple — the operator needs the ratio, not a "+
			"bare 'too expensive'", detail)
	}
	// 三个**不该**报的，一个都不能漏。
	for _, notWant := range []string{"raw-slight", "raw-cheap", "raw-fx"} {
		var leaked int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
			WHERE check_id='supplier_price_drift' AND entity_name LIKE '%`+notWant+`%'`).
			Scan(&leaked); err != nil {
			t.Fatalf("count %s: %v", notWant, err)
		}
		if leaked != 0 {
			t.Errorf("the check reported %s — cheap suppliers and mild overcharges are not "+
				"problems, and currency-incomparable rows have a meaningless ratio", notWant)
		}
	}
}

// TestFreeBaselineChargedBySupplierIsAlerted 钉「原厂免费而供应商收费」这一类。
//
// # 缺口（2026-10-05 真库实测）
//
// `v_supplier_price_vs_baseline` 的倍率对 0 分母给 NULL（视图里
// `OR mc.baseline_input_price_per_1m = 0 THEN NULL`，**防除零是对的**），
// 而 `has_baseline` 判的是 `IS NOT NULL` ⇒ 0 基准也算「有基准价」。
//
// ⇒ 后果：「原厂 0 / 供应商 5」这一行
//
//	· `supplier_price_drift`：`ratio > 1.5` 不成立（NULL）⇒ **不报**；
//	· `baseline_price_missing`：`NOT has_baseline` 不成立 ⇒ **也不报**。
//
// 而台账侧同样把它记成 `not_comparable` / "neither side has a comparable price"
// —— 与「两边都是 0（真免费、价一致）」**判词与 reason 完全一样**。
//
// 这是成本核算里最可行动的一类偏差（本该白给的东西在收钱），而它此前是**静默**的。
// 真实库里 `billing_mode='free'` 有 105 行，这个形态不是假想。
//
// 承重是**双向**：免费→收费要报；真免费→免费、以及比原厂便宜，一律不报。
// 只测单向的话，把 WHERE 写成恒真也能过。
func TestFreeBaselineChargedBySupplierIsAlerted(t *testing.T) {
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

	// 四个模型：
	//   m-free-charged 基准 **0**/0，供应商 5.00/25.00 ⇒ 免费→收费，**该报**
	//   m-free-free    基准 **0**/0，供应商 0/0        ⇒ 真免费且价一致，**不该报**
	//   m-pricey       基准 5.00/25.00，供应商 15/75   ⇒ 3.0x，**该报**（护栏）
	//   m-cheap        基准 5.00/25.00，供应商 1/5      ⇒ 0.2x，**不该报**（护栏）
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, baseline_price_currency,
		                                    baseline_input_price_per_1m, baseline_output_price_per_1m)
		VALUES ('m-free-charged','USD',0.00,0.00), ('m-free-free','USD',0.00,0.00),
		       ('m-pricey','USD',5.00,25.00), ('m-cheap','USD',5.00,25.00);
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, v.raw, mc.id, mc.canonical_name
		  FROM public.providers pr,
		       (VALUES ('raw-free-charged','m-free-charged'),('raw-free-free','m-free-free'),
		               ('raw-pricey','m-pricey'),('raw-cheap','m-cheap')) AS v(raw, canon)
		  JOIN public.models_canonical mc ON mc.canonical_name = v.canon;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id,
		       CASE pm.raw_model_name
		         WHEN 'raw-free-charged' THEN 5.00 WHEN 'raw-free-free' THEN 0.00
		         WHEN 'raw-pricey' THEN 15.00 ELSE 1.00 END,
		       CASE pm.raw_model_name
		         WHEN 'raw-free-charged' THEN 25.00 WHEN 'raw-free-free' THEN 0.00
		         WHEN 'raw-pricey' THEN 75.00 ELSE 5.00 END,
		       'USD'
		  FROM public.credentials c, public.provider_models pm;`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// **量具自证**：两行的倍率必须真的都是 NULL（视图防了除零），而 has_baseline
	// 必须是 true（0 也是「有基准价」）。若这三条不成立，下面量的是别的东西。
	var nullRatios, hasBaseline int
	if err := pool.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE input_price_ratio IS NULL AND output_price_ratio IS NULL),
			count(*) FILTER (WHERE has_baseline)
		FROM public.v_supplier_price_vs_baseline
		WHERE canonical_name IN ('m-free-charged','m-free-free')`).
		Scan(&nullRatios, &hasBaseline); err != nil {
		t.Fatalf("probe the view: %v", err)
	}
	if nullRatios != 2 || hasBaseline != 2 {
		t.Fatalf("the two zero-baseline rows have %d NULL ratios and %d has_baseline, want 2/2 — "+
			"the premise of this test (a 0 baseline yields no ratio but counts as \"has a baseline\") "+
			"no longer holds in the view", nullRatios, hasBaseline)
	}

	def := healthCheckByID(t, "supplier_price_drift")
	var matched []string
	rows, err := pool.Query(ctx, def.Query)
	if err != nil {
		t.Fatalf("run the check query: %v", err)
	}
	for rows.Next() {
		var a, b, c, d string
		if err := rows.Scan(&a, &b, &c, &d); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		matched = append(matched, b)
		if strings.Contains(c, "FREE") {
			t.Logf("free-to-paid detail: %s", c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	// ⚠ entity_name 是 `provider_code:raw_model_name`（`p1:raw-pricey`），
	// 所以「报没报」必须按**子串**判，不能拿裸名直接比。
	want := []string{"raw-free-charged", "raw-pricey"}
	for _, name := range want {
		found := false
		for _, m := range matched {
			if strings.Contains(m, name) {
				found = true
			}
		}
		if !found {
			t.Errorf("the check did not report %q. A model the original vendor gives away, being "+
				"charged for, is the most actionable cost finding there is — and because a ratio "+
				"against a 0 baseline is NULL it slips past every ratio comparison. Reported: %v",
				name, matched)
		}
	}
	for _, name := range []string{"raw-free-free", "raw-cheap"} {
		for _, m := range matched {
			if strings.Contains(m, name) {
				t.Errorf("the check reported %q, which must stay silent: a model that is free on "+
					"both sides has nothing to reconcile, and a cheaper-than-list supplier is not a "+
					"problem. Reported: %v", name, matched)
			}
		}
	}

	// 落库：只跑查询不会写健康面，所以 detail 那条断言之前少了这一步，
	// 报出来的是「no rows in result set」而不是断言失败 —— 症状指向了别处。
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}

	// detail 必须说清「免费→收费」而不是给一个 "?" 倍率 —— 运营要知道下一步做什么。
	var detail string
	if err := pool.QueryRow(ctx, `SELECT detail FROM routing_health_checks
		WHERE check_id='supplier_price_drift' AND entity_name LIKE '%raw-free-charged%'`).
		Scan(&detail); err != nil {
		t.Fatalf("read the free-to-paid row: %v", err)
	}
	if !strings.Contains(detail, "FREE") {
		t.Errorf("detail=%q does not say the baseline is free — an operator seeing \"charging ?x\" "+
			"has no way to tell this apart from a rounding disagreement", detail)
	}
}

// TestFreeBaselineGetsItsOwnLedgerVerdict 钉台账那一半。
//
// 健康面告警只解决「有人会被告知」；台账要解决「事后能查出来当时判成了什么」。
// 修复前两者都记成 not_comparable / "neither side has a comparable price"，与
// 「两边都是 0」完全一致 —— 也就是说**台账里查不出免费→收费这件事发生过**。
//
// 纯函数，不需要数据库。
func TestFreeBaselineGetsItsOwnLedgerVerdict(t *testing.T) {
	now := time.Now()
	zero, five, twentyFive := 0.0, 5.0, 25.0
	ssot := &BaselinePrice{
		InputPer1M: &zero, OutputPer1M: &zero, Currency: "USD",
		SourceURL: "https://vendor.example/pricing", FetchedAt: now.Format(time.RFC3339),
	}

	// 承重之一：免费 → 收费必须有自己的判词，且必须进入 drift 计数
	// （对账侧的告警路径统计 counts[drift] > 0，不新增迁移即生效）。
	r := ReconcileBaselinePrice("free-model", ssot,
		&PriceObservation{InputPer1M: &five, OutputPer1M: &twentyFive, Currency: "USD"}, now)
	if r.Verdict != PriceVerdictDrift {
		t.Errorf("free -> charged verdict = %q (detail %v), want %q. Before the fix it was %q with "+
			"reason %q, which is the same verdict the benign \"free on both sides\" case produces",
			r.Verdict, r.Detail, PriceVerdictDrift, PriceVerdictNotComparable, "neither side has a comparable price")
	}
	if reason, _ := r.Detail["reason"].(string); !strings.Contains(reason, "free") {
		t.Errorf("reason = %q, want one that says the baseline is free — the number is absent by "+
			"design (a ratio against 0 is undefined), so the reason is the only thing left", reason)
	}

	// 承重之二：真免费（两边都是 0）**不得**被报成 drift。
	rFree := ReconcileBaselinePrice("free-model", ssot,
		&PriceObservation{InputPer1M: &zero, OutputPer1M: &zero, Currency: "USD"}, now)
	if rFree.Verdict == PriceVerdictDrift {
		t.Errorf("free -> free verdict = %q, want not drift. There is nothing to reconcile when both "+
			"sides are 0, and reporting it would make the free-to-paid finding indistinguishable "+
			"from the benign case (detail %v)", rFree.Verdict, rFree.Detail)
	}
}
