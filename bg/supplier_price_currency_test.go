package bg

// 「币种不可比 / 币种未知」这两类，在偏差告警链路上此前**只写不读**。
//
// # 缺口（2026-10-05 盘出来的事实，不是推理）
//
// `supplier_price_drift` 的 WHERE 里有 `v.currency_comparable`，所以币种不同的
// 行**刻意不报** —— 作者的注释写得很清楚：「倍率是拿两种货币直接相除算出来的，
// 它没有意义。那些行由对账台账的 not_comparable 判词负责（需要的是汇率决策，
// 不是告警）」。
//
// 那个委托**没有接收方**：台账 `model_baseline_price_reconciliation` 只写不读
// （与本会话早前实测的「deviation recorded but nobody told」同一族）。
// ⇒ **供应商按 CNY 计价、而原厂基准是 USD 时，这条绑定的实际成本从此不受监控**，
// 而且它在两张报表里都**看不见**：偏差视图给 `currency_comparable=false`、
// 两条健康检查都不提它。运营的仪表盘上这个模型「一切正常」。
//
// # 第二个缺口：未知币种会**伪造**一个看起来正常的倍率
//
// 826 视图里 `currency_comparable` 定义为
// `COALESCE(cmb.currency,'USD') IS NOT DISTINCT FROM COALESCE(mc.baseline_price_currency,'USD')`。
// ⇒ **基准侧币种为空时，它被当成 USD**。若供应商确实按 USD 计价，两侧碰巧
// "可比"，视图就照着两个数算出一个 3.0x 的倍率，而 `supplier_price_drift`
// 会把这个**伪造比较的结果**当成真偏差报出来。
//
// 这比「算不出来」更坏：算不出来至少是 NULL（看起来像"没数据"），伪造出来的是
// 一个**精确的错数字**，带着 ratio 与 detail 一起进健康表。
//
// 写入侧（`validate` 强制 currency 非空）已在本会话补上，所以产品代码路径已经
// 造不出这种行；但运维手工 SQL、以及**任何绕过 validate 的写入**都还可以。
// 检查侧不能假定写入侧永远是干净的。
//
// # 真库测量（本文件 2026-10-05 实测，不是推理）
//
// `SELECT count(*) … WHERE currency IS NULL` on the read-only production-shaped
// library: **0**（2045 行，全是 CNY 或 USD，且该列是 `NOT NULL`）。
// ⇒ 供应商侧的空币种在真环境**不可达**，本文件因此**不**试图覆盖它；
// 覆盖的是**基准侧**为空这一侧（视图照抄了它的原值，所以判据能从视图读出来）。
//
// ⚠ 顺带量到一处**对象 SSOT 与真库的分歧**（未修，见 changelog）：
// `sql/objects/tables/credential_model_bindings.sql` 里
// `currency text DEFAULT 'USD'`、六个价格/币种列**都没有 NOT NULL**，
// 而真库这六列全是 `NOT NULL`。⇒ 按仓里的对象 SSOT 装出来的新环境在这条轴上
// **比生产弱**。它不是本文件要修的东西，但它决定了「供应商侧空币种」在
// 新装环境里是可达的 —— 所以那句「真环境不可达」只对现有这台库成立。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// currencyFixture 搭出偏差视图 + 健康表，并把五种形态一次种齐。
//
// ★ 清理用 `t.Cleanup` 而不是 `defer`（helper 里的 `defer` 会在夹具建好的
// 那一刻就触发，表格当场被删）；★ 池也用 `t.Cleanup` 且**先注册**，靠 LIFO
// 让 DROP 跑在关池之前 —— 否则拿已关闭的池 DROP、err 被丢、残桩静默留库，
// 下一轮安全闸看到「表已存在」直接 SKIP（无结论被读成通过）。
func currencyFixture(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real database")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

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
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
			DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;
			DROP TABLE IF EXISTS public.model_baseline_price_reconciliation;
			DROP TABLE IF EXISTS public.routing_health_checks;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	})

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
	// 应用**真** 826：基准价九列与那个视图都由它建，不手抄。
	mig, err := os.ReadFile("../sql/migrations/startup/826_model_baseline_price.sql")
	if err != nil {
		t.Fatalf("read 826: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 826: %v", err)
	}

	// 五种形态。基准价一律 5.00/25.00，供应商 15.00/75.00（= 3.0x），
	// 只有币种与价格在变：
	//
	//   m-pricey  USD / USD  3.0x  真偏差            ⇒ 漂移**要**报
	//   m-slight  USD / USD  1.1x  阈值下             ⇒ 谁都不报
	//   m-cheap   USD / USD  0.2x  比原厂便宜          ⇒ 谁都不报
	//   m-fx      USD / CNY  ——    币种不同，倍率无定义 ⇒ 漂移不报、新检查**要**报
	//   m-nofx    (空) / USD ——    基准币种未知，视图当 USD ⇒ 3.0x 是**伪造**的
	//
	// 承重全在这五行的差异上，所以它们必须**逐行**落到位（见下面的量具自证）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, baseline_price_currency,
		                                    baseline_input_price_per_1m, baseline_output_price_per_1m)
		VALUES ('m-pricey','USD',5.00,25.00),
		       ('m-slight','USD',5.00,25.00),
		       ('m-cheap','USD',5.00,25.00),
		       ('m-fx','USD',5.00,25.00),
		       ('m-nofx',NULL,5.00,25.00);
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1');
		INSERT INTO public.credentials (provider_id) SELECT id FROM public.providers WHERE code='p1';
		INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		SELECT pr.id, 'raw-' || replace(v.canon,'m-',''), mc.id, mc.canonical_name
		  FROM public.providers pr,
		       (VALUES ('m-pricey'),('m-slight'),('m-cheap'),('m-fx'),('m-nofx')) AS v(canon)
		  JOIN public.models_canonical mc ON mc.canonical_name = v.canon;
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id,
		       CASE WHEN pm.raw_model_name = 'raw-slight' THEN 5.50
		            WHEN pm.raw_model_name = 'raw-cheap'  THEN 1.00 ELSE 15.00 END,
		       CASE WHEN pm.raw_model_name = 'raw-slight' THEN 27.50
		            WHEN pm.raw_model_name = 'raw-cheap'  THEN  5.00 ELSE 75.00 END,
		       CASE WHEN pm.raw_model_name = 'raw-fx' THEN 'CNY' ELSE 'USD' END
		  FROM public.credentials c, public.provider_models pm;`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ★ 量具自证：五种形态必须**真的**各自落在预期的那一格。
	// 少了它，「夹具没按预期落位」会伪装成下面的断言红，排查方向被带偏。
	// m-nofx 那一行是本文件的核心：它证明视图真的把 NULL 币种当成了 USD
	// （currency_comparable=true）—— 也就是「伪造倍率」这个前提本身成立。
	var comparable, mismatch, nullCur int
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE currency_comparable AND input_price_ratio > 1.5),
			count(*) FILTER (WHERE NOT currency_comparable AND baseline_currency = 'USD'),
			count(*) FILTER (WHERE baseline_currency IS NULL AND currency_comparable)
		  FROM public.v_supplier_price_vs_baseline WHERE has_baseline`).Scan(
		&comparable, &mismatch, &nullCur); err != nil {
		t.Fatalf("probe view: %v", err)
	}
	// comparable 3.0x 的有两条：m-pricey（真的）与 m-nofx（伪造的）。
	if comparable != 2 {
		t.Fatalf("the fixture produced %d comparable 3.0x rows, want 2 (m-pricey + the "+
			"fabricated m-nofx) — the seed did not land as intended and every assertion "+
			"below is vacuous", comparable)
	}
	if mismatch != 1 {
		t.Fatalf("known-currency-mismatch rows = %d, want 1 (m-fx) — the fixture is wrong", mismatch)
	}
	if nullCur != 1 {
		t.Fatalf("rows with a NULL baseline currency that the view still calls comparable = %d, "+
			"want 1 (m-nofx) — if this is 0 the premise of this whole file is wrong", nullCur)
	}
	return pool
}

func healthCheckDef(t *testing.T, id string) HealthCheckDef {
	t.Helper()
	for _, c := range AllHealthChecks() {
		if c.CheckID == id {
			return c
		}
	}
	t.Fatalf("%s missing from AllHealthChecks()", id)
	return HealthCheckDef{}
}

// TestUnknownBaselineCurrencyMustNotProduceAFabricatedDriftAlert 钉承重之一：
// **不许**用一个伪造的比较结果去告警。
//
// 修前实测：m-nofx（基准价 5.00、币种为空、供应商 USD 15.00）被 826 视图判成
// `currency_comparable=true` 并算出 3.0x，于是 `supplier_price_drift` 报出
// 「charging 3x the original-vendor baseline」。而源页面**从没说过**这个基准价
// 是 USD —— 这个倍率是两个数字相除的结果，不是偏差。
func TestUnknownBaselineCurrencyMustNotProduceAFabricatedDriftAlert(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := currencyFixture(t, ctx)

	def := healthCheckDef(t, "supplier_price_drift")
	// 走**真的** runChecks 而不是直接跑 SQL：健康检查的列名是表达式（视图列拼接，
	// 没有 AS），直接 `WHERE entity_name` 会报 42703；而落库后的 entity_name 由
	// Go 侧按 provider_code 重算 —— 那正是运营在仪表盘上看到的东西，钉它才对。
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='supplier_price_drift' AND entity_name LIKE '%raw-nofx%'`).Scan(&leaked); err != nil {
		t.Fatalf("count raw-nofx: %v", err)
	}
	if leaked != 0 {
		t.Errorf("supplier_price_drift reported raw-nofx — the baseline currency is unknown, "+
			"so the 3x it computed comes from treating an unstated currency as USD. That is a "+
			"fabricated number presented as a measured deviation (%d row(s))", leaked)
	}

	// 承重的另一半：m-pricey 必须**仍然**报。加这条是为了防「把守卫写成恒假」
	// —— 那会让这一条判据变成「什么都不报也通过」。
	var genuine int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='supplier_price_drift' AND entity_name LIKE '%raw-pricey%'`).Scan(&genuine); err != nil {
		t.Fatalf("count raw-pricey: %v", err)
	}
	if genuine != 1 {
		t.Errorf("supplier_price_drift matched %d rows for raw-pricey, want 1 — a real 3x "+
			"overcharge in a known currency must still be reported, otherwise the guard is a "+
			"blanket mute", genuine)
	}
}

// TestCurrencyMismatchAndUnknownCurrencyAreSurfaced 钉承重之二：
// 这两类此前**只写不读**（台账记了 `not_comparable`，没有任何读者）。
func TestCurrencyMismatchAndUnknownCurrencyAreSurfaced(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := currencyFixture(t, ctx)

	def := healthCheckDef(t, "supplier_price_currency_mismatch")

	// 落库形态：必须经 runChecks 才会进健康表 —— 只跑查询的话健康表里没有行，
	// 下面读 detail 的断言会报 "no rows in result set"，症状指向「没落库」而不是
	// 断言本身（同一个坑，第二次踩）。
	if _, _, err := runChecks(ctx, pool, []HealthCheckDef{def}); err != nil {
		t.Fatalf("runChecks: %v", err)
	}

	var hits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='supplier_price_currency_mismatch'`).Scan(&hits); err != nil {
		t.Fatalf("count persisted rows: %v", err)
	}
	if hits != 2 {
		t.Errorf("%d row(s) reported, want exactly 2 (m-fx known mismatch + m-nofx unknown "+
			"baseline currency) — every priced binding in these two states is unmonitored, so "+
			"under-reporting here is a silent hole in cost control", hits)
	}

	// 三种「不该报」的一个都不能漏，否则这条检查自己会变成噪声。
	for _, notWant := range []string{"raw-pricey", "raw-slight", "raw-cheap"} {
		var leaked int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
			WHERE check_id='supplier_price_currency_mismatch' AND entity_name LIKE '%`+notWant+`%'`).
			Scan(&leaked); err != nil {
			t.Fatalf("count %s: %v", notWant, err)
		}
		if leaked != 0 {
			t.Errorf("the check reported %s — same-currency bindings are exactly the ones it "+
				"must stay quiet about", notWant)
		}
	}

	// 判词必须带两个币种，否则运营不知道该去核对哪一侧。
	var detail string
	if err := pool.QueryRow(ctx, `SELECT detail FROM public.routing_health_checks
		WHERE check_id='supplier_price_currency_mismatch' AND entity_name LIKE '%raw-fx%'`).
		Scan(&detail); err != nil {
		t.Fatalf("read the m-fx row: %v", err)
	}
	for _, want := range []string{"USD", "CNY"} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail=%q does not name %q — the operator cannot act without knowing "+
				"which two currencies are involved", detail, want)
		}
	}
}
