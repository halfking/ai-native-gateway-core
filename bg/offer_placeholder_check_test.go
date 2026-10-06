package bg

// 第 17 条 `offer_price_looks_like_placeholder` 的判据。
//
// # 缺陷本体（2026-10-06 真库实测）
//
// 八个 offer 的 `unit_price_in_per_1m` 与 `unit_price_out_per_1m` **都是 0.1**，
// 另有一个 0.2；跨 5 个模型、4 个供应商，`pricing_source` 是
// manual / inherited / imported。判别依据不是「0.1 太小」，而是
// **in 与 out 精确相等** —— 真实 token 定价几乎不会输入输出同价到分。
//
// 而它们**已经在产生账面成本**：近 30 天 4,522 条请求的成本由它们算出（$0.04）。
// ⇒ 这是「**看起来记了价**」的那一类，比没价更坏：没价触发告警，假价安静进账。
//
// # 三条判据各自证明什么
//
//	A 种下形似占位的行 ⇒ 必须报，且报法要区分两族
//	B 零价那族（in==out==0）⇒ 必须**不**被当缺陷（它们诚实地落 NULL）
//	C 删掉形似占位的行 ⇒ 必须不报（证明不是恒真）
//
// # 为什么用夹具表而不是真库
//
// 55432 里 `model_offers` / `models_canonical` 都不存在，而**造一张假表**会带来
// 「夹具形状与生产不一致」这个正是本项目反复吃亏的问题。⇒ 夹具**照抄生产的列定义**，
// 并由 `TestPlaceholderFixtureMatchesProductionColumnTypes` 核对过一遍。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func placeholderCheckSQL(t *testing.T) string {
	t.Helper()
	for _, d := range AllHealthChecks() {
		if d.CheckID == "offer_price_looks_like_placeholder" {
			return d.Query
		}
	}
	t.Fatalf("check offer_price_looks_like_placeholder not found in AllHealthChecks() "+
		"(%d checks registered); this test cannot verify what it claims to", len(AllHealthChecks()))
	return ""
}

// placeholderPool 连夹具库并确保 model_offers 存在。
//
// ★ 列定义**照抄生产**（127.0.0.1:5432 实测 information_schema）：
// available boolean / unit_price_in_per_1m numeric / unit_price_out_per_1m numeric，
// 其余列本条 SQL 不碰，但为了让「照抄」这句话能被核对，带上并断言类型。
// 只建**本条 SQL 引用到的**列 —— 判据证明的是筛选与措辞，不是这张表的其余 45 列。
func placeholderPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过占位价夹具回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)

	// ★ 2026-10-06 补：**表已存在 ⇒ 跳过**，绝不接管。
	//
	// 原先是 `CREATE TABLE IF NOT EXISTS` + `t.Cleanup(DROP TABLE ... CASCADE)`，
	// 那个组合只在「这张表本来不存在」的库上安全（夹具库 55432 就是这种）。
	// 在**已建好 schema 的库**上（集成门 shape=installer）它有两个真问题，
	// 都不是「测试红」那么简单：
	//
	//   1) 破坏性：`DROP TABLE IF EXISTS ... CASCADE` 会把那张真表连同依赖
	//      一起删掉。门禁库是满的，那一行等于在删生产形状。
	//   2) 不兼容：真 model_offers 带 provider_id / credential_id 的外键与
	//      NOT NULL，而夹具是硬编码 canonical_id=1 / credential_id=1 的最小表。
	//      集成门实测（2026-10-06）：
	//        seed offer (0/0): ERROR: null value in column "provider_id" of
	//        relation "provider_models" violates not-null constraint (23502)
	//
	// 同包的 TestBaselinePriceChecksReportNonEmptyRows 早就是这个形状
	// （`if existing > 0 { t.Skipf }`），本文件漏了。
	// 守卫与清理都注册在**建表之前**：建到一半失败会留残桩，下一轮看到
	// 「表已存在」直接 SKIP，人看到的是「通过」，实际一次都没跑。
	var preExists bool
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.model_offers') IS NOT NULL`).
		Scan(&preExists); err != nil {
		t.Fatalf("probe for a pre-existing model_offers: %v", err)
	}
	if preExists {
		t.Skipf("public.model_offers already exists in %s — this criterion builds a minimal "+
			"fixture table and drops it in cleanup, so it must not run against a database that "+
			"already has the real one", dsn)
	}

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS public.model_offers (
		    available boolean,
		    billing_mode text,
		    canonical_id bigint,
		    credential_id bigint,
		    pricing_source text,
		    unit_price_in_per_1m numeric,
		    unit_price_out_per_1m numeric
		)`); err != nil {
		t.Fatalf("create fixture model_offers: %v", err)
	}
	// 夹具不得留痕。t.Cleanup 注册在**建表之前**是本项目的纪律，
	// 这里反过来：清理必须在建表之后注册，才不会漏。
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TABLE IF EXISTS public.model_offers CASCADE`)
	})
	return pool
}

// runPlaceholderCheck 跑本条检查的 SQL，回读 (告警条数, 报法文本)。
func runPlaceholderCheck(t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, placeholderCheckSQL(t))
	if err != nil {
		t.Fatalf("run placeholder check: %v", err)
	}
	defer rows.Close()
	n := 0
	var detail string
	for rows.Next() {
		n++
		var key, title, msg string
		if err := rows.Scan(&key, &title, &msg); err != nil {
			t.Fatalf("scan: %v", err)
		}
		detail = msg
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return n, detail
}

// seedOffer 种一行。in/out 都传同一个数就是「形似占位」。
func seedOffer(t *testing.T, pool *pgxpool.Pool, pIn, pOut float64, avail bool, source string) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO public.model_offers
		   (available, billing_mode, canonical_id, credential_id, pricing_source,
		    unit_price_in_per_1m, unit_price_out_per_1m)
		 VALUES ($1, 'token_plan', 1, 1, $2, $3, $4)`,
		avail, source, pIn, pOut)
	if err != nil {
		t.Fatalf("seed offer (%v/%v): %v", pIn, pOut, err)
	}
}

// A + B + C 一条测试跑三臂，共享夹具与清理。
func TestPlaceholderShapedOfferIsReportedAndZeroValuedOnesAreNotDefects(t *testing.T) {
	pool := placeholderPool(t)
	ctx := context.Background()
	// 臂 0：空表 ⇒ 必须不报（恒真检测）
	if n, _ := runPlaceholderCheck(t, pool); n != 0 {
		t.Fatalf("an empty table must produce no alert, got %d row(s) — the check fires unconditionally", n)
	}

	// 臂 B：只有零价那族 ⇒ 必须不报，且**不得**把它们说成 priced at 0
	seedOffer(t, pool, 0, 0, true, "manual")
	seedOffer(t, pool, 0, 0, true, "inherited")
	if n, msg := runPlaceholderCheck(t, pool); n != 0 {
		t.Errorf("zero/zero offers must NOT be reported: CalcCost returns nil for them, so "+
			"they leave cost_usd NULL and are not defects. Got %d alert(s):\n%s", n, msg)
	}

	// 臂 A：种一行形似占位的（正数、in==out）⇒ 必须报
	seedOffer(t, pool, 0.1, 0.1, true, "manual")
	n, msg := runPlaceholderCheck(t, pool)
	if n != 1 {
		t.Fatalf("a positive in==out offer must produce exactly one alert, got %d — "+
			"the placeholder shape is not being detected (message: %q)", n, msg)
	}

	// 报法必须点名那两族，且措辞不许把零价说成缺陷
	for _, want := range []string{"candidate", "manual", "not defects", "opposite fixes"} {
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(want)) {
			t.Errorf("alert wording must contain %q so the reader knows this is a candidate list "+
				"and that the zero-valued population needs the opposite fix. Got:\n%s", want, msg)
		}
	}
	// 上一条断言的 forbidden phrasing：像第 14 条的措辞门一样，把「0 价」说成
	// 「被定价成 0」会让人去 `WHERE unit_price_in_per_1m = 0` 找**零价那族**，
	// 而它与本条盯的不是同一件事。
	for _, forbidden := range []string{"priced at 0", "are priced at zero", "quoted 0"} {
		if strings.Contains(strings.ToLower(msg), forbidden) {
			t.Errorf("alert wording must not say %q: that describes the zero-valued population, "+
				"which this check deliberately excludes (supplier_price_missing_from_cost owns it):\n%s",
				forbidden, msg)
		}
	}

	// 臂 C：删掉形似占位的那行 ⇒ 必须回到不报（证明不是恒真）
	if _, err := pool.Exec(ctx,
		`DELETE FROM public.model_offers WHERE unit_price_in_per_1m = 0.1`); err != nil {
		t.Fatalf("delete shaped offer: %v", err)
	}
	if n, _ := runPlaceholderCheck(t, pool); n != 0 {
		t.Errorf("after removing the shaped offer the check must fall silent, got %d alert(s) "+
			"— it is reporting on the wrong population", n)
	}
}

// 不对称价（真的价格）必须**不**报。
//
// 这一条防的是「把 in==out 当成充分条件」的过宽实现：真实 token 定价天然不对称，
// 而候选形态只该覆盖**对称且为正**那族。
func TestAsymmetricRealPriceIsNotReportedAsPlaceholder(t *testing.T) {
	pool := placeholderPool(t)
	seedOffer(t, pool, 5.00, 25.00, true, "manual") // 真的非对称价
	seedOffer(t, pool, 0.30, 1.20, true, "scraped") // 真的非对称价
	if n, msg := runPlaceholderCheck(t, pool); n != 0 {
		t.Errorf("a real asymmetric price is not a placeholder; got %d alert(s):\n%s", n, msg)
	}
}

// 反向：in != out 但**都是正数**，也不能报。
func TestSymmetricButZeroIsNotTheOnlyPositiveEqualCase(t *testing.T) {
	pool := placeholderPool(t)
	// 3.5 == 3.5 仍然是对称 ⇒ 属候选（这条断言的是「>0 而不是 =0.1」）。
	// 它存在的意义是：把实现写成「= 0.1」或「< 1」都会在这里露馅。
	seedOffer(t, pool, 3.5, 3.5, true, "manual")
	n, msg := runPlaceholderCheck(t, pool)
	if n != 1 {
		t.Fatalf("symmetric positive at 3.5 is still the candidate shape and must be reported "+
			"(an implementation hard-coding 0.1 or 0.2 would miss it). got %d: %q", n, msg)
	}
	if !strings.Contains(msg, "3.5") {
		t.Errorf("the alert must name the value it saw, got:\n%s", msg)
	}
}

// 夹具忠实性：列定义必须与生产一致，否则上面三条测的不是被测对象。
func TestPlaceholderFixtureMatchesProductionColumnTypes(t *testing.T) {
	pool := placeholderPool(t)
	rows, err := pool.Query(context.Background(),
		`SELECT column_name, data_type FROM information_schema.columns
		  WHERE table_schema='public' AND table_name='model_offers'
		    AND column_name IN ('available','unit_price_in_per_1m','unit_price_out_per_1m',
		                        'pricing_source','billing_mode','credential_id','canonical_id')`)
	if err != nil {
		t.Fatalf("read fixture columns: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var col, ty string
		if err := rows.Scan(&col, &ty); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[col] = ty
	}
	// 生产实测值（127.0.0.1:5432 information_schema，2026-10-06）
	want := map[string]string{
		"available": "boolean", "unit_price_in_per_1m": "numeric",
		"unit_price_out_per_1m": "numeric", "pricing_source": "text",
		"billing_mode": "text", "credential_id": "bigint", "canonical_id": "bigint",
	}
	for col, ty := range want {
		if got[col] != ty {
			t.Errorf("fixture column %s is %q, production is %q — the fixture must copy the "+
				"production shape or this test measures the fixture, not the check", col, got[col], ty)
		}
	}
}
