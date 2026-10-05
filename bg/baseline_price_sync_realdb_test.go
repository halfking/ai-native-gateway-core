package bg

// `SyncBaselinePricesToDB` 的真库判据 —— 目标第二半「对各模型的**标准价格进行
// 设置**，作为模型基准价」的**落库**那一半。
//
// # 为什么需要这一批
//
// `bg/pricing_baseline_sync_test.go` 有 7 条判据，但 **`grep -c TEST_DATABASE_URL`
// = 0** —— 全部是纯单元测试（validate / verdict / 漂移容差 / kill switch）。
// `bg/pricing_baseline_live_test.go` 同样 = 0。
//
// ⇒ `SyncBaselinePricesToDB` 里那条 `UPDATE models_canonical … WHERE
// canonical_name = $1`（连同它的 9 个出处列与 `RowsAffected()==0` 分支）
// **从未在真库上执行过一次**。
//
// 而它是「基准价」这个概念**唯一**的落点：SSOT 是仓内 JSON，要变成
// `models_canonical.baseline_*` 那 9 列并让 826 的偏差视图能算倍率，中间只有这一步。
// 与上一轮 `dueTargets` / `rollupVerdict` 同一族「判据全绿、核心动作没跑过」。
//
// # 承重的是什么
//
// ① **出处必须落库**（vendor / source / source_url / fetched_at）。基准价的全部
//    价值在于「它出自原厂哪一页、什么时候取的」；只落数字就退化成一个人工填的
//    数字，而 832/827 那几道 staleness 检查全都靠 `fetched_at`。
// ② **一条缺失不该中断其余**。清单覆盖的原厂模型可能一个都没被供应商接入
//    （`RowsAffected()==0` 分支）；如果那会让整轮 sync 失败，那么
//    「名单里有一个模型没接入」就会阻塞其余所有模型的基准价设置。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// baselineFixture 搭出「能应用**真** 826」的最小生产形态：models_canonical 走仓
// 的逐对象 SSOT，826 的偏差视图要读的四张表作为脚手架在场，然后原样应用 826。
//
// # 为什么抽成共享 helper
//
// 这个夹具原先在本文件里抄了两份；第三份（币种那条）**只抄了 models_canonical**
// 就去 apply 826 ⇒ 撞 `42P01 relation "credential_model_bindings" does not exist`。
// 也就是说「漏抄脚手架」的失效形态是**真报错**，这是好的；但它证明**抄**本身
// 就是这个文件的复发面 —— 826 以后再加依赖，谁记得回头改第三份？
//
// ⇒ 依赖清单集中在这里一处，且**表名与 826 的 FROM 列表一一对应**。
// 加列、加表时只改这里。
func baselineFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	// ★ 安全闸清单必须**穷举**这张夹具建出来的每一样东西：漏一张的话，下一轮
	// `CREATE TABLE` 会撞已存在而失败，或者（若改用 IF NOT EXISTS）静默沿用
	// **旧形状**，测试照样绿。
	tables := []string{"model_baseline_price_reconciliation", "credential_model_bindings",
		"provider_models", "credentials", "providers", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 清理用 `t.Cleanup` 而**不是 `defer`**：`defer` 绑的是**本函数**返回，
	// 而本函数在夹具建好的那一刻就返回了 ⇒ 表格当场被删，后面全报 42P01
	// （踩过一次：三条判据同时 `relation "models_canonical" does not exist`）。
	// 形态是「测试体没建表」而不是「断言红」，很容易误判成环境问题。
	//
	// ★ 注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到「表已存在」
	// 直接 SKIP，人看到的是「通过」，实际一次都没跑。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.WithoutCancel(ctx), `
			DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;
			DROP TABLE IF EXISTS public.model_baseline_price_reconciliation;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	})

	// models_canonical 走仓的逐对象 SSOT（真表 id 上无主键/无唯一约束）。
	if _, err := pool.Exec(ctx, "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n"+
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)+`
-- 826 还会建 v_supplier_price_vs_baseline，它读下面这四张表 ⇒ 要把**真** 826
-- 整个应用上去，这几张必须在场。（它们是脚手架，不参与被测对象。）
CREATE TABLE public.providers (
    id bigserial PRIMARY KEY, code text NOT NULL, display_name text NOT NULL);
CREATE TABLE public.credentials (
    id bigserial PRIMARY KEY, provider_id bigint REFERENCES public.providers(id));
CREATE TABLE public.provider_models (
    id bigserial PRIMARY KEY, provider_id bigint NOT NULL,
    raw_model_name text NOT NULL, canonical_id bigint, canonical_raw_name text NOT NULL);
CREATE TABLE public.credential_model_bindings (
    id bigserial PRIMARY KEY, credential_id bigint NOT NULL, provider_model_id bigint NOT NULL,
    unit_price_in_per_1m numeric, unit_price_out_per_1m numeric,
    cache_read_price_per_1m numeric, cache_write_price_per_1m numeric,
    currency text DEFAULT 'USD', billing_mode text DEFAULT 'per_token',
    pricing_source text, pricing_updated_at timestamptz,
    UNIQUE (credential_id, provider_model_id));`); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	// 应用**真** 826：基准价列与那个 CHECK 都由它建，不手抄。
	mig, err := os.ReadFile("../sql/migrations/startup/826_model_baseline_price.sql")
	if err != nil {
		t.Fatalf("read 826: %v", err)
	}
	if _, err := pool.Exec(ctx, string(mig)); err != nil {
		t.Fatalf("apply 826: %v", err)
	}

	// ★ 量具自证：建完立刻确认**真**看得见这些对象。
	// 少了这一步，「夹具没建出来」会一路伪装成「断言红」（relation does not
	// exist 出现在测试体第一行，而不是在真正的断言上），人排查的方向是错的。
	// 视图是 826 建的，必须在位 —— 本文件至少一条判据要读它的 detail。
	for _, obj := range []string{
		"models_canonical",
		"model_baseline_price_reconciliation",
		"v_supplier_price_vs_baseline",
	} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace ns ON ns.oid = c.relnamespace
			WHERE ns.nspname='public' AND c.relname = $1`, obj).Scan(&n); err != nil {
			t.Fatalf("self-check %s: %v", obj, err)
		}
		if n != 1 {
			t.Fatalf("fixture self-check: public.%s is not in the database after building the "+
				"fixture (found %d) — every assertion below would fail with 42P01 for the wrong reason",
				obj, n)
		}
	}
}

func TestSyncBaselinePricesWritesProvenanceAndSkipsAbsentModels(t *testing.T) {
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
	// ★ 必须用 `t.Cleanup` 而不是 `defer`：cleanup 是 **LIFO**，且 `defer`
	// 在测试函数返回时就跑了 —— 那早于所有 `t.Cleanup`。若这里用 defer，
	// 池在夹具的 DROP 之前就关掉，DROP 静默失败（err 被丢）⇒ 残桩留库
	// ⇒ 下一轮安全闸看到「表已存在」直接 SKIP。
	// 先注册本行、后注册夹具的 DROP ⇒ LIFO 让 DROP 先跑、池后关。
	t.Cleanup(pool.Close)

	baselineFixture(t, ctx, pool)

	// 库里只有 m-in；清单里另有 m-gone（**不存在**于 models_canonical）。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name) VALUES ('m-in');`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	// 拿一个指针值：BaselinePrice 的价都是 *float64，而 nil 与 0 在成本核算里
	// 是两回事（826 的注释专门解释了「CHECK 约束保证 0 不会被当成缺省」）。
	f := func(v float64) *float64 { return &v }
	catalog := map[string]BaselinePrice{
		"m-in": {
			InputPer1M: f(3.00), OutputPer1M: f(15.00),
			CacheReadPer1M: f(0.30), CacheWritePer1M: f(3.75),
			Currency:  "USD",
			Vendor:    "anthropic",
			Source:    "pricing page",
			SourceURL: "https://www.anthropic.com/pricing",
			FetchedAt: "2026-10-01T00:00:00Z",
		},
		// 这个模型在 models_canonical 里不存在 ⇒ RowsAffected()==0 分支。
		"m-gone": {
			InputPer1M: f(1.00), OutputPer1M: f(2.00),
			Currency:  "USD",
			Vendor:    "xai",
			Source:    "pricing page",
			SourceURL: "https://x.ai/pricing",
			FetchedAt: "2026-10-01T00:00:00Z",
		},
	}

	// **承重之二**：缺失的那个模型**不得**让整轮失败。
	written, err := SyncBaselinePricesToDB(ctx, pool, catalog)
	if err != nil {
		t.Fatalf("SyncBaselinePricesToDB returned an error for a catalog entry with no matching "+
			"models_canonical row: %v — that aborts the whole sync and blocks baseline prices for "+
			"every other model in the catalog", err)
	}
	if written != 1 {
		t.Errorf("wrote=%d, want 1 — only m-in exists in models_canonical; the missing model must "+
			"be skipped, not counted as written", written)
	}

	// **承重之一**：数字**与出处**都必须落库。
	var cur, vendor, source, sourceURL string
	var inP, outP, crP, cwP *float64
	var fetched *time.Time
	if err := pool.QueryRow(ctx, `SELECT baseline_price_currency, baseline_input_price_per_1m,
		baseline_output_price_per_1m, baseline_cache_read_price_per_1m,
		baseline_cache_write_price_per_1m, baseline_price_vendor, baseline_price_source,
		baseline_price_source_url, baseline_price_fetched_at
		  FROM public.models_canonical WHERE canonical_name='m-in'`).
		Scan(&cur, &inP, &outP, &crP, &cwP, &vendor, &source, &sourceURL, &fetched); err != nil {
		t.Fatalf("read m-in: %v", err)
	}
	if inP == nil || *inP != 3.00 || outP == nil || *outP != 15.00 ||
		crP == nil || *crP != 0.30 || cwP == nil || *cwP != 3.75 {
		t.Errorf("prices landed as in=%v out=%v cache_read=%v cache_write=%v, want 3/15/0.3/3.75",
			inP, outP, crP, cwP)
	}
	if cur != "USD" {
		t.Errorf("currency=%q, want USD", cur)
	}
	// 出处三件套 + 时间戳：基准价的全部价值在这里，缺任何一样它就退化成
	// 一个无法追溯来源的数字，而 827/832 的陈旧性检查全都靠 fetched_at。
	if vendor != "anthropic" || source != "pricing page" ||
		sourceURL != "https://www.anthropic.com/pricing" {
		t.Errorf("provenance landed as vendor=%q source=%q url=%q, want anthropic / pricing page / "+
			"https://www.anthropic.com/pricing — a baseline price without provenance is an "+
			"untraceable number", vendor, source, sourceURL)
	}
	if fetched == nil {
		t.Error("baseline_price_fetched_at is NULL — the reconciliation's staleness verdicts " +
			"(stale_source) and the 30-day re-check window both key on this column")
	}

	// 缺失的那个模型**不得**留下任何痕迹（不能凭空造一行）。
	var ghosts int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.models_canonical
		WHERE canonical_name = 'm-gone'`).Scan(&ghosts); err != nil {
		t.Fatalf("count m-gone: %v", err)
	}
	if ghosts != 0 {
		t.Errorf("the sync created %d row(s) for a model that does not exist in models_canonical — "+
			"the baseline price catalog must never invent canonical models", ghosts)
	}
}

// TestRecordReconciliationKeepsAbsentAndZeroApart 是**对账台账**那条写路径的判据。
//
// 上一条验的是「基准价写进 models_canonical」；这条验的是「对账结论写进
// model_baseline_price_reconciliation」—— 826 注释里说的「每次对账一行，把清单
// 里的基准价与观察到的价并排放进去」。`RecordReconciliation` 同样此前零真库覆盖
// （`bg/pricing_baseline_sync_test.go` 的 `TEST_DATABASE_URL` = 0）。
//
// 承重的是 **NULL / 0 / 空串三者的分离** —— 这正是 826 自己强调的纪律：
//
//	这两者的区别在成本核算里是本质的，CHECK 约束保证 0 不会被当成缺省。
//
// 而 `RecordReconciliation` 里那一串 `if p.Currency != "" { ssotCurrency = &p.Currency }`
// 就是在维持它：空串 ⇒ **保持 nil**，而不是把空串塞进库里。下游 826 的偏差视图用
// `COALESCE(cmb.currency,'USD')`，空串会让它与 NULL 分道扬镳（`” IS DISTINCT FROM
// 'USD'`）⇒ 币种不可比的模型会被算成「可比」。
func TestRecordReconciliationKeepsAbsentAndZeroApart(t *testing.T) {
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
	// ★ 必须用 `t.Cleanup` 而不是 `defer`：cleanup 是 **LIFO**，且 `defer`
	// 在测试函数返回时就跑了 —— 那早于所有 `t.Cleanup`。若这里用 defer，
	// 池在夹具的 DROP 之前就关掉，DROP 静默失败（err 被丢）⇒ 残桩留库
	// ⇒ 下一轮安全闸看到「表已存在」直接 SKIP。
	// 先注册本行、后注册夹具的 DROP ⇒ LIFO 让 DROP 先跑、池后关。
	t.Cleanup(pool.Close)

	baselineFixture(t, ctx, pool)

	f := func(v float64) *float64 { return &v }
	zero := 0.0
	drift := 20.0
	now := time.Now().UTC()

	// 三行，覆盖三种「缺失」形态：
	//   1) full   —— 两侧都有值，判词 drift，漂移 20%
	//   2) empty  —— SSOT 的 currency / source_url 是**空串** ⇒ 必须落成 NULL
	//   3) none   —— 观察侧整个为 nil（没观察到这个模型）⇒ observed_* 全 NULL
	rows := []struct {
		model        string
		verdict      string
		ssotCur      string
		ssotURL      string
		obs          *PriceObservation
		inDrift      *float64
		wantObsIsNil bool
	}{
		{"m-full", "drift", "USD", "https://www.anthropic.com/pricing",
			&PriceObservation{InputPer1M: f(3.60), OutputPer1M: f(18.00), Currency: "USD",
				Source: "models.dev", SourceURL: "https://models.dev/api.json", ObservedAt: now},
			&drift, false},
		{"m-empty", "not_comparable", "", "",
			&PriceObservation{InputPer1M: f(1.0), OutputPer1M: f(2.0), Currency: "",
				Source: "", SourceURL: "", ObservedAt: now},
			nil, false},
		{"m-none", "missing", "USD", "https://x.ai/pricing", nil, nil, true},
	}
	for _, r := range rows {
		rec := Reconciliation{
			Model: r.model, Verdict: r.verdict,
			InputDriftPct: r.inDrift, OutputDriftPct: r.inDrift,
			SSOTPrice: &BaselinePrice{
				InputPer1M: f(3.00), OutputPer1M: f(15.00),
				// m-zero 的清单侧价是 0：0 与「没有价」必须可区分。
				Currency: r.ssotCur, Vendor: "v", Source: "s",
				SourceURL: r.ssotURL, FetchedAt: "2026-10-01T00:00:00Z",
			},
			Observation: r.obs, SSOTFetchedAt: &now,
			Detail: map[string]any{"case": r.model},
		}
		if r.model == "m-full" {
			rec.SSOTPrice.InputPer1M = f(3.00)
		}
		if err := RecordReconciliation(ctx, pool, rec); err != nil {
			t.Fatalf("RecordReconciliation(%s): %v", r.model, err)
		}
	}
	_ = zero

	var landed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.model_baseline_price_reconciliation`).Scan(&landed); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if landed != len(rows) {
		t.Fatalf("ledger has %d row(s), want %d", landed, len(rows))
	}

	// 承重一：空串必须落成 **NULL**，不是 ''。
	// 下游 826 的偏差视图用 COALESCE(currency,'USD')，而 '' IS DISTINCT FROM 'USD'
	// ⇒ 空串会让「币种未知」被算成「币种与 USD 不同」，即不可比 —— 那是**错**的：
	// 未知就是未知，不该被当成「另一种币种」。
	var ssotCur, ssotURL, obsCur *string
	if err := pool.QueryRow(ctx, `SELECT ssot_currency, ssot_source_url, observed_currency
		FROM public.model_baseline_price_reconciliation WHERE canonical_name='m-empty'`).
		Scan(&ssotCur, &ssotURL, &obsCur); err != nil {
		t.Fatalf("read m-empty: %v", err)
	}
	if ssotCur != nil {
		t.Errorf("m-empty ssot_currency=%q, want NULL — an empty Currency must stay absent; "+
			"downstream COALESCE(currency,'USD') would treat '' as a different currency and "+
			"report the model as not-comparable instead of unknown", *ssotCur)
	}
	if ssotURL != nil {
		t.Errorf("m-empty ssot_source_url=%q, want NULL for the same reason", *ssotURL)
	}
	if obsCur != nil {
		t.Errorf("m-empty observed_currency=%q, want NULL", *obsCur)
	}

	// 承重二：观察侧整个为 nil ⇒ observed_* 必须是 NULL，且判词落成 missing。
	var obsIn, obsOut, obsSrc, obsURL *string
	var verdict string
	if err := pool.QueryRow(ctx, `SELECT observed_input_price_per_1m::text,
		observed_output_price_per_1m::text, observed_source, observed_source_url, verdict
		FROM public.model_baseline_price_reconciliation WHERE canonical_name='m-none'`).
		Scan(&obsIn, &obsOut, &obsSrc, &obsURL, &verdict); err != nil {
		t.Fatalf("read m-none: %v", err)
	}
	if obsIn != nil || obsOut != nil || obsSrc != nil || obsURL != nil {
		t.Errorf("m-none observed_* landed as in=%v out=%v src=%v url=%v, want all NULL — "+
			"a model that was not observed must not carry fabricated observation values",
			obsIn, obsOut, obsSrc, obsURL)
	}
	if verdict != "missing" {
		t.Errorf("m-none verdict=%q, want missing", verdict)
	}

	// 承重三：drift 判词的漂移值必须落库（漂移率是「准确控制成本」的核心读数）。
	var inDrift *float64
	if err := pool.QueryRow(ctx, `SELECT input_drift_pct
		FROM public.model_baseline_price_reconciliation WHERE canonical_name='m-full'`).
		Scan(&inDrift); err != nil {
		t.Fatalf("read m-full drift: %v", err)
	}
	if inDrift == nil || *inDrift != 20.0 {
		t.Errorf("m-full input_drift_pct=%v, want 20 — the drift percentage is the number this "+
			"whole ledger exists to produce", inDrift)
	}

	// 承重四：非法判词必须被表自己的 CHECK 挡住（826 的 verdict CHECK 承重）。
	if err := RecordReconciliation(ctx, pool, Reconciliation{
		Model: "m-bogus", Verdict: "totally_made_up",
		SSOTPrice: &BaselinePrice{InputPer1M: f(1), OutputPer1M: f(2), Currency: "USD",
			Vendor: "v", Source: "s", SourceURL: "https://x", FetchedAt: "2026-10-01T00:00:00Z"},
	}); err == nil {
		t.Error("an out-of-enum verdict was accepted — the table's verdict CHECK is the last line " +
			"of defence and it did not fire")
	}
}

// TestSyncRefusesAPriceWhoseCurrencyIsUnknown 钉「币种未知必须被拒」。
//
// # 缺口（2026-10-05）
//
// `validate` 原本要求 `source_url`（理由：「没有出处的价格不可审计」），却**不要求**
// `currency`，而 `SyncBaselinePricesToDB` 里有一句
//
//	currency := p.Currency; if strings.TrimSpace(currency) == "" { currency = "USD" }
//
// 那是把「未知」变成一个关于钱的**断言**，而这条路径真实可达：提取器的
// `currencyOf` 在价格行里找不到 $/€/£/¥ 时返回 ""，提案会带空币种，人照抄进
// SSOT 就中招。
//
// 后果两处且都静默：826 视图按 `COALESCE(baseline_price_currency,'USD')` 比币种
// ⇒ 真值是 EUR 却被当成 USD（要么永远算不出偏差，要么与同为 "USD" 的供应商价
// 算出**看起来正常的错倍率**）；台账记下的权威值本身就是错的。
//
// 承重是**双向**：缺币种必须**被拒且一个字都不能落库**；给了币种必须**原样**写入。
// 只测单向的话，把 validate 的新校验删掉也能过。
func TestSyncRefusesAPriceWhoseCurrencyIsUnknown(t *testing.T) {
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
	// ★ 必须用 `t.Cleanup` 而不是 `defer`：cleanup 是 **LIFO**，且 `defer`
	// 在测试函数返回时就跑了 —— 那早于所有 `t.Cleanup`。若这里用 defer，
	// 池在夹具的 DROP 之前就关掉，DROP 静默失败（err 被丢）⇒ 残桩留库
	// ⇒ 下一轮安全闸看到「表已存在」直接 SKIP。
	// 先注册本行、后注册夹具的 DROP ⇒ LIFO 让 DROP 先跑、池后关。
	t.Cleanup(pool.Close)

	baselineFixture(t, ctx, pool)

	if _, err := pool.Exec(ctx, `INSERT INTO public.models_canonical (canonical_name)
		VALUES ('m-nocurrency'), ('m-eur')`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	f := func(v float64) *float64 { return &v }

	// 承重之一：缺币种 ⇒ 报错，且**一个字都不能落库**。
	written, err := SyncBaselinePricesToDB(ctx, pool, map[string]BaselinePrice{
		"m-nocurrency": {
			InputPer1M: f(5.00), OutputPer1M: f(25.00), // 刻意不填 Currency
			SourceURL: "https://vendor.example/pricing", FetchedAt: now,
		},
	})
	if err == nil {
		t.Error("a baseline price with no currency was accepted. Defaulting it to USD states a fact " +
			"the vendor's page never said, and it lands in the authoritative column")
	} else if !strings.Contains(err.Error(), "currency") {
		t.Errorf("the rejection does not mention currency: %v", err)
	}
	if written != 0 {
		t.Errorf("written = %d, want 0", written)
	}
	var landed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.models_canonical
		WHERE canonical_name='m-nocurrency' AND baseline_price_currency IS NOT NULL`).Scan(&landed); err != nil {
		t.Fatalf("probe: %v", err)
	}
	if landed != 0 {
		t.Errorf("%d row(s) got a baseline_price_currency despite the rejection — the write must "+
			"not happen at all, not even partially", landed)
	}

	// 承重之二：给了币种必须**原样**写入，不能被换成 USD。
	if _, err := SyncBaselinePricesToDB(ctx, pool, map[string]BaselinePrice{
		"m-eur": {
			InputPer1M: f(5.00), OutputPer1M: f(25.00), Currency: "EUR",
			SourceURL: "https://vendor.example/pricing", FetchedAt: now,
		},
	}); err != nil {
		t.Fatalf("a price with an explicit EUR currency was rejected: %v", err)
	}
	var cur string
	var inP, outP float64
	if err := pool.QueryRow(ctx, `SELECT baseline_price_currency,
		baseline_input_price_per_1m, baseline_output_price_per_1m
		FROM public.models_canonical WHERE canonical_name='m-eur'`).
		Scan(&cur, &inP, &outP); err != nil {
		t.Fatalf("read the written row: %v", err)
	}
	if cur != "EUR" {
		t.Errorf("baseline_price_currency = %q, want \"EUR\" — the currency the source stated must "+
			"survive verbatim; anything else is a fabricated fact in the authoritative table", cur)
	}
	if inP != 5.00 || outP != 25.00 {
		t.Errorf("prices landed as %v/%v, want 5/25", inP, outP)
	}
}
