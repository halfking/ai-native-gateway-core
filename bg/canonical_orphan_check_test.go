package bg

// 第 18 条 `canonical_row_discovered_but_never_referenced` 的判据。
//
// # 这条检查在报什么（2026-10-06 真库读数，不是推演）
//
// 查「哪些模型永远无法被多模态核实」时撞出来的一个恒等式：
//
//	零引用的 models_canonical 行 = 228
//	永远无法核实的模型        = 228
//
// 两者**完全重合**，因为 modal prober 走 credential_model_bindings，而一条
// 没有任何 provider_models 行的 canonical 不可能有绑定。所以「228」不是
// 「228 个模型没核实」，而是「**228 行 models_canonical 谁也没在用**」。
//
// 按 provenance 切开后，auto_discovered 是那个离群值：58 行里 50 行零引用
// （86%），对照 provider_refresh 是 321 行里 3 行。
//
// # 三条判据各自证明什么
//
//	A 四臂种群筛选：只有 auto_discovered ∧ active ∧ 零引用 才报
//	  （另外三臂是 seed / disabled / 有引用，各自有不报的理由）
//	B 名字含空白 ⇒ detail 必须点名「一行存了多个模型名」这个解析缺陷
//	C bulk 汇总行只在上界 > 20 时出现（阈值双向）
//	D 走一遍 runChecks：插进去的行 entity_id 非 0、detail 非空、fix_sql 为空
//
// # 为什么夹具复用 supplierViewFixture 而不是手抄列
//
// models_canonical **不是手抄的**，而是从仓的逐对象 SSOT 推导
//（internal/schemaobj，见 supplierViewFixture 的注释）。本项目反复吃亏的
// 一点就是「夹具形状与生产不一致」，而手抄 models_canonical 正是会引入
// 差异的那条路（真表 id 上没有主键、没有索引）。⇒ 复用，不写第二遍。
//
// 而「本条 SQL 引用到的列在夹具里确实存在」由 TestOrphanCanonicalFixtureHas
// TheColumnsTheQueryReads 显式钉住 —— 缺列会让查询直接报错，那种失败是
// 偶然的；这条断言把「夹具够用」变成必然。

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// orphanCheckSQL 取本条检查的 SQL。
//
// 取不到就 Fatal 而不是静默跳过：否则「检查没注册」会被读成「没有缺陷」。
func orphanCheckSQL(t *testing.T) string {
	t.Helper()
	for _, d := range AllHealthChecks() {
		if d.CheckID == "canonical_row_discovered_but_never_referenced" {
			return d.Query
		}
	}
	t.Fatalf("check canonical_row_discovered_but_never_referenced not found in AllHealthChecks() "+
		"(%d checks registered); this test cannot verify what it claims to", len(AllHealthChecks()))
	return ""
}

// orphanPool 连夹具库并建出本条检查要读的那几张表。
//
// ★ 清理注册在**建表之前**：建到一半失败会留残桩，下一轮安全闸看到「表已存在」
//
//	直接 SKIP，人看到的是「通过」，实际一次都没跑。
func orphanPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置，跳过孤儿 canonical 夹具回归")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("connect real db: %v", err)
	}
	t.Cleanup(pool.Close)

	// 同包其他测试也建这些表并靠「已存在就 SKIP」避开。抢到残桩时同样 SKIP，
	// 但要说清是哪张表 —— 静默 SKIP 是本项目反复吃亏的地方。
	tables := []string{"routing_health_checks", "provider_models",
		"credentials", "providers", "models_canonical"}
	var existing []string
	rows, err := pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		  WHERE table_schema='public' AND table_name = ANY($1)`, tables)
	if err != nil {
		t.Fatalf("probe schema: %v", err)
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		existing = append(existing, n)
	}
	rows.Close()
	if len(existing) > 0 {
		t.Skipf("%v already exist in the fixture db — a previous test left residue, so this "+
			"run would measure a half-built database", existing)
	}

	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
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
	// autoFixCanonicalID 写 provider_models.canonical_cleared_at；夹具不建它。
	if _, err := pool.Exec(ctx, `ALTER TABLE public.provider_models
		ADD COLUMN IF NOT EXISTS canonical_cleared_at timestamptz`); err != nil {
		t.Fatalf("add canonical_cleared_at: %v", err)
	}
	return pool
}

// orphanAlert 是一条命中行。
type orphanAlert struct{ key, name, detail string }

// runOrphanCheck 跑本条检查的 SQL，回读全部命中行。
func runOrphanCheck(t *testing.T, pool *pgxpool.Pool) []orphanAlert {
	t.Helper()
	ctx := context.Background()
	rows, err := pool.Query(ctx, orphanCheckSQL(t))
	if err != nil {
		t.Fatalf("run orphan check: %v", err)
	}
	defer rows.Close()
	var out []orphanAlert
	for rows.Next() {
		var a orphanAlert
		if err := rows.Scan(&a.key, &a.name, &a.detail); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// seedCanonical 种一行 models_canonical，回传 id。
func seedCanonical(t *testing.T, pool *pgxpool.Pool, name, source, status string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO public.models_canonical (canonical_name, family, source, status, created_at)
		 VALUES ($1, 'unknown', $2, $3, now() - interval '3 days') RETURNING id`,
		name, source, status).Scan(&id); err != nil {
		t.Fatalf("seed canonical %q: %v", name, err)
	}
	return id
}

// referenceCanonical 给一条 canonical 造一个 provider_models 引用，
// 使它不再「零引用」。
func referenceCanonical(t *testing.T, pool *pgxpool.Pool, canonicalID int64, rawModel string) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO public.providers (code, display_name) VALUES ('p1','P1')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.provider_models (provider_id, raw_model_name, canonical_id, canonical_raw_name)
		 SELECT pr.id, $2, $1, 'x' FROM public.providers pr WHERE pr.code='p1'`,
		canonicalID, rawModel); err != nil {
		t.Fatalf("reference canonical %d: %v", canonicalID, err)
	}
}

// A：四臂种群筛选。这是本条检查最容易写宽的地方 —— 三个漏网方向各自有理由。
func TestOrphanCanonicalReportsOnlyAutoDiscoveredActiveUnreferencedRows(t *testing.T) {
	pool := orphanPool(t)
	ctx := context.Background()

	// 臂 0：空表 ⇒ 必须不报（恒真检测）
	if got := runOrphanCheck(t, pool); len(got) != 0 {
		t.Fatalf("an empty fixture must produce no alert, got %d row(s) — the check fires "+
			"unconditionally", len(got))
	}

	// ★ 量具自证（前置）：下面那个「该报的行」必须真的满足全部三个条件，
	//   否则后面「只报了 1 行」可能是因为它压根没进候选集，而断言照样绿。
	orphan := seedCanonical(t, pool, "auto-orphan", "auto_discovered", "active")
	var provSource, provStatus string
	var refCount int
	if err := pool.QueryRow(ctx,
		`SELECT source, status,
		        (SELECT count(*) FROM public.provider_models pm WHERE pm.canonical_id = mc.id)
		   FROM public.models_canonical mc WHERE mc.id = $1`, orphan).
		Scan(&provSource, &provStatus, &refCount); err != nil {
		t.Fatalf("precondition read: %v", err)
	}
	if provSource != "auto_discovered" || provStatus != "active" || refCount != 0 {
		t.Fatalf("precondition not met: seeded row is source=%q status=%q refs=%d; want "+
			"auto_discovered/active/0 — every assertion below would be vacuous",
			provSource, provStatus, refCount)
	}

	// 三个**不该**报的行，各有各的理由。
	seedCanonical(t, pool, "seed-orphan", "seed", "active")                  // 种子清单，按设计未接供应商
	seedCanonical(t, pool, "disabled-orphan", "auto_discovered", "disabled") // 已被人工下架
	linked := seedCanonical(t, pool, "auto-linked", "auto_discovered", "active")
	referenceCanonical(t, pool, linked, "raw-auto-linked") // 有人引用

	got := runOrphanCheck(t, pool)
	if len(got) != 1 {
		var names []string
		for _, a := range got {
			names = append(names, a.name)
		}
		t.Fatalf("got %d alert(s) %v, want exactly 1 (auto-orphan). The three suppressed "+
			"populations are each excluded on purpose: seed rows are an un-onboarded "+
			"catalogue BY DESIGN, disabled rows were already triaged by a human, and a "+
			"referenced row is in use", len(got), names)
	}
	if got[0].name != "auto-orphan" {
		t.Errorf("the single alert is %q, want auto-orphan", got[0].name)
	}
	// 首列必须与 name 同值：它是落进健康表的稳定键，两者不一致意味着
	// 「显示的模型」与「被追踪的实体」是两条记录。
	if got[0].key != got[0].name {
		t.Errorf("key=%q but name=%q — the stable key that identifies the health row must be "+
			"the canonical name, otherwise the alert tracks a different entity than it shows", got[0].key, got[0].name)
	}
}

// B：名字含空白 ⇒ detail 必须点名「一行存了多个模型名」。
//
// 这一支是本条检查真正的技术发现：真库 id=3256018 的 canonical_name 是
// `gpt-5.6-terra claude-sonnet-5 claude-opus-5 gpt-6-sol gpt-6-astra` ——
// 自动发现把一行多模型当成了一个模型名。只报「未引用」的话，运营会去删行，
// 而正确的第一反应是「解析器错了」。
func TestOrphanCanonicalWhitespaceNameIsCalledOutAsAParseDefect(t *testing.T) {
	pool := orphanPool(t)
	seedCanonical(t, pool,
		"gpt-5.6-terra claude-sonnet-5 claude-opus-5 gpt-6-sol gpt-6-astra",
		"auto_discovered", "active")

	got := runOrphanCheck(t, pool)
	if len(got) != 1 {
		t.Fatalf("got %d alert(s), want 1: %+v", len(got), got)
	}
	d := got[0].detail
	for _, want := range []string{"whitespace", "SEVERAL model names", "one canonical row"} {
		if !strings.Contains(d, want) {
			t.Errorf("detail must contain %q so the reader sees \"the parser is wrong\", not "+
				"\"the catalogue is untidy\" — a multi-model line stored as one name is a write-side parse "+
				"defect and the fix is different from deleting a row. Got:\n%s", want, d)
		}
	}

	// 反向：普通名字**不得**被说成解析缺陷。把空白信号写成恒真也会过上面那条，
	// 所以这一半是承重的。
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM public.models_canonical`); err != nil {
		t.Fatalf("clear: %v", err)
	}
	seedCanonical(t, pool, "plain-model-name", "auto_discovered", "active")
	got = runOrphanCheck(t, pool)
	if len(got) != 1 {
		t.Fatalf("got %d alert(s), want 1: %+v", len(got), got)
	}
	if strings.Contains(got[0].detail, "SEVERAL model names") {
		t.Errorf("a single-token name must NOT be reported as a multi-model parse defect:\n%s",
			got[0].detail)
	}
}

// C：bulk 汇总行的阈值双向。
//
// 只测 >20 那半边的话，把 `> 20` 删掉也能过；只测 <=20 那半边的话，把
// `> 20` 改成 `> 0` 也能过。两侧都要。
func TestOrphanCanonicalBulkSummaryAppearsOnlyAboveTwenty(t *testing.T) {
	pool := orphanPool(t)
	ctx := context.Background()

	// 20 行：贴着阈值下沿，不出汇总行，逐条列名。
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.models_canonical (canonical_name, family, source, status)
		 SELECT 'auto-orphan-'||g, 'unknown', 'auto_discovered', 'active'
		   FROM generate_series(1,20) g`); err != nil {
		t.Fatalf("seed 20: %v", err)
	}
	got := runOrphanCheck(t, pool)
	if len(got) != 20 {
		t.Fatalf("20 orphans must yield 20 named rows and no summary, got %d", len(got))
	}
	for _, a := range got {
		if strings.HasPrefix(a.name, "(bulk)") {
			t.Fatalf("a summary row appeared at 20 orphans; the threshold is exclusive "+
				"(> 20), got %q", a.name)
		}
	}

	// 21 行 ⇒ 恰好 1 条汇总行，且它必须带缺失数（工作量）。
	if _, err := pool.Exec(ctx,
		`INSERT INTO public.models_canonical (canonical_name, family, source, status)
		 VALUES ('auto-orphan-21', 'unknown', 'auto_discovered', 'active')`); err != nil {
		t.Fatalf("seed 21st: %v", err)
	}
	got = runOrphanCheck(t, pool)
	var bulk []orphanAlert
	for _, a := range got {
		if strings.HasPrefix(a.name, "(bulk)") {
			bulk = append(bulk, a)
		}
	}
	if len(bulk) != 1 {
		t.Fatalf("21 orphans must collapse to exactly 1 summary row, got %d summary row(s) "+
			"among %d total", len(bulk), len(got))
	}
	if !strings.Contains(bulk[0].name, "21") {
		t.Errorf("the summary row %q does not state how many rows are orphaned — it must carry "+
			"the workload, not just the fact", bulk[0].name)
	}
	// 汇总行的首列必须是**常量**：健康表的 UNIQUE 是
	// (check_id, entity_type, entity_id)，首列若含计数，entity_id 每轮换一行，
	// 旧行留在 status='open' 变成永不消失的僵尸告警。
	if bulk[0].key != "(bulk) auto-discovered-orphan-canonical" {
		t.Errorf("summary row key=%q, want the constant \"(bulk) auto-discovered-orphan-canonical\" "+
			"— a key containing the count would land on a NEW health row every cycle and orphan "+
			"the previous one", bulk[0].key)
	}
}

// D：走一遍 runChecks —— 查询命中不等于能被读出来。
//
// 缺 scan case 时 switch 走空，entityID/entityName/detail 全保持零值，
// 而循环照样把这一行插进健康表（见 health_check_scan_guard_test.go 的
// 缺陷记录）。所以「查询正确」与「结果能被读出来」之间隔着那个 switch。
func TestOrphanCanonicalAlertRowIsActionableAndOffersNoOneClickFix(t *testing.T) {
	pool := orphanPool(t)
	ctx := context.Background()

	seedCanonical(t, pool, "auto-orphan", "auto_discovered", "active")

	var def HealthCheckDef
	found := false
	for _, c := range AllHealthChecks() {
		if c.CheckID == "canonical_row_discovered_but_never_referenced" {
			def, found = c, true
			break
		}
	}
	if !found {
		t.Fatal("check missing from AllHealthChecks()")
	}

	var hit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM (`+def.Query+`) q`).Scan(&hit); err != nil {
		t.Fatalf("run the check query: %v", err)
	}
	if hit != 1 {
		t.Fatalf("the check matched %d row(s) for the seeded orphan; every assertion below is "+
			"vacuous if the fixture never reached the check", hit)
	}

	crit, warn, err := runChecks(ctx, pool, []HealthCheckDef{def})
	if err != nil {
		t.Fatalf("runChecks: %v", err)
	}
	if crit != 0 || warn != 1 {
		t.Errorf("got newCritical=%d newWarning=%d for 1 matched row on a severity=warning check",
			crit, warn)
	}

	var entityID int64
	var entityType, name, detail, fix string
	if err := pool.QueryRow(ctx, `SELECT entity_id, entity_type, entity_name, detail, fix_sql
		FROM public.routing_health_checks WHERE check_id=$1`, def.CheckID).
		Scan(&entityID, &entityType, &name, &detail, &fix); err != nil {
		t.Fatalf("read the inserted row (the check matched %d row(s) but inserted none): %v", hit, err)
	}
	if entityID == 0 {
		t.Errorf("inserted entity_id=0 — that is the no-scan-branch signature: the query matched a " +
			"row, but the switch had no case for this CheckID, so the row was inserted with every " +
			"column at its zero value")
	}
	if entityType != def.CheckID {
		t.Errorf("entity_type=%q, want %q — the health table's UNIQUE is "+
			"(check_id, entity_type, entity_id); a mismatched entity_type splits one check's "+
			"identity space in two", entityType, def.CheckID)
	}
	if name != "auto-orphan" {
		t.Errorf("entity_name=%q, want auto-orphan", name)
	}
	if detail == "" {
		t.Error("inserted detail='' — an empty detail is not actionable in the health surface")
	}
	// 刻意不给一键修复：处置有两个方向（删行 / 接供应商补绑定），而这个模型在
	// 业务上还要不要取决于业务判断。给一个 DELETE 按钮替不了人做这个决定。
	if fix != "" {
		t.Errorf("fix_sql=%q, want empty — deleting a models_canonical row vs onboarding the "+
			"vendor are opposite fixes, and a one-click button can only perform one of them", fix)
	}

	// 跨轮稳定性：再跑一次必须**刷新同一行**，而不是插第二条。
	// 不稳定 ⇒ UPSERT 收敛不了 ⇒ 每轮一条新行，旧行永远 open。
	var after int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM public.routing_health_checks WHERE check_id=$1`, def.CheckID).
		Scan(&after); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if after != 1 {
		t.Errorf("after a second cycle the check has %d row(s) in routing_health_checks, want 1 — "+
			"entity_id must be stable across cycles or every cycle orphans the previous row as a "+
			"permanently-open zombie alert", after)
	}
}

// 夹具忠实性：本条 SQL 读到的每一列都必须在夹具里存在且类型一致。
//
// models_canonical 来自 SSOT（生产形状），provider_models 是脚手架。这条
// 断言的作用是让「夹具够用」从偶然变成必然 —— 少一列时查询会报错，那是
// 偶然失败；这里把它变成必然且可读的失败。
func TestOrphanCanonicalFixtureHasTheColumnsTheQueryReads(t *testing.T) {
	pool := orphanPool(t)
	rows, err := pool.Query(context.Background(),
		`SELECT table_name, column_name, data_type FROM information_schema.columns
		  WHERE table_schema='public'
		    AND ((table_name='models_canonical' AND column_name IN
		            ('id','canonical_name','family','created_at','source','status'))
		      OR (table_name='provider_models' AND column_name='canonical_id'))
		  ORDER BY table_name, column_name`)
	if err != nil {
		t.Fatalf("read fixture columns: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var tbl, col, ty string
		if err := rows.Scan(&tbl, &col, &ty); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[tbl+"."+col] = ty
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// 生产实测值（127.0.0.1:5432 information_schema，2026-10-06）
	want := map[string]string{
		"models_canonical.id":             "bigint",
		"models_canonical.canonical_name": "text",
		"models_canonical.family":         "text",
		"models_canonical.created_at":     "timestamp with time zone",
		"models_canonical.source":         "text",
		"models_canonical.status":         "text",
		"provider_models.canonical_id":    "bigint",
	}
	for k, ty := range want {
		gotTy, ok := got[k]
		if !ok {
			t.Errorf("fixture column %s is missing — the check query reads it, so the test "+
				"would fail at query time instead of here", k)
			continue
		}
		if gotTy != ty {
			t.Errorf("fixture column %s is %s, production is %s — a type mismatch here makes "+
				"the test measure something other than the check", k, gotTy, ty)
		}
	}
}
