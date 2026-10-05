package bg

// 目标第一半「自动对未曾标注核实过的模型定时进行核实，**这个需要加入到自检任务
// 中**」在健康面的判据。
//
// 缺口（2026-10-04 盘点）
//
// 核实 worker 早已接线（cmd/gateway/main.go:4589 `go modalityVerify.Run(...)`），
// 827 也把判断所需的每个数算好了 —— 但健康面 8 条检查里**没有一条覆盖多模态
// 核实**，而 827 的两个视图在生产侧无人读。⇒「定时核实」在运维可见的界面上
// 是隐形的，`models_blocked_by_strict` 只能靠人手动查。
//
// 承重的是什么
//
// 这条检查与另两条价格检查的**故障形态相反**：它们坏的时候是「查得到、报不出」
// （缺扫描分支 → 插全空行）；这条坏的时候是「WHERE 恒真 / 恒假」——
// 恒真会把已确认的模型也报成阻塞项，恒假则让阻塞项彻底隐形。
// ⇒ 判据必须**双向**：挡路的要报、通行的不能报。
//
// 数据源用 827 的 progress 视图（一个**关系**）而不是直接读 825 的列：827 未应用
// 时缺关系报 42P01，落在 Optional 已支持的那条路上；若直接读列则报 42703
// undefined_column，而 Optional 只认 42P01 ⇒ 整轮健康检查会中止。
//
// 依赖真库：没有 TEST_DATABASE_URL 时跳过；夹具表已存在时也跳过（它要建表）。

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/kaixuan/llm-gateway-go/internal/schemaobj"
)

// modalityGateFixture 只建 models_canonical（真表，从仓的逐对象 SSOT 推导）。
//
// ★ 刻意**不**手抄：真表的 id 上没有主键、没有唯一约束、连索引都没有，
//
//	手抄的替身当初写了 `id bigserial PRIMARY KEY` —— 比真表更宽松，于是
//	「唯一约束缺失」这类缺陷在夹具里永远看不见。bg/supplier_view_cardinality_test.go
//	已经在同一处栽过这个跟头（它的 provider_models 脚手架曾给真表补上 FK）。
//
// 825 会用 `ADD CONSTRAINT models_canonical_pkey PRIMARY KEY (id)` 把它补上
// （并发会话 b251929ab 的修法），所以「真表本来没主键」这件事能被忠实保留到
// 825 应用之前的那一步。
func modalityGateFixture(t *testing.T) string {
	t.Helper()
	return "CREATE SEQUENCE IF NOT EXISTS public.models_canonical_id_seq;\n" +
		schemaobj.Table(t,
			"../sql/objects/tables/models_canonical.sql",
			"../sql/objects/sequences/models_canonical_id.sql",
			"../sql/objects/constraints/models_canonical_models_canonical_canonical_name_key.sql",
		)
}

func TestModalityVerificationStaleReportsBlockedPairsOnly(t *testing.T) {
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

	tables := []string{"routing_health_checks", "model_modality_verification", "models_canonical"}
	var existing int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='public' AND table_name = ANY($1)`, tables).Scan(&existing); err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	if existing > 0 {
		t.Skipf("%d of the fixture tables already exist — this test drops them", existing)
	}

	// ★ 清理注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到
	// 「表已存在」直接 SKIP，人看到的是「通过」，实际一次都没跑。
	// 清理清单穷举 825/827 建出来的每一样东西 —— 漏一张的话，下一轮
	// `CREATE ... IF NOT EXISTS` 会静默沿用**旧形状**，测试照样绿，
	// 而绿的是一份几百行前的结构。
	defer func() {
		_, _ = pool.Exec(ctx, `
			DROP VIEW IF EXISTS public.v_model_modality_verification_rollup;
			DROP VIEW IF EXISTS public.v_model_modality_verification_progress;
			DROP VIEW IF EXISTS public.v_model_modality_verdict;
			DROP TABLE IF EXISTS public.model_modality_verification;
			DROP TABLE IF EXISTS public.routing_health_checks;
			DROP TABLE IF EXISTS public.provider_models;
			DROP TABLE IF EXISTS public.models_canonical;
			DROP SEQUENCE IF EXISTS public.models_canonical_id_seq;`)
	}()

	if _, err := pool.Exec(ctx, `CREATE TABLE public.routing_health_checks (
		check_id text, severity text, entity_type text, entity_id bigint,
		entity_name text, detail text, fix_sql text, status text,
		created_at timestamptz, updated_at timestamptz,
		UNIQUE (check_id, entity_type, entity_id));
		-- runChecks 收尾时会**无条件**调用 autoFixCanonicalID（它是 canonical_id_null
		-- 检查的自动修复，与本次被测的那条无关）。它 UPDATE provider_models，所以
		-- 即便只跑一条检查，这张表也必须在场，否则 runChecks 在最后一步以 42P01
		-- 失败 —— 而那个报错与被测对象毫无关系。
		CREATE TABLE public.provider_models (
			id bigserial PRIMARY KEY,
			raw_model_name text NOT NULL,
			canonical_id bigint,
			canonical_cleared_at timestamptz);`); err != nil {
		t.Fatalf("create support tables: %v", err)
	}
	if _, err := pool.Exec(ctx, modalityGateFixture(t)); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	// 夹具过期的响亮告警：真表若哪天补上了主键/唯一约束，这份推导就过时了。
	// 断言的不是「真表该缺约束」，而是「这份夹具还代表真表」。
	var uniq int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_constraint
		 WHERE conrelid = 'public.models_canonical'::regclass
		   AND contype IN ('p','u')
		   AND conkey = ARRAY[(SELECT attnum FROM pg_attribute
		                        WHERE attrelid = 'public.models_canonical'::regclass
		                          AND attname = 'id')]::smallint[]`).Scan(&uniq); err != nil {
		t.Fatalf("probe models_canonical keys: %v", err)
	}
	if uniq > 0 {
		t.Fatalf("models_canonical.id now has %d unique constraint(s) — the repo SSOT and this "+
			"fixture are out of date; re-derive modalityGateFixture from sql/objects/", uniq)
	}

	// 应用**真** 825 与真** 827，不手抄 DDL：与 provider/modality_gate_alignment_test.go
	// 同一取舍。手抄会把「迁移里到底写了什么」从判据里摘出去。
	for _, name := range []string{
		"825_modality_graded_verification.sql",
		"827_modality_verification_progress_view.sql",
	} {
		b, err := os.ReadFile("../sql/migrations/startup/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}

	// 两个模型：`never-probed` 零证据（严格档会挡）、`confirmed` 有一条
	// confirmed 证据（两档都不挡）。**双向是承重的**。
	//
	// modality_source 的合法值是 825 自己 CHECK 死的：
	// inferred / structural / semantic / manual（825:109）。这里用 inferred
	// —— 本判据关心的判词来自证据表，不来自这一列，所以选哪个合法值都不影响
	// 被测行为。
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.models_canonical (canonical_name, modality, modality_source)
		VALUES ('m-never-probed','vision','inferred'),
		       ('m-confirmed','vision','inferred');
		INSERT INTO public.model_modality_verification
			(canonical_id, canonical_name, credential_id, raw_model_name, modality,
			 carry_level, read_level, read_pos_streak, checked_at)
		SELECT mc.id, mc.canonical_name, 1, 'm-confirmed', 'vision',
		       'accepted', 'confirmed', 2, now()
		  FROM public.models_canonical mc
		 WHERE mc.canonical_name = 'm-confirmed';`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	var def HealthCheckDef
	found := false
	for _, c := range AllHealthChecks() {
		if c.CheckID == "modality_verification_stale" {
			def, found = c, true
			break
		}
	}
	if !found {
		t.Fatal("modality_verification_stale missing from AllHealthChecks() — the target's first " +
			"half (scheduled modality verification on the self-check surface) would be invisible")
	}
	if !def.Optional {
		t.Fatal("modality_verification_stale is not marked Optional — on any environment that has " +
			"not applied 827 the missing view would abort the whole health run")
	}

	// **量具自证**：查询命中数必须等于「全部组合数 - 已确认的组合数」。
	//
	// 期望值是 5 而不是 1：progress 视图为每个 (canonical, modality) 都**先存在
	// 一行**，哪怕一条证据都没有（827 的 per_pair CTE 就是干这个的）—— 2 个模型
	// × 3 个模态（vision/audio/video）= 6 组，减去唯一那条 confirmed = 5 组挡路。
	// 这不是视图的毛病，正是它让「没核实」可见的设计。
	//
	// 同时把「恰好 5」固定下来：恒真的 WHERE 会得到 6（把 confirmed 那组也报出来），
	// 恒假得到 0。
	var pairs, confirmedPairs int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE verdict='confirmed')
		FROM public.v_model_modality_verification_progress`).Scan(&pairs, &confirmedPairs); err != nil {
		t.Fatalf("count progress pairs: %v", err)
	}
	if pairs != 6 || confirmedPairs != 1 {
		t.Fatalf("the fixture produced %d pairs (%d confirmed), want 6 (1 confirmed) — the "+
			"seed did not land as intended and every assertion below is vacuous", pairs, confirmedPairs)
	}
	want := pairs - confirmedPairs

	var hit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+def.Query+`) q`).Scan(&hit); err != nil {
		t.Fatalf("run the check query: %v", err)
	}
	if hit != want {
		t.Fatalf("the check matched %d rows, want %d (every pair except the confirmed one) — a "+
			"query that reported the confirmed pair too (%d) or missed blocked ones would be wrong",
			hit, want, pairs)
	}

	// ★ 注意返回顺序：runChecks 返回 (newCritical, newWarning, err)。
	crit, warn, err := runChecks(ctx, pool, []HealthCheckDef{def})
	if err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	if crit != 0 || warn != want {
		t.Errorf("got newCritical=%d newWarning=%d for %d matched rows on a severity=warning check",
			crit, warn, want)
	}

	// 逐行读回（而不是 LIMIT 1）：5 行里有 4 行属于 never-probed 模型、1 行属于
	// confirmed 模型的 audio 模态。只读一行会漏掉「同一模型跨模态重复上报」这件事。
	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='modality_verification_stale'`).Scan(&rowCount); err != nil {
		t.Fatalf("count inserted rows: %v", err)
	}
	if rowCount != want {
		t.Errorf("inserted %d row(s), want %d — the check matched %d rows, so the mismatch is in "+
			"the scan branch or the upsert", rowCount, want, want)
	}

	var entityID int64
	var name, detail, fix string
	if err := pool.QueryRow(ctx, `SELECT entity_id, entity_name, detail, fix_sql
		FROM public.routing_health_checks
		WHERE check_id='modality_verification_stale' AND entity_name LIKE '%[vision]%'
		LIMIT 1`).
		Scan(&entityID, &name, &detail, &fix); err != nil {
		t.Fatalf("read the inserted row (the check matched %d rows but inserted none): %v", hit, err)
	}
	if entityID == 0 {
		t.Errorf("inserted entity_id=0 — that is the no-scan-branch signature: the query matched a " +
			"row but the switch had no case for this CheckID")
	}
	if name == "" || detail == "" {
		t.Errorf("inserted entity_name=%q detail=%q — an empty row is not actionable in the "+
			"health surface", name, detail)
	}
	// 显式断言「**已确认的那一组**不得被报出来」：WHERE 恒真也能通过上面所有断言
	// （它会得到 6 而不是 5，所以上面那条其实已经抓到了；这一条让失败信息直接
	// 指向「把已确认的也报成了阻塞」这个具体形态）。
	//
	// 刻意**不**断言「m-confirmed 一行都不许出现」：它的 audio / video 组合确实
	// 没有任何证据，被严格档挡住是**正确**的。对一个不成立的性质写断言，比不写
	// 更糟。
	var leaked int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.routing_health_checks
		WHERE check_id='modality_verification_stale'
		  AND entity_name = 'm-confirmed [vision]'`).Scan(&leaked); err != nil {
		t.Fatalf("count leaked rows: %v", err)
	}
	if leaked != 0 {
		t.Errorf("the check reported %d row(s) for 'm-confirmed [vision]', which HAS a confirmed "+
			"read-level verdict and is blocked by neither gate — a confirmed pair is not a problem",
			leaked)
	}
}
