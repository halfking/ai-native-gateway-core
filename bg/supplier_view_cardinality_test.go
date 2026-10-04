package bg

// v_supplier_price_vs_baseline 的「一条绑定 ⇒ 一行」判据。
//
// 背景：826 建的偏差视图是「准确控制模型实际成本」这条目标的落点——它把
// 供应商实付价与原厂基准价摆在一起算出倍率。它的 canonical JOIN 原来写成
//
//	LEFT JOIN models_canonical mc
//	       ON mc.id = pm.canonical_id
//	      OR lower(mc.canonical_name) = lower(pm.canonical_raw_name)
//
// 真库实测**一条供应商绑定变成三行**，且同一个价被同时报成「比基准贵 20%」
// 与「比基准便宜 40%」。成因是 OR 两侧各自命中不同的 canonical 行：
// provider_models.canonical_id 与 canonical_raw_name 由不同代码路径写入，
// 不一致完全可能；而 modelname/normalize.go 明确不做 claude-opus-4-8 ↔
// claude-opus-4.8 的跨形态归一，这两种写法能在 models_canonical 里并存。
//
// 这类缺陷有一个很讨厌的性质：**它不会报错、不会空集、倍率算得出来**。
// 一张「多出两行」的偏差报表看起来仍然像报表，只有对账时才暴露。
// 所以判据直接钉住「行数」，而不是钉住某几个倍率值。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；已有这些表时也跳过（它要建表）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// supplierViewFixture 是偏差视图要读的那几张表。
//
// ★ models_canonical **不是手抄的**，而是从仓的逐对象 SSOT 推导
//
//	（internal/schemaobj）。原因与 provider/modality_gate_alignment_test.go
//	同源：手抄的替身当初写了 `id bigserial PRIMARY KEY`，比真表**更宽松**
//	——真表的 id 上没有主键、没有唯一约束、连索引都没有。夹具自己补上了真表
//	缺的那个键，于是「唯一约束缺失」这类缺陷在夹具里永远看不见。
//	同一份推导逻辑两处共用，不写第二遍。
//
// 下面三张表是**种数据的脚手架**，不是 826 的依赖：826 不给它们加外键也不
// 改它们，所以它们与真表的列差异不会掩盖 826 自身的 DDL 缺陷。但「不涉及
// 被测对象」不等于「可以随手写」——见下面 provider_models 上那条注释：
// 脚手架自己的一根外键，曾经把真表缺的唯一键给补上了。
func supplierViewFixture(t *testing.T) string {
	t.Helper()
	return "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		) + `
CREATE TABLE public.providers (
    id bigserial PRIMARY KEY,
    code text NOT NULL,
    display_name text NOT NULL
);
CREATE TABLE public.credentials (
    id bigserial PRIMARY KEY,
    provider_id bigint REFERENCES public.providers(id)
);
CREATE TABLE public.provider_models (
    id bigserial PRIMARY KEY,
    provider_id bigint NOT NULL,
    raw_model_name text NOT NULL,
    -- ★ 这里**故意没有外键**。真 schema 实测：整个库里没有任何一张表外键
    -- 指向 models_canonical（它连主键都没有），所以 provider_models 与
    -- models_canonical 之间是约定而不是约束。
    --
    -- 早先这个夹具写了 canonical_id bigint REFERENCES models_canonical(id)，
    -- 于是它在夹具里**自己造出了真表缺的那个唯一键**——同一处遮蔽，第二个
    -- 位置。加上真表后它立刻以 "there is no unique constraint matching given
    -- keys" 失败。⇒ 「脚手架表不涉及被测对象」这句话本身就是错的：只要
    -- 脚手架上有一根指向真表的外键，它就能把真表的缺陷补掉。
    canonical_id bigint,
    canonical_raw_name text NOT NULL
);
CREATE TABLE public.credential_model_bindings (
    id bigserial PRIMARY KEY,
    credential_id bigint NOT NULL,
    provider_model_id bigint NOT NULL,
    unit_price_in_per_1m numeric,
    unit_price_out_per_1m numeric,
    currency text DEFAULT 'USD',
    billing_mode text DEFAULT 'per_token',
    pricing_source text,
    pricing_updated_at timestamptz
);
`
}

func TestSupplierPriceViewEmitsExactlyOneRowPerBinding(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set — the deviation-view check needs a real database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tables := []string{"credential_model_bindings", "provider_models", "credentials", "providers", "models_canonical"}
	var existing int
	q := `SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name = ANY($1)`
	if err := pool.QueryRow(ctx, q, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 顺序要紧：**先注册清理，再建表**。
	//
	// 建表在前、defer 在后时，任何「建表执行到一半就失败」都会把桩表留在
	// 库里，而下一轮的安全闸看到「表已存在」直接 SKIP —— 人看到的是「测试
	// 通过」，实际是**一次都没跑**。
	//
	// 清理清单必须**穷举** 826 建出来的每一样东西。漏一张的后果不是「库脏了」，
	// 而是：826 用的是 `CREATE TABLE IF NOT EXISTS`，所以那张残留表会在下一轮
	// 让 DDL 静默沿用**旧形状**——测试照样绿，而绿的是一份几百行前的表结构。
	// 这次清库时残留的 public.model_baseline_price_reconciliation 就是这么来的
	// （defer 跑过了，5 张 fixture 表和视图都删干净了，唯独漏了这张）。
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_supplier_price_vs_baseline;
			DROP TABLE IF EXISTS public.model_baseline_price_reconciliation;
			DROP TABLE IF EXISTS public.credential_model_bindings;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.credentials;
			DROP TABLE IF EXISTS public.providers;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, supplierViewFixture(t)); err != nil {
		t.Fatalf("create fixture: %v", err)
	}

	viewSQL, err := os.ReadFile("../sql/migrations/startup/826_model_baseline_price.sql")
	if err != nil {
		t.Fatalf("read 826 migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(viewSQL)); err != nil {
		t.Fatalf("apply 826: %v", err)
	}

	// 同一个模型的两种写法并存 —— normalize.go 不做这层归一，所以可能。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, baseline_price_currency,
		                                    baseline_input_price_per_1m, baseline_output_price_per_1m)
		VALUES ('claude-opus-4-8','USD',5.00,25.00),
		       ('claude-opus-4.8','USD',9.99,99.99),
		       ('claude-sonnet-4-6','USD',3.00,15.00),
		       -- 只差大小写的一对。canonical_name 上的 UNIQUE 是大小写**敏感**的
		       -- （models_canonical_canonical_name_key），所以这两行能并存；
		       -- 而下面按 lower() 匹配时它们会**同时**命中 —— 这是唯一能让
		       -- LIMIT 1 变成承重墙的输入。
		       ('GPT-4o','USD',2.50,10.00),
		       ('gpt-4o','USD',3.75,15.00)`); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.providers (code, display_name) VALUES ('anthropic','Anthropic')`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO public.credentials (provider_id)
		SELECT id FROM public.providers WHERE code='anthropic'`); err != nil {
		t.Fatalf("seed credential: %v", err)
	}
	// 三种绑定：名字干净、canonical_id 与 raw name 指向**不同**的两种写法、
	// 以及 canonical_id 为空只靠名字兜底。
	for _, pm := range []struct {
		raw       string
		canonName string // 空 = canonical_id 留空，只靠名字兜底
		wantID    string // 非空 = canonical_id 指向它
	}{
		{"m-clean", "claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"m-split", "", "claude-opus-4-8"}, // 关键：raw name 写成另一种形态
		{"m-by-name", "claude-opus-4-8", ""},
		// canonical_id 留空 + 只差大小写的 raw name ⇒ 名字那条路命中 2 行
		{"m-case-dup", "gpt-4o", ""},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
			SELECT pr.id, $1, mc.id, $3
			  FROM public.providers pr
			  LEFT JOIN public.models_canonical mc ON mc.canonical_name = $2
			 WHERE pr.code = 'anthropic'`,
			pm.raw, pm.wantID, pm.canonName); err != nil {
			t.Fatalf("seed provider_model %s: %v", pm.raw, err)
		}
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO public.credential_model_bindings
			(credential_id, provider_model_id, unit_price_in_per_1m, unit_price_out_per_1m, currency)
		SELECT c.id, pm.id, 6.00, 30.00, 'USD'
		  FROM public.credentials c, public.provider_models pm`); err != nil {
		t.Fatalf("seed bindings: %v", err)
	}

	var bindings, rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.credential_model_bindings`).Scan(&bindings); err != nil {
		t.Fatalf("count bindings: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.v_supplier_price_vs_baseline`).Scan(&rows); err != nil {
		t.Fatalf("count view rows: %v", err)
	}

	// **量具自证**：视图必须真的看到了这批绑定。种子写错时这个数会是 0，
	// 而 0 == 0 会让下面那条断言恒真。
	if rows == 0 {
		t.Fatalf("the deviation view returned 0 rows for %d seeded bindings — the fixture "+
			"did not reach the view, so every assertion below is vacuous", bindings)
	}
	if rows != bindings {
		var dup string
		_ = pool.QueryRow(ctx, `
			SELECT string_agg(raw_model_name || '×' || n::text, ', ')
			  FROM (SELECT raw_model_name, count(*) AS n
			          FROM public.v_supplier_price_vs_baseline GROUP BY 1) x
			 WHERE n > 1`).Scan(&dup)
		t.Fatalf("%d bindings produced %d view rows — a deviation report that duplicates a "+
			"supplier price across two baselines makes the same cost look simultaneously "+
			"20%% over and 40%% under (duplicated: %s)", bindings, rows, dup)
	}

	// **三道机制的分工**（逐条回退实测得出，不要凭直觉调整）：
	//   · LIMIT 1 —— **承重**。去掉它，m-case-dup（只差大小写的一对
	//     canonical，名字那条路命中 2 行）立刻变成 2 行。
	//   · `pm.canonical_id IS NULL` —— **冗余**。去掉它无任何变化：
	//     LIMIT 1 仍然只留一行。
	//   · ORDER BY (id 匹配优先) DESC —— **未证明**。去掉它这条判据仍然
	//     绿，因为当前 PostgreSQL 恰好仍把 id 匹配那一行先返回。
	//     **行序没有保证**，所以它是对扫描顺序的保险，不是可验证的机制。
	//
	// ⇒ 因此下面两条断言**一律钉结果、不钉实现**：钉「m-split 必须落在
	// 基准 5.00」而不是「ORDER BY 必须存在」。这样即使将来 PostgreSQL 换了
	// 扫描顺序导致行为改变，测试会**如实变红**，而不会因为某个机制名还在
	// 就继续绿——后者正是「断言实现的实现」这类假绿的来源。

	// 拆开的那一条必须走 canonical_id（权威），而不是按名字落到另一种写法上。
	var base float64
	if err := pool.QueryRow(ctx, `
		SELECT baseline_in_per_1m FROM public.v_supplier_price_vs_baseline
		 WHERE raw_model_name = 'm-split'`).Scan(&base); err != nil {
		t.Fatalf("read m-split: %v", err)
	}
	if base != 5.00 {
		t.Errorf("m-split resolved to baseline %.2f, want 5.00 — canonical_id is authoritative "+
			"and must beat the canonical_raw_name fallback", base)
	}
	// m-case-dup 只能出**一行**：它证明 LIMIT 1 还在（否则这一行会变成两行，
	// 而上面的行数断言会先炸——这条断言是为了让失败信息直接指向 LIMIT 1）。
	var caseRows int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM public.v_supplier_price_vs_baseline
		 WHERE raw_model_name = 'm-case-dup'`).Scan(&caseRows); err != nil {
		t.Fatalf("read m-case-dup: %v", err)
	}
	if caseRows != 1 {
		t.Errorf("a raw name that matches two case-variant canonical rows produced %d rows, "+
			"want 1 — LIMIT 1 is the only mechanism that collapses that", caseRows)
	}
}
