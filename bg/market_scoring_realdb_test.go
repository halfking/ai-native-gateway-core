package bg

// market_scoring_realdb_test.go — 评分 job 的真库腿（TEST_DATABASE_URL，
// 需要干净的 scratch 库：夹具要建表）。
//
// 覆盖（定稿 M2-T3 验收门的全链形态）：
//  1. 夹具 = 对象 SSOT 的 models_canonical + 真迁移 826/940/941/949/958/959
//     （不手抄视图 —— 视图由真迁移链建出来，959 的双列与 registry 种子一并真跑）；
//  2. ScoreNow 全链：读（复刻 959 join）→ 纯函数 → 单事务写；
//  3. 断言手算对拍四模型 + 同区零 FX（CNY 绑定 vs ppio 国内地板，fx_used NULL）
//     + unknown_currency + **canonical_id 排除分支**（mpr 行 canonical_id 指向
//     别的 canonical ⇒ 视图与评分读 SQL 同样排除 —— 名字裸 join 会错误接上）；
//  4. 幂等重放：跑两遍 ⇒ 行数不变、值收敛；
//  5. 过期行清理：删掉一条绑定 ⇒ 重跑 ⇒ 该行被 DELETE（RowsDeleted ≥ 1）。
//
// ★ 清理注册在**建表之前**（LIFO：DROP 先于关池）——见
// supplier_view_cardinality_test.go 的「先注册清理，再建表」注释。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const marketScoringRealdbFX = "6.687755"

func marketScoringRealdbFixture(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — this needs a real (scratch) database")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	tables := []string{"credential_model_bindings", "provider_models", "credentials",
		"providers", "models_canonical", "market_price_reference",
		"supplier_market_position", "market_platform_registry", "market_scoring_runs"}
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
			DROP TABLE IF EXISTS public.market_price_reference_sync;
			DROP TABLE IF EXISTS public.supplier_market_position;
			DROP TABLE IF EXISTS public.market_price_reference;
			DROP TABLE IF EXISTS public.market_platform_registry;
			DROP TABLE IF EXISTS public.market_scoring_runs;
			DROP TABLE IF EXISTS public.schema_migrations;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	})

	apply := func(name string) {
		t.Helper()
		b, err := os.ReadFile("../sql/migrations/startup/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	// 对象 SSOT 的 models_canonical（同 supplierViewFixture，不手抄）。
	if _, err := pool.Exec(ctx, supplierViewFixture(t)); err != nil {
		t.Fatalf("base fixture: %v", err)
	}
	// scratch 库没有迁移记账表 —— 按公开形态补上（940/958/959 尾部自登记用）。
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.schema_migrations (
		version text PRIMARY KEY,
		description text,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatalf("create schema_migrations: %v", err)
	}
	// AF-7：绑定的组合键在生产是 UNIQUE（smp 的 PK 引用它）——脚手架表没有
	// ⇒ 补上生产形态，重复绑定在夹具里就会当场炸（而不是靠评分侧防御去重）。
	if _, err := pool.Exec(ctx, `ALTER TABLE public.credential_model_bindings
		ADD CONSTRAINT cmb_cred_model_uq UNIQUE (credential_id, provider_model_id)`); err != nil {
		t.Fatalf("add binding combo unique: %v", err)
	}
	// 844 代的缓存价列（941 视图读它们；supplierViewFixture 的脚手架表没有）。
	if _, err := pool.Exec(ctx, `ALTER TABLE public.credential_model_bindings
		ADD COLUMN IF NOT EXISTS cache_read_price_per_1m numeric,
		ADD COLUMN IF NOT EXISTS cache_write_price_per_1m numeric`); err != nil {
		t.Fatalf("add cache price columns: %v", err)
	}
	// 真迁移链：826（基准列+视图 v1）→ 940（行情三表）→ 941/949（视图追列）
	// → 958（registry）→ 959（双地板列 + 视图 v3 + registry 补种）。
	apply("826_model_baseline_price.sql")
	apply("940_market_price_reference_family.sql")
	apply("941_supplier_view_market_columns.sql")
	apply("949_supplier_view_binding_id.sql")
	apply("958_market_platform_registry.sql")
	apply("959_market_dual_floor_a17.sql")
	apply("961_market_scoring_runs.sql")

	// ── 种数据：四模型 + 漂移探针（211 真库 2026-11-12 实测值） ──────────
	mustExec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v (sql: %s)", err, sql)
		}
	}
	mustExec(`INSERT INTO public.models_canonical (canonical_name) VALUES
		('claude-opus-4-8'),('claude-fable-5'),('minimax-m2.7'),('glm-5.1'),('test-drift-model'),
		('unused-canonical')`)
	mustExec(`INSERT INTO public.providers (code, display_name) VALUES ('p1','P1'),('p2','P2')`)
	mustExec(`INSERT INTO public.credentials (provider_id) VALUES (1),(1),(2),(2)`)
	mustExec(`INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name) VALUES
		(1,'claude-opus-4-8',   (SELECT id FROM public.models_canonical WHERE canonical_name='claude-opus-4-8'),  'claude-opus-4-8'),
		(1,'claude-fable-5',    (SELECT id FROM public.models_canonical WHERE canonical_name='claude-fable-5'),   'claude-fable-5'),
		(2,'glm-5.1',           (SELECT id FROM public.models_canonical WHERE canonical_name='glm-5.1'),          'glm-5.1'),
		(2,'minimax-m2.7',      (SELECT id FROM public.models_canonical WHERE canonical_name='minimax-m2.7'),     'minimax-m2.7'),
		(2,'minimax-m2.7-cny',  (SELECT id FROM public.models_canonical WHERE canonical_name='minimax-m2.7'),     'minimax-m2.7'),
		(2,'glm-5.1-eur',       (SELECT id FROM public.models_canonical WHERE canonical_name='glm-5.1'),          'glm-5.1'),
		(1,'test-drift-model',  (SELECT id FROM public.models_canonical WHERE canonical_name='test-drift-model'), 'test-drift-model')`)
	mustExec(`INSERT INTO public.credential_model_bindings
		(credential_id, provider_model_id, currency, unit_price_in_per_1m, unit_price_out_per_1m) VALUES
		(1, (SELECT id FROM public.provider_models WHERE raw_model_name='claude-opus-4-8'),  'USD', 10,   25),
		(1, (SELECT id FROM public.provider_models WHERE raw_model_name='claude-fable-5'),   'USD', 15,   50),
		(2, (SELECT id FROM public.provider_models WHERE raw_model_name='glm-5.1'),          'USD', 0.1,  0.1),
		(2, (SELECT id FROM public.provider_models WHERE raw_model_name='minimax-m2.7'),     'USD', 0.30, 1.20),
		(3, (SELECT id FROM public.provider_models WHERE raw_model_name='minimax-m2.7-cny'), 'CNY', 2.5,  10),
		(4, (SELECT id FROM public.provider_models WHERE raw_model_name='glm-5.1-eur'),      'EUR', 1,    2),
		(4, (SELECT id FROM public.provider_models WHERE raw_model_name='test-drift-model'), 'USD', 1,    2)`)
	// ★ drift 绑定价格 1/2（不等价）：若写 1/1 会被 C3 门判 suspect_symmetric，
	//   污染 961 台账的 non_trusted_count 判据（2/7）。

	// market_price_reference：211 真库实测值。★ test-drift-model 的行
	// canonical_id 指向**无绑定的** unused-canonical（≠ 自己的 canonical id）：
	// 视图与评分读 SQL 的 canonical_id 排除分支都必须把它从自己的绑定上拒开
	// （id 非空 ⇒ 名字分支被跳过 ⇒ 无 join）。注意不能指向别的绑定模型 ——
	// id 匹配分支在 AF-8 dedup 里**优先于**名字匹配，那样它会合法认领那个
	// canonical（夹具第一版就犯了这个错，被 opus 断言当场抓住）。
	mustExec(`INSERT INTO public.market_price_reference
		(canonical_name, dimension, currency, vendor_list_currency,
		 vendor_list_in_per_1m, vendor_list_out_per_1m,
		 overseas_min_in_per_1m, overseas_min_out_per_1m, overseas_min_platform_id, overseas_min_platform_count,
		 domestic_min_in_per_1m, domestic_min_out_per_1m, domestic_min_platform_id, domestic_min_platform_count,
		 market_min_in_per_1m, market_min_out_per_1m, fx_rate, source_run_date) VALUES
		('claude-opus-4-8','standard','CNY','', NULL, NULL, 5, 25, 'openrouter', 1, NULL, NULL, '', 0, 33.438775, 167.193875, 6.687755, '2026-11-12'),
		('claude-fable-5','standard','CNY','', NULL, NULL, 10, 50, 'openrouter', 1, NULL, NULL, '', 0, 66.877550, 334.387750, 6.687755, '2026-11-12'),
		('glm-5.1','standard','CNY','CNY', 6, 24, NULL, NULL, '', 0, 6, 24, 'zhipu', 5, 6, 24, 6.687755, '2026-11-12'),
		('minimax-m2.7','standard','CNY','', NULL, NULL, NULL, NULL, '', 0, 2.1, 8.4, 'ppio', 3, 2.1, 8.4, 6.687755, '2026-11-12'),
		('test-drift-model','standard','CNY','', NULL, NULL, 1, 1, 'openrouter', 1, NULL, NULL, '', 0, 6.687755, 33.438775, 6.687755, '2026-11-12')`)
	mustExec(`UPDATE public.market_price_reference SET canonical_id =
		(SELECT id FROM public.models_canonical WHERE canonical_name='unused-canonical')
		WHERE canonical_name='test-drift-model'`)

	return pool
}

// row 是 q() 的读出形状（smp 一行的断言面）。
type row struct {
	currency   string
	confidence string
	c1, mp     *float64
	refUsed    string
	refCount   int
	vsOv, vsDm *float64
	adv        *float64
	fxUsed     *float64
	vendorCur  string
}

// runOneCheck 按 CheckID 取健康检查 Query 并在夹具库上执行，返回 3 列文本键行
// （与 runChecksWithRecorder 的 textHash 形态同一批列）。
func runOneCheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool, checkID string) map[string]string {
	t.Helper()
	var query string
	for _, chk := range AllHealthChecks() {
		if chk.CheckID == checkID {
			query = chk.Query
			break
		}
	}
	if query == "" {
		t.Fatalf("check %q missing", checkID)
	}
	rows, err := pool.Query(ctx, query)
	if err != nil {
		t.Fatalf("check %s query: %v", checkID, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, n, d string
		if err := rows.Scan(&k, &n, &d); err != nil {
			t.Fatalf("check %s scan: %v", checkID, err)
		}
		out[k] = n + " :: " + d
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("check %s rows: %v", checkID, err)
	}
	return out
}

func TestMarketScoringRealDBFullPipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	pool := marketScoringRealdbFixture(t, ctx)

	// q 按 (credential_id, raw_model_name) 取一行 smp（同一 raw 有多绑定时必须消歧）。
	q := func(cred int, pmRaw string) row {
		t.Helper()
		var r row
		err := pool.QueryRow(ctx, `
			SELECT s.currency, s.price_confidence, s.discount_vs_vendor, s.market_position,
			       s.market_ref_used, s.market_ref_platform_count,
			       s.market_position_vs_overseas, s.market_position_vs_domestic,
			       s.market_advantage_score, s.fx_used, s.vendor_ref_currency
			  FROM public.supplier_market_position s
			  JOIN public.provider_models pm ON pm.id = s.provider_model_id
			 WHERE pm.raw_model_name = $1 AND s.credential_id = $2`, pmRaw, cred).Scan(
			&r.currency, &r.confidence, &r.c1, &r.mp, &r.refUsed, &r.refCount,
			&r.vsOv, &r.vsDm, &r.adv, &r.fxUsed, &r.vendorCur)
		if err != nil {
			t.Fatalf("read smp row %s@cred%d: %v", pmRaw, cred, err)
		}
		return r
	}

	w := NewMarketScoringWorker(pool)

	// ══ 阶段 1：基础夹具（minimax 海外地板缺位）＝ 四模型手算 + 同区零 FX ══
	stats, err := w.ScoreNow(ctx)
	if err != nil {
		t.Fatalf("ScoreNow: %v", err)
	}
	if stats.BindingsRead != 7 || stats.RowsWritten != 7 {
		t.Fatalf("run stats: %+v (want 7 bindings written)", stats)
	}

	t.Run("claude-opus-4-8", func(t *testing.T) {
		r := q(1, "claude-opus-4-8")
		if r.currency != "USD" || r.confidence != "trusted" || r.refUsed != "overseas" || r.refCount != 1 {
			t.Fatalf("meta: %+v", r)
		}
		expectF64(t, "C1", r.c1, nil, 0)
		expectF64(t, "market_position", r.mp, scoreF64(0.8571), 0)
		expectF64(t, "vs_overseas", r.vsOv, scoreF64(0.8571), 0)
		expectF64(t, "vs_domestic", r.vsDm, nil, 0)
		expectF64(t, "advantage", r.adv, scoreF64(0), 0)
		expectF64(t, "fx_used", r.fxUsed, nil, 0)
	})

	t.Run("claude-fable-5", func(t *testing.T) {
		r := q(1, "claude-fable-5")
		expectF64(t, "market_position", r.mp, scoreF64(0.9231), 0)
		expectF64(t, "vs_overseas", r.vsOv, scoreF64(0.9231), 0)
		expectF64(t, "advantage", r.adv, scoreF64(0), 0)
	})

	t.Run("glm-5.1 suspect_symmetric + 跨币 C1", func(t *testing.T) {
		r := q(2, "glm-5.1")
		if r.confidence != "suspect_symmetric" {
			t.Fatalf("confidence: %q", r.confidence)
		}
		expectF64(t, "C1", r.c1, scoreF64(0.9554), 1e-4)
		if r.vendorCur != "CNY" {
			t.Fatalf("vendor_ref_currency: %q", r.vendorCur)
		}
		expectF64(t, "vs_domestic", r.vsDm, scoreF64(22.4291), 1e-4)
		expectF64(t, "vs_overseas", r.vsOv, nil, 0)
		expectF64(t, "advantage", r.adv, scoreF64(95.54), 1e-2)
		expectF64(t, "fx_used", r.fxUsed, scoreF64(6.687755), 0)
	})

	t.Run("minimax-m2.7 USD（海外地板缺位：综合地板兜底）", func(t *testing.T) {
		r := q(2, "minimax-m2.7")
		expectF64(t, "vs_domestic", r.vsDm, scoreF64(1.0467), 1e-4)
		expectF64(t, "vs_overseas", r.vsOv, nil, 0)
		// 同区海外缺 → 综合地板折算兜底 = 100×(1−1.5/1.5700336) = 4.46
		expectF64(t, "advantage", r.adv, scoreF64(4.46), 1e-2)
		expectF64(t, "fx_used", r.fxUsed, scoreF64(6.687755), 0)
	})

	t.Run("minimax-m2.7 CNY 同区零 FX（fx_used NULL）", func(t *testing.T) {
		r := q(3, "minimax-m2.7-cny")
		if r.currency != "CNY" || r.refUsed != "domestic" || r.refCount != 3 {
			t.Fatalf("meta: %+v", r)
		}
		expectF64(t, "market_position", r.mp, scoreF64(0.84), 0)
		expectF64(t, "vs_domestic", r.vsDm, scoreF64(0.84), 0)
		expectF64(t, "vs_overseas", r.vsOv, nil, 0)
		expectF64(t, "advantage", r.adv, scoreF64(0), 0)
		// ★ 全行零折算 ⇒ fx_used NULL（定稿验收用例的判据）
		expectF64(t, "fx_used", r.fxUsed, nil, 0)
	})

	t.Run("EUR 绑定 unknown_currency 全 NULL", func(t *testing.T) {
		r := q(4, "glm-5.1-eur")
		if r.confidence != "unknown_currency" || r.refUsed != "" {
			t.Fatalf("meta: %+v", r)
		}
		expectF64(t, "C1", r.c1, nil, 0)
		expectF64(t, "market_position", r.mp, nil, 0)
		expectF64(t, "vs_domestic", r.vsDm, nil, 0)
		expectF64(t, "advantage", r.adv, nil, 0)
	})

	t.Run("canonical_id 排除分支：错误 id 的 mpr 行被拒（与视图同语义）", func(t *testing.T) {
		r := q(4, "test-drift-model")
		expectF64(t, "C1", r.c1, nil, 0)
		expectF64(t, "market_position", r.mp, nil, 0)
		expectF64(t, "vs_overseas", r.vsOv, nil, 0)
		if r.refUsed != "" {
			t.Fatalf("refUsed: want '', got %q", r.refUsed)
		}
	})

	// ══ 阶段 2：M2-T4 告警腿的地板/绑定增量 ⇒ 重跑 ═══════════════════════
	// minimax 补海外地板（1/4 USD，n=3）+ 两条「贵超 50%」形态绑定
	// （cred5 3/12 ⇒ 5/15=0.333 火；cred6 fable 100/400 ⇒ 0.12 但 n=1 被门挡）。
	if _, err := pool.Exec(ctx, `UPDATE public.market_price_reference
	   SET overseas_min_in_per_1m = 1, overseas_min_out_per_1m = 4,
	       overseas_min_platform_id = 'openrouter', overseas_min_platform_count = 3
	 WHERE canonical_name = 'minimax-m2.7' AND dimension = 'standard'`); err != nil {
		t.Fatalf("update minimax floor: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (provider_id) VALUES (2),(1)`); err != nil {
		t.Fatalf("seed alert credentials: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.credential_model_bindings
		(credential_id, provider_model_id, currency, unit_price_in_per_1m, unit_price_out_per_1m) VALUES
		(5, (SELECT id FROM public.provider_models WHERE raw_model_name='minimax-m2.7'), 'USD', 3, 12),
		(6, (SELECT id FROM public.provider_models WHERE raw_model_name='claude-fable-5'),  'USD', 100, 400)`); err != nil {
		t.Fatalf("seed alert bindings: %v", err)
	}
	if _, err := w.ScoreNow(ctx); err != nil {
		t.Fatalf("ScoreNow phase2: %v", err)
	}

	t.Run("minimax-m2.7 USD 阶段2：同区海外地板生效（优势 70.00）", func(t *testing.T) {
		r := q(2, "minimax-m2.7")
		// 主列 = 同区海外地板原生：5/1.5 = 3.3333（比地板便宜）
		expectF64(t, "market_position", r.mp, scoreF64(3.3333), 0)
		if r.refUsed != "overseas" || r.refCount != 3 {
			t.Fatalf("ref meta: %+v", r)
		}
		// advantage = 100×clamp(1−1.5/5,0,1) = 70.00（同区地板原生，不再走兜底）
		expectF64(t, "advantage", r.adv, scoreF64(70), 0)
	})
	t.Run("minimax-m2.7 CNY 阶段2：vs_overseas 出现（跨币折算）", func(t *testing.T) {
		r := q(3, "minimax-m2.7-cny")
		// vs_overseas = (5 USD × 6.687755)/12.5 = 2.6751
		expectF64(t, "vs_overseas", r.vsOv, scoreF64(2.6751), 1e-4)
		expectF64(t, "fx_used", r.fxUsed, scoreF64(6.687755), 0)
	})

	// ══ 961 台账：当日一行（重放收敛），non_trusted=2 / trusted=7 ═════════
	var runs int
	var nonTrusted, trusted int
	if err := pool.QueryRow(ctx, `SELECT count(*), max(non_trusted_count), max(trusted_count)
		FROM public.market_scoring_runs WHERE run_date = current_date`).Scan(&runs, &nonTrusted, &trusted); err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	if runs != 1 {
		t.Fatalf("ledger rows for today: want 1 (idempotent upsert), got %d", runs)
	}
	if nonTrusted != 2 || trusted != 7 {
		t.Fatalf("ledger counts: non_trusted=%d trusted=%d, want 2/7", nonTrusted, trusted)
	}

	// ══ M2-T4 四条告警在夹具上的行为验证（该火的火 / 该哑的哑） ═══════════
	t.Run("supplier_price_above_market", func(t *testing.T) {
		fired := runOneCheck(t, ctx, pool, "supplier_price_above_market")
		if len(fired) != 1 {
			t.Fatalf("fired rows: want 1 (minimax 3/12: 5/15=0.333), got %d: %v", len(fired), fired)
		}
		for k, v := range fired {
			if !strings.Contains(v, "market_position=0.3333") {
				t.Fatalf("fired row detail lacks market_position=0.3333: %q -> %s", k, v)
			}
			// fable 100/400（60/500=0.12 同样 <1/1.5）必须被 count>=3 门挡住
			if strings.Contains(v, "claude-fable-5") {
				t.Fatalf("fable (floor n=1) must be excluded by the thin-coverage gate: %v", fired)
			}
		}
	})

	t.Run("market_ref_thin_coverage", func(t *testing.T) {
		thin := runOneCheck(t, ctx, pool, "market_ref_thin_coverage")
		// 阶段 2 后仍 n=1 的海外地板：opus / fable / drift 三只
		if len(thin) != 3 {
			t.Fatalf("thin rows: want 3, got %d: %v", len(thin), thin)
		}
		for _, want := range []string{"claude-opus-4-8:overseas", "claude-fable-5:overseas", "test-drift-model:overseas"} {
			if _, ok := thin[want]; !ok {
				t.Fatalf("thin coverage missing %q: %v", want, thin)
			}
		}
	})

	t.Run("price_confidence_degraded", func(t *testing.T) {
		// 台账当日 non_trusted=2；昨日 =1 ⇒ 上升 ⇒ 火。
		if _, err := pool.Exec(ctx, `INSERT INTO public.market_scoring_runs
			(run_date, bindings, trusted_count, non_trusted_count) VALUES (current_date - 1, 9, 8, 1)`); err != nil {
			t.Fatalf("seed yesterday ledger: %v", err)
		}
		if got := runOneCheck(t, ctx, pool, "price_confidence_degraded"); len(got) != 1 {
			t.Fatalf("degraded (2 > 1) must fire once, got %v", got)
		}
		// 昨日 =5 ⇒ 下降 ⇒ 哑。
		if _, err := pool.Exec(ctx, `UPDATE public.market_scoring_runs SET non_trusted_count = 5
			WHERE run_date = current_date - 1`); err != nil {
			t.Fatalf("flip yesterday ledger: %v", err)
		}
		if got := runOneCheck(t, ctx, pool, "price_confidence_degraded"); len(got) != 0 {
			t.Fatalf("degraded (2 <= 5) must stay silent, got %v", got)
		}
	})

	t.Run("platform_region_unmapped", func(t *testing.T) {
		if _, err := pool.Exec(ctx, `INSERT INTO public.market_price_reference_sync
			(run_date, source_platforms, source_rows, landed_rows, rejected_rows, detail)
			VALUES (current_date, 25, 1067, 1065, 2,
			        '{"a17_unmapped_rows": 2, "a17_unmapped_by_platform": {"test-platform": 2}}'::jsonb)`); err != nil {
			t.Fatalf("seed sync ledger: %v", err)
		}
		got := runOneCheck(t, ctx, pool, "platform_region_unmapped")
		if len(got) != 1 {
			t.Fatalf("unmapped must fire per-platform once, got %v", got)
		}
		for k, v := range got {
			if !strings.Contains(k, "test-platform") || !strings.Contains(v, "unmapped=2") {
				t.Fatalf("unmapped row shape wrong: %q -> %s", k, v)
			}
		}
	})

	// ══ 幂等重放：第三遍 ⇒ 行数不变 + 值收敛 ══════════════════════════════
	stats2, err := w.ScoreNow(ctx)
	if err != nil {
		t.Fatalf("ScoreNow replay: %v", err)
	}
	if stats2.RowsWritten != 9 || stats2.RowsDeleted != 0 {
		t.Fatalf("replay stats: %+v (want 9 written / 0 deleted)", stats2)
	}
	var cnt int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.supplier_market_position`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 9 {
		t.Fatalf("replay row count: want 9, got %d", cnt)
	}
	expectF64(t, "replay C1", q(2, "glm-5.1").c1, scoreF64(0.9554), 1e-4)

	// ══ 过期行清理：删 fable 两条绑定 ⇒ 重跑 ⇒ 两行被删 ═══════════════════
	if _, err := pool.Exec(ctx, `DELETE FROM public.credential_model_bindings
		WHERE provider_model_id = (SELECT id FROM public.provider_models WHERE raw_model_name='claude-fable-5')`); err != nil {
		t.Fatalf("delete binding: %v", err)
	}
	stats3, err := w.ScoreNow(ctx)
	if err != nil {
		t.Fatalf("ScoreNow after binding removal: %v", err)
	}
	if stats3.RowsDeleted != 2 || stats3.RowsWritten != 7 {
		t.Fatalf("cleanup stats: %+v (want 7 written / 2 deleted)", stats3)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.supplier_market_position`).Scan(&cnt); err != nil {
		t.Fatalf("count: %v", err)
	}
	if cnt != 7 {
		t.Fatalf("post-cleanup row count: want 7, got %d", cnt)
	}
}
