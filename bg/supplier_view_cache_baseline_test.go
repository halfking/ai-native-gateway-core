package bg

// supplier_view_cache_baseline_test.go —— 「缓存基准价必须真的进入告警通路」
//
// 这条判据是 2026-10-07 缺口修复（迁移 844）的直接产物。
//
// ── 缺陷 ──────────────────────────────────────────────────────────────
// models_canonical 有 baseline_cache_read/write_price_per_1m（826 建列 +
// 非负 CHECK），credential_model_bindings 有对应的 cache_read/write_price_per_1m
// —— 两侧都有值，**比得出来**。
//
// 但 826 建的 v_supplier_price_vs_baseline 只投影 in/out 两个基准价：
// cache 列只出现在 ADD COLUMN 与 CHECK 段，**不在视图投影段**，
// 连那个 LATERAL 子查询的 SELECT 列表里都没有它们。
// ⇒ supplier_price_drift 的 WHERE 与 SELECT 全部走视图列
//   ⇒ 缓存基准价即使在库里有值，也**永远进不了任何告警**。
//
// 与 2026-10-07 人工拍板的关系：基准价的角色被定为「合理性下限」告警、
// 不参与金额计算。⇒ 「基准价只写不读」因此不是中性的观察缺口，
// 而是**功能缺失**：被指定为告警依据的四个价里有两个根本没接进通路。
//
// ── 为什么要判据，而不是注释 ────────────────────────────────────────
// 「视图少投影两列」这种缺陷**不会让任何东西报错**：视图照建、查询照跑、
// 告警照报（只是永远看不到缓存那半边）。它是本仓已记录的
// 「台账只写不读」族的第 N 例。
// ⇒ 唯一的自证方式是**断言那几个列名在视图定义里**，而且要断言到
//   子查询那一层——只查主 SELECT 列表会漏掉「LATERAL 没取该列」这个形态。
//
// 判据是**纯静态**的：每次都跑，不需要数据库。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// supplierViewFile 是承载该视图的受追踪迁移。
const supplierViewFile = "sql/migrations/startup/826_model_baseline_price.sql"

// cacheBaselineMigrationFile 是补登投影的迁移（844）。
const cacheBaselineMigrationFile = "sql/migrations/startup/844_supplier_view_cache_baseline_columns.sql"

// mustReadRepoFile 读仓内文件；失败即 FAIL（不 SKIP）——
// 「读不到判据要检查的文件」本身就是缺陷，不该被当成环境问题跳过。
func mustReadRepoFile(t *testing.T, rel string) string {
	t.Helper()
	root := repoRootFromBg(t)
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读取 %s 失败 %v。\n"+
			"  这条判据要检查的迁移文件必须存在于仓内；\n"+
			"  文件被改名/移动时必须同步改本判据里的路径。", rel, err)
	}
	return string(b)
}

// viewBody 抽出 CREATE [OR REPLACE] VIEW <viewName> 的定义体。
func viewBody(t *testing.T, sql, viewName string) string {
	t.Helper()
	lower := strings.ToLower(sql)
	idx := strings.Index(lower, "create or replace view public."+strings.ToLower(viewName))
	if idx < 0 {
		idx = strings.Index(lower, "create view public."+strings.ToLower(viewName))
	}
	if idx < 0 {
		t.Fatalf("%s 里找不到 %s 的 CREATE VIEW 语句。\n"+
			"  视图若被改名或改为别的方式创建（如 DO 块里 EXECUTE），本判据失效 —— \n"+
			"  请同步改这里，不要直接删掉判据。", "迁移文件", viewName)
	}
	rest := sql[idx:]
	// 视图定义以顶层分号结束；这里用 COMMENT ON VIEW 作为更可靠的边界。
	if c := strings.Index(rest, "COMMENT ON VIEW"); c > 0 {
		rest = rest[:c]
	}
	return rest
}

// TestSupplierViewProjectsCacheBaselineColumns 钉住「844 之后视图导出两个缓存基准价」。
//
// 变异对照：把 844 的两个投影列删掉 ⇒ 红；
// 而**原始的 826**（投影缺失）本来就红 ⇒ 这条判据测的正是原来没测的那个量。
func TestSupplierViewProjectsCacheBaselineColumns(t *testing.T) {
	body := viewBody(t, mustReadRepoFile(t, cacheBaselineMigrationFile),
		"v_supplier_price_vs_baseline")

	for _, col := range []string{
		"baseline_cache_read_per_1m",
		"baseline_cache_write_per_1m",
	} {
		if !strings.Contains(body, col) {
			t.Errorf("844 的视图定义里没有 %q。\n"+
				"    models_canonical 与 credential_model_bindings 两侧都有缓存价、比得出来，\n"+
				"    但视图不投影 ⇒ supplier_price_drift 永远看不到缓存那半边，\n"+
				"    而 2026-10-07 拍板已把基准价定为「合理性下限」告警的依据。", col)
		}
	}
}

// TestSupplierViewCacheRatiosGuardZeroAndCurrency 是「倍率不会算出假数字」的静态形态钉子。
//
// 为什么重要：826 的注释记录过一个真实教训——拿 CNY 的供应商价去比 USD 的基准价，
// 出来的偏差是纯噪声，但它在报表里长得和真偏差一模一样。
// ⇒ 三种「算不出来」必须各有一个分支：基准为空、基准为 0、币种不一致。
func TestSupplierViewCacheRatiosGuardZeroAndCurrency(t *testing.T) {
	body := viewBody(t, mustReadRepoFile(t, cacheBaselineMigrationFile),
		"v_supplier_price_vs_baseline")

	// 每个倍率 CASE 必须同时防住这四种情况，少一条就会产出「精确的错数字」。
	for _, guard := range []string{
		"baseline_cache_read_price_per_1m IS NULL",
		"baseline_cache_read_price_per_1m = 0",
		"cmb.cache_read_price_per_1m IS NULL",
		"IS DISTINCT FROM",
		"baseline_cache_write_price_per_1m IS NULL",
		"baseline_cache_write_price_per_1m = 0",
		"cmb.cache_write_price_per_1m IS NULL",
	} {
		if !strings.Contains(body, guard) {
			t.Errorf("844 的 cache 倍率缺少守卫 %q。\n"+
				"    少一条就会在「基准为 0」「供应商没填」「币种不同」时算出一个\n"+
				"    **看起来精确但其实是错的**倍率 —— 那比 NULL 更坏，\n"+
				"    NULL 至少还长得像「没数据」。", guard)
		}
	}
}

// TestCacheBaselineMigrationSuppliesItsOwnColumns 钉住 844 自带补列。
//
// 依据（本轮实测）：视图定义**不能引用不存在的列**。在只缺
// credential_model_bindings.cache_read/write_price_per_1m 的库上直接建视图 ⇒
//   ERROR: 字段 cmb.cache_read_price_per_1m 不存在 (42703)
// 而全仓没有任何迁移建过这两个列（`ADD COLUMN ... cache_read` 只命中 826，
// 而那是 baseline_cache_*，建在 models_canonical 上）。
// ⇒ 844 必须自己 ADD COLUMN IF NOT EXISTS，不许依赖「反正 398 建过」。
func TestCacheBaselineMigrationSuppliesItsOwnColumns(t *testing.T) {
	body := mustReadRepoFile(t, cacheBaselineMigrationFile)

	if !strings.Contains(body, "ALTER TABLE public.credential_model_bindings") {
		t.Fatalf("844 没有补 credential_model_bindings 的缓存价列。\n"+
			"  实测：在缺这两列的库上，视图定义会 42703 硬失败（整条部署挂掉）。\n"+
			"  全仓没有任何迁移建过它们 —— 398 只是 SELECT 过，不建列。")
	}
	for _, col := range []string{
		"ADD COLUMN IF NOT EXISTS cache_read_price_per_1m",
		"ADD COLUMN IF NOT EXISTS cache_write_price_per_1m",
	} {
		if !strings.Contains(body, col) {
			t.Errorf("844 缺 %q。\n"+
				"    必须用 IF NOT EXISTS 保持幂等；裸 ADD COLUMN 重放会失败。", col)
		}
	}
}

// TestCacheBaselineRollbackReallyRemovesColumns 钉住 down 是**真回滚**。
//
// 依据（本轮实测）：第一版 down 用 `CREATE OR REPLACE VIEW`，
// 实跑报 `无法从视图中删除列`，视图的 4 个 cache 列**一个没少** ——
// 而 psql 不带 ON_ERROR_STOP 时 **rc 仍是 0**，也就是它「报成功」却什么都没撤。
// ⇒ 删列只能 DROP VIEW IF EXISTS + CREATE VIEW。这条判据就是不让它退回去。
func TestCacheBaselineRollbackReallyRemovesColumns(t *testing.T) {
	root := repoRootFromBg(t)
	rel := "sql/migrations/startup/844_supplier_view_cache_baseline_columns.down.sql"
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("读取 %s 失败 %v。\n"+
			"  门禁要求每个迁移都有 .down.sql；缺了就不是可回滚的迁移。", rel, err)
	}
	body := string(b)

	if strings.Contains(body, "CREATE OR REPLACE VIEW public.v_supplier_price_vs_baseline") {
		t.Errorf("%s 用 CREATE OR REPLACE VIEW 撤列 —— 它**撤不掉**。\n"+
			"    实测：报 `无法从视图中删除列`，视图列一个不少，而 psql 的 rc 仍是 0\n"+
			"    （不报错 ⇒ 看起来成功了 ⇒ 这是一条假回滚）。\n"+
			"    正确形态：DROP VIEW IF EXISTS + CREATE VIEW。", rel)
	}
	if !strings.Contains(strings.ToUpper(body), "DROP VIEW IF EXISTS") {
		t.Errorf("%s 缺 `DROP VIEW IF EXISTS`。\n"+
			"    PostgreSQL 不允许用 CREATE OR REPLACE 从视图里删列，只能 DROP + CREATE。", rel)
	}
}

// ★ 剥注释用的是本包已有的 stripSQLComments（bg/supplier_price_zero_wording_test.go:44，
// 同名函数在 bg/auto_index_refresher_sql_test.go:23 还有个带字符串字面量处理的加强版）。
//   第一版判据是自己重写了一个，结果 **redeclaration build failed** ——
//   而一个编译不过的判据在 `go test ./bg/` 的整包输出里只表现为「整包 FAIL」，
//   与「判据抓到了缺陷」在观测上很像。⇒ 先查已有工具，再决定要不要新写。
//
// TestSupplierViewRowDedupGuardSurvived 把 826 记录过的真实事故钉成不可回退。
//
// 826 的注释：JOIN 写成 `ON mc.id = pm.canonical_id OR lower(mc.canonical_name) =
// lower(pm.canonical_raw_name)` 时，真库实测会把**一条**供应商绑定变成**三行**
// （两个列由不同代码路径写入，不一致是可能状态），
// 同一个供应商价被同时报成「比基准贵 20%」和「比基准便宜 40%」。
// 修法是「名字匹配只在 canonical_id 为空时走」+ LATERAL LIMIT 1。
// ⇒ 844 与它的 down 都必须保留这个形态，否则会把那个错价放回去。
//
// ⚠ 断言必须打在**剥掉注释之后**的正文上。
//   第一版直接 `strings.Contains(body, "LIMIT 1")`，而 844 的注释里就写着
//   「ORDER BY、LIMIT 1 与 826 逐字一致」⇒ 变异「删掉真代码的 LIMIT 1」
//   之后判据**照样 PASS**（实测 P3 变异绿）。
//   这条判据的第一版是恒真的，第二版才是真的。
func TestSupplierViewRowDedupGuardSurvived(t *testing.T) {
	for _, rel := range []string{cacheBaselineMigrationFile,
		"sql/migrations/startup/844_supplier_view_cache_baseline_columns.down.sql"} {
		code := stripSQLComments(mustReadRepoFile(t, rel))

		if !strings.Contains(code, "OR (pm.canonical_id IS NULL") {
			t.Errorf("%s 的正文里没有名字兜底匹配那段（`OR (pm.canonical_id IS NULL`）。\n"+
				"    826 记录过：JOIN 写成 OR 时真库实测会把一条绑定变成三行，\n"+
				"    同一个供应商价被报成互相矛盾的两个偏差。", rel)
		}
		if !strings.Contains(code, "LIMIT 1") {
			t.Errorf("%s 的正文里没有 LATERAL 的 `LIMIT 1`。\n"+
				"    没有它，名字兜底匹配可能命中多行 ⇒ 一条绑定变成多行 ⇒\n"+
				"    同一个供应商价被报成互相矛盾的两个偏差（826 记录过这个真实事故）。\n"+
				"    ⚠ 本断言刻意只查剥掉注释后的正文——注释里提到 LIMIT 1 不算数。", rel)
		}
	}
}

// TestSupplierViewCacheAssertionsSeeCodeNotComments 是上面那条的**量具自证**。
//
// 它回答一个问题：「stripSQLComments 真的把注释剥掉了吗？」
// 没有它，上面那条判据可能因为同一个原因恒真，而这件事本身不会暴露。
func TestSupplierViewCacheAssertionsSeeCodeNotComments(t *testing.T) {
	raw := mustReadRepoFile(t, cacheBaselineMigrationFile)

	// 自证 1：原始文件里注释确实提到了 LIMIT 1（这正是第一版恒真的原因）。
	if !strings.Contains(raw, "--") || !strings.Contains(raw, "LIMIT 1") {
		t.Skip("本仓该迁移的注释形态变了（不再有 -- 注释或不再提到 LIMIT 1），" +
			"「判据读到了注释」这个前提不复存在 —— 请复核 stripSQLComments 是否还需要")
	}
	// 自证 2：剥掉注释后，LIMIT 1 仍应存在**一次**（真代码里那一行）。
	code := stripSQLComments(raw)
	if !strings.Contains(code, "LIMIT 1") {
		t.Fatalf("剥掉注释后正文里没有 LIMIT 1 —— 说明本迁移的 LIMIT 1 只存在于注释，\n"+
			"  而那正是一个**真的缺陷**：LATERAL 没有 LIMIT 1，去重修法已经丢了。")
	}
	// 自证 3：构造一个「只有注释提到 LIMIT 1」的片段，剥注释后必须查不到。
	probe := "-- 注释里提到 LIMIT 1\nSELECT 1;\n"
	if strings.Contains(stripSQLComments(probe), "LIMIT 1") {
		t.Errorf("stripSQLComments 没有剥掉行注释 —— 依赖它的判据会变成恒真。\n"+
			"  这就是 P3 变异变绿的原因。")
	}
}