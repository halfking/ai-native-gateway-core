package admin

// 列存面可服务性门（2026-10-05，审计 §9.197 / 决策表 D30-c）。
//
// # 这道门测的是什么
//
// §9.196 定位到 `request_logs_bodies` 的读法失败，根因是 Citus `columnar`。
// 本门把那个结论从**一张表**推广到**整个列存面**，并补上 §9.196 没测的第二种故障。
//
// 本轮在真库上实测出来的三条事实（全部可复现，见审计 §9.197.2）：
//
//  1. **触发条件是「列存关系 + 未命名子查询里的 `UNION ALL`」**。
//     实测 18 种形状：顶层 `UNION ALL` 通过、CTE 通过、普通子查询通过、嵌套子查询通过、
//     子查询内 JOIN 通过、子查询内 `LEFT JOIN` 通过、`WHERE` 里的子查询通过、
//     子查询内 `UNION`（去重）通过、子查询内 `EXCEPT` 通过；
//     而子查询内的 `UNION ALL`（无论哪条腿是列存、无论热腿在前还是在后、
//     无论外层还套不套 JOIN）一律抛
//     `invalid perminfoindex 0 in RTE with relid 0`。
//     ⇒ 这不是「子查询不能包列存表」，是**集合算子 + 子查询**这一组合。
//     记错这一点会导致把好好的两段式查询也改掉。
//
//  2. **故障与具体表无关，是 columnar 的性质**。
//     6 个「列存分区 + `_hot` 孪生」的母表**全部**失败：
//     `request_logs_bodies` / `routing_decision_log` / `supplier_errors` /
//     `usage_ledger` / `request_wal` / `handoff_logs` / `credential_model_index`（7 个）；
//     而同样是两面的 `request_logs`（全 heap）**通过**，返回 2,184,300 行。
//     ⇒ 「只有 bodies 有问题」是错的。§9.196 的门
//     `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable` 只列了 5 对 session 族，
//     看不到上面这 5 个族；本门从**库里的 catalog** 推导全集，不再手写名单。
//
//  3. **第二种、独立的故障：列存关系上的视图无法投影「合成输出列」**。
//     `supplier_errors_unified` 的第 1 列 `source` 是 `SELECT 'hot'::text AS source` 这种
//     **算出来的**列（不是基表列）。`SELECT source FROM supplier_errors_unified` 抛
//     `cache lookup failed for attribute source of relation 12964345`
//     （12964345 = 列存分区 `supplier_errors_2026_09`），
//     而 `SELECT id FROM ...`、`SELECT count(*) FROM ...`、以及 §9.197.4 实测的
//     生产真实形状（把视图包进子查询、只选基表列）**都正常**。
//     逐列扫描 8 个视图 147 列，**只有这 1 列**失败。
//     这一类故障 §9.196 的门完全看不见：它只测两腿 UNION ALL 形状。
//
// # 口径
//
//   - 列存关系全集**从真库 catalog 推导**，不在代码里写死表名。
//     写死的名单会随部署漂移——§9.196 的门就是栽在这上面。
//   - 母表名要**连同它的列存分区一起**收进来。只收分区名会得到**假零**：
//     查询读的是母表，母表名不在名单里 ⇒ 一条都匹配不上（§9.197.3 的自我更正）。
//   - 分类器**穷举**，没有「默认判过」。这一版的默认分支是
//     `UNCLASSIFIED FAIL`——照抄 §9.184.4 的教训：第一版把连接失败默认判成 OK，
//     整张矩阵读数全错。
//   - 形状用 `SELECT 1` 而不是 `SELECT request_id`：与列名解耦，
//     一份查询能覆盖所有族（实测该写法与按列名写的复现性一致）。
//
// # 本门**当前是红的**，而且红得对
//
// `two_surface_setop_shape` 7/7 失败、`deployed_view_column` 1/147 失败。
// 这两处都是**真实存在**的库级故障，不是判据太严：
//   - 第一条已由 §9.196 在干净库上正面复现（0 行分区 `SET ACCESS METHOD columnar` 即复现）。
//   - 第二条的三个对照臂（直读分区带标签、直读分区不带标签、同分区在子查询里带标签）
//     全部正常 ⇒ 分区本身健康，问题特定于「视图的合成输出列」。
//
// ⚠ **不要用「重建库 / 回滚 765」把本门弄绿**（决策表 D30-a / D25-a）。
// 全 heap 的库上这些形状当然全通过，但那是**把生产形态换成了非生产形态**，
// 下次部署 migration 765 就复发。本门红的意义正是「这个库的存储形态与生产不一致」。
//
// # 与既有门的关系（不重复造轮子）
//
//   - `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable`（§9.184/§9.196）：
//     手写 5 对 session 族，跑两腿 UNION ALL 形状。**覆盖面窄，不删**——
//     它带 perminfoindex 的根因说明文案，是那道红门的解释出处。
//   - `retirement_exposure_attribution_gap_probe_test.go`（§9.195）：探针，量关系宇宙盲区。
//   - 本门：从 catalog 推导全集 + 新增「合成输出列」这一类故障 + 一个静态守卫。
//     前两者是**运行期**测量，本门最后一个子测试是**静态**的，
//     管的是「别再写出一个这样的查询」。

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ---------------------------------------------------------------------------
// SSOT：列存关系全集（含母表）
// ---------------------------------------------------------------------------

// columnarParentUniverseSQL 列出 public schema 下所有「自身是列存」或
// 「至少有一个列存分区」的关系，输出 schema 限定名。
//
// ⚠ 第二个 SELECT 里的 `colpart` 联接是 §9.197.3 自我更正的重点：
// 只按 `relam='columnar'` 收会**只拿到分区**，而业务查询读的是**母表**，
// 于是母表名匹配不上、整条查询一个候选都找不到 ⇒ 假零。
// （这与 D29-a 的「关系宇宙漏掉视图」是同一类错误。）
const columnarParentUniverseSQL = `
WITH colpart AS (
    SELECT DISTINCT i.inhparent AS parentoid
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_am am ON am.oid = c.relam
    JOIN pg_inherits i ON i.inhrelid = c.oid
    WHERE am.amname = 'columnar' AND n.nspname = 'public' AND c.relkind IN ('r','p')
)
SELECT pn.nspname || '.' || p.relname
FROM colpart cp
JOIN pg_class p ON p.oid = cp.parentoid
JOIN pg_namespace pn ON pn.oid = p.relnamespace
UNION
SELECT n.nspname || '.' || c.relname
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_am am ON am.oid = c.relam
WHERE am.amname = 'columnar' AND n.nspname = 'public' AND c.relkind IN ('r','p')
ORDER BY 1`

// columnarViewsOverColumnarSQL 列出所有定义里提到任一列存关系（含母表）的已部署视图，
// 附上它的输出列名。
const columnarViewsOverColumnarSQL = `
WITH colpart AS (
    SELECT DISTINCT i.inhparent AS parentoid, c.relname AS partname
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_am am ON am.oid = c.relam
    JOIN pg_inherits i ON i.inhrelid = c.oid
    WHERE am.amname = 'columnar' AND n.nspname = 'public' AND c.relkind IN ('r','p')
),
col AS (
    SELECT partname AS relname FROM colpart
    UNION
    SELECT p.relname FROM colpart cp JOIN pg_class p ON p.oid = cp.parentoid
    UNION
    SELECT c.relname
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_am am ON am.oid = c.relam
    WHERE am.amname = 'columnar' AND n.nspname = 'public' AND c.relkind IN ('r','p')
),
v AS (
    SELECT c.oid, c.relname
    FROM pg_class c
    JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind = 'v' AND n.nspname = 'public'
      AND EXISTS (
          SELECT 1 FROM col
          WHERE lower(pg_get_viewdef(c.oid, true)) LIKE '%' || lower(col.relname) || '%'
      )
)
SELECT vn.nspname, v.relname, a.attname
FROM v
JOIN pg_namespace vn ON vn.oid = (SELECT n.oid FROM pg_class c2 JOIN pg_namespace n ON n.oid = c2.relnamespace WHERE c2.oid = v.oid)
JOIN pg_attribute a ON a.attrelid = v.oid AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY 1, 2, a.attnum`

func openColumnarGatePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("TEST_DB_URL")
	}
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL / TEST_DB_URL 未设置 —— 列存面可服务性门未运行（非通过）")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	// 连不上必须**报红**：落到下面任何分类里都会被读成「形状可执行」。
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping 失败 —— 门无法测量（环境故障，不是「可服务」）：%v", err)
	}
	return pool
}

// classifyPlanErr 把一条查询的报错归类。**没有默认通过分支**。
func classifyPlanErr(err error) (outcome, detail string) {
	if err == nil {
		return "ok", ""
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "invalid perminfoindex"):
		return "PLAN FAIL(invalid perminfoindex 0 in RTE with relid 0)", firstLine(msg)
	case strings.Contains(msg, "cache lookup failed for attribute"):
		return "PLAN FAIL(cache lookup failed for attribute — 视图合成输出列)", firstLine(msg)
	case strings.Contains(msg, "does not exist"):
		return "MISSING RELATION", firstLine(msg)
	case strings.Contains(msg, "permission denied"):
		return "PERMISSION", firstLine(msg)
	case strings.Contains(msg, "canceling statement due to statement timeout"):
		return "TIMEOUT（未定：既不是通过也不是那两类故障）", firstLine(msg)
	default:
		return "UNCLASSIFIED FAIL —— 分类器缺这一类，请补而不是让它落进「可服务」", firstLine(msg)
	}
}

// ---------------------------------------------------------------------------
// 子测试 1：全集本身可不可信
// ---------------------------------------------------------------------------

// TestColumnarUniverseFromCatalog 是本门的**量具自证**。
//
// 它守的不是产品行为，是「下面两个子测试有没有在测东西」。
// §9.197.3 记过一次假零：全集按分区名收、母表名收不到，于是「0 个候选」
// 被读成「没有问题」。这个测试让那种假零**必然报红**。
func TestColumnarUniverseFromCatalog(t *testing.T) {
	pool := openColumnarGatePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rels := loadColumnarUniverse(ctx, t, pool)
	if len(rels) == 0 {
		t.Fatalf("列存关系全集为空 —— 这**不是**「没有问题」，是量具坏了。" +
			"（历史教训 §9.197.3：只按 relam='columnar' 收分区名、不收母表名时结果会偏小；" +
			"Citus 未启用时也确实为 0，但那属于环境不符，应显式说明而不是静默通过）")
	}
	t.Logf("列存关系全集（%d 个，含母表）：%s", len(rels), strings.Join(rels, ", "))

	// 阳性对照：全集里必须认得 migration 765 转换的那张表。
	// 认不得 ⇒ 查询漏了「母表」这一跳，正是造成假零的那一跳。
	var sawBodies bool
	for _, r := range rels {
		if r == "public.request_logs_bodies" {
			sawBodies = true
		}
	}
	if !sawBodies {
		t.Errorf("列存全集里没有 public.request_logs_bodies —— " +
			"全集推导漏了母表这一跳，下游子测试会得到**假零**而不是真结论")
	}
}

func loadColumnarUniverse(ctx context.Context, t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(ctx, columnarParentUniverseSQL)
	if err != nil {
		t.Fatalf("查询列存全集失败: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// 子测试 2：两腿 UNION ALL 形状（§9.196 的推广）
// ---------------------------------------------------------------------------

// TestColumnarParentTwoSurfaceSetopShape_RealDB 对**每一个**有 `_hot` 孪生的列存母表
// 跑生产同款形状。
//
// 与 `TestSessionFamilyTwoSurfaceUnionShapeIsExecutable` 的差别只有一处，
// 但那一处是全部：那份名单是手写的 5 对，本测试从 catalog 推导。
func TestColumnarParentTwoSurfaceSetopShape_RealDB(t *testing.T) {
	pool := openColumnarGatePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	universe := loadColumnarUniverse(ctx, t, pool)
	type row struct {
		parent, hot, outcome, detail string
	}
	var checked []row
	var twins int

	for _, parent := range universe {
		hot := hotTwinName(t, pool, parent)
		if hot == "" {
			continue // 没有 hot 孪生的表不存在「两面」这个形状，不测也不假装测过
		}
		twins++
		// `SELECT 1` 与列名解耦：同一份查询覆盖所有族。实测该写法与
		// 按列名写的复现性一致（审计 §9.197.2 V1）。
		q := fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s UNION ALL SELECT 1 FROM %s) s`, hot, parent)
		var n int64
		outcome, detail := classifyPlanErr(pool.QueryRow(ctx, q).Scan(&n))
		if outcome == "ok" {
			detail = fmt.Sprintf("%d 行", n)
		}
		checked = append(checked, row{parent, hot, outcome, detail})
	}

	if twins == 0 {
		t.Fatalf("列存母表里没有任何一个带 _hot 孪生 —— 下文 0/0 的「全通过」是**假绿**，请指名环境不符")
	}
	t.Logf("带 _hot 孪生的列存母表：%d 个", twins)

	var bad []string
	for _, r := range checked {
		if r.outcome != "ok" {
			bad = append(bad, fmt.Sprintf("  %-38s + %-42s ⇒ %s\n      %s",
				r.parent, r.hot, r.outcome, r.detail))
		}
	}
	if len(bad) > 0 {
		t.Errorf("以下列存母表**无法执行生产同款的两腿 UNION ALL 形状**（%d/%d 个带孪生的列存母表）：\n%s\n\n"+
			"根因（§9.196 已在干净库正面复现）：Citus `columnar` 关系出现在**未命名子查询**里\n"+
			"参与 `UNION ALL` ⇒ 计划期就抛 invalid perminfoindex。\n"+
			"⚠ 这**不是 request_logs_bodies 独有的**——全 heap 的 `request_logs` 在同一形状下\n"+
			"正常返回 2,184,300 行，所以它是 columnar 的性质，不是某张表的毛病。\n"+
			"⚠ 也**不是「子查询不能包列存表」**：顶层 UNION ALL、CTE、普通/嵌套子查询、\n"+
			"子查询内 JOIN、子查询内 UNION（去重）/EXCEPT 全部实测通过（审计 §9.197.2）。\n"+
			"处置属主决定（决策表 D30-a）：① 读法改两段式/顶层 UNION ALL，或 ② 回滚列存转换。\n"+
			"⚠ **不要**用重建库把本门弄绿（决策表 D25-a）：全 heap 的库当然全绿，\n"+
			"但那是把生产形态换掉了，下次部署 migration 765 就复发。",
			len(bad), twins, strings.Join(bad, "\n"))
	}
}

func hotTwinName(t *testing.T, pool *pgxpool.Pool, parent string) string {
	t.Helper()
	schema, rel, ok := strings.Cut(parent, ".")
	if !ok {
		schema, rel = "public", parent
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		                 WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r')`,
		schema, rel+"_hot").Scan(&exists)
	if err != nil {
		t.Fatalf("探测 %s 的 hot 孪生失败: %v", parent, err)
	}
	if !exists {
		return ""
	}
	return schema + "." + rel + "_hot"
}

// ---------------------------------------------------------------------------
// 子测试 3：视图的每一个输出列（§9.197 新发现的第二类故障）
// ---------------------------------------------------------------------------

// TestDeployedViewOverColumnarIsServable_RealDB 对每个「建在列存关系之上」的已部署视图，
// 逐列 EXPLAIN。
//
// 为什么必须**逐列**：视图里 `SELECT 'hot'::text AS source` 这类**算出来的**列
// 与基表列在计划期走的是不同路径。`SELECT id`、`SELECT count(*)` 都正常，
// 只有 `SELECT source` 挂——只测 `SELECT *` 或 `count(*)` 会把它整个漏过去。
func TestDeployedViewOverColumnarIsServable_RealDB(t *testing.T) {
	pool := openColumnarGatePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rows, err := pool.Query(ctx, columnarViewsOverColumnarSQL)
	if err != nil {
		t.Fatalf("查询列存之上的视图失败: %v", err)
	}
	type target struct{ schema, view, col string }
	var targets []target
	for rows.Next() {
		var sc, v, c string
		if err := rows.Scan(&sc, &v, &c); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		targets = append(targets, target{sc, v, c})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}

	if len(targets) == 0 {
		t.Fatalf("列存之上的视图输出列清单为空 —— 这是**量具坏了**（0 个候选不等于 0 个问题），"+
			"请检查 %s 的母表这一跳是否还在", "columnarViewsOverColumnarSQL")
	}
	views := map[string]bool{}
	for _, tg := range targets {
		views[tg.schema+"."+tg.view] = true
	}
	t.Logf("列存之上的已部署视图 %d 个，输出列合计 %d 个", len(views), len(targets))

	var bad []string
	for _, tg := range targets {
		q := fmt.Sprintf(`EXPLAIN (COSTS OFF) SELECT %q FROM %s.%s`, tg.col, tg.schema, tg.view)
		_, err := pool.Exec(ctx, q)
		outcome, detail := classifyPlanErr(err)
		if outcome != "ok" {
			bad = append(bad, fmt.Sprintf("  %s.%s.%s ⇒ %s\n      %s", tg.schema, tg.view, tg.col, outcome, detail))
		}
	}
	if len(bad) > 0 {
		t.Errorf("以下视图输出列**在列存布局上无法计划**（%d/%d 列，涉及视图 %d 个）：\n%s\n\n"+
			"这一类故障与 §9.196 的两腿 UNION ALL **无关**，是独立的一种：\n"+
			"视图 targetlist 里的**合成列**（`SELECT 'hot'::text AS source` 这种算出来的列，\n"+
			"不是基表列）在列存关系上投影时，会去找基表上并不存在的同名属性。\n"+
			"对照实测（同库、同分区，§9.197.4）：直读该列存分区带标签 ✓、不带标签 ✓、\n"+
			"放进子查询带标签 ✓ ⇒ 分区本身健康，问题特定于「视图的合成输出列」。\n"+
			"本机实测 8 视图 147 列中只有这一列失败，其余全部通过。\n"+
			"⚠ 现网读法暂时不碰它：`admin/errors_trend.go:239` 把视图包进子查询且只选基表列，\n"+
			"实测该形状正常。所以这是**潜伏**故障，不是正在冒烟的故障。\n"+
			"处置属主决定（决策表 D30-d）：改视图定义（去掉合成列或加 base.relname 限定）、\n"+
			"或让该列不落在列存面上。**不要**用重建库消掉——理由同 D25-a。",
			len(bad), len(targets), len(views), strings.Join(bad, "\n"))
	}
}

// ---------------------------------------------------------------------------
// 子测试 4：形状边界（防止把「结论」记错后误改好代码）
// ---------------------------------------------------------------------------

// TestColumnarSetopSubqueryIsTheNarrowTrigger_RealDB 把 §9.197.2 的形状矩阵固化成门。
//
// 它只断言**安全形状通过**。已知坏形状**不断言会坏**——
// 断言「bug 存在」是脆的：Citus 一旦修好，这道门会因为正确的理由变红。
// 坏形状由上面的子测试 2 以「读起来就是坏了」的方式覆盖。
//
// 这道门的作用是**防止结论被记错**：如果有人把根因记成
// 「子查询里不能读列存表」，就会去改那些其实好好的两段式查询。
func TestColumnarSetopSubqueryIsTheNarrowTrigger_RealDB(t *testing.T) {
	pool := openColumnarGatePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	universe := loadColumnarUniverse(ctx, t, pool)
	parent := ""
	for _, r := range universe {
		if r == "public.request_logs_bodies" {
			parent = r
		}
	}
	if parent == "" {
		t.Skipf("列存全集里没有 request_logs_bodies（本机库形态与生产不符），形状边界门未运行（非通过）")
	}
	hot := hotTwinName(t, pool, parent)
	if hot == "" {
		t.Fatalf("request_logs_bodies 没有 _hot 孪生，无法构造两腿形状")
	}

	safe := []struct {
		name string
		sql  string
	}{
		{"顶层 UNION ALL",
			fmt.Sprintf(`SELECT count(*) FROM %s UNION ALL SELECT count(*) FROM %s`, parent, hot)},
		{"已部署两腿视图（顶层 UNION ALL，LIMIT 1）",
			`SELECT count(*) FROM (SELECT * FROM public.request_logs_bodies_with_current_month LIMIT 1) z`},
		{"顶层 UNION ALL 三关系（列存夹在中间）",
			fmt.Sprintf(`SELECT count(*) FROM %s UNION ALL SELECT count(*) FROM %s UNION ALL SELECT count(*) FROM %s`,
				"public.request_logs", parent, hot)},
		{"普通子查询（无集合算子）",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s) s`, parent)},
		{"嵌套子查询",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM (SELECT 1 FROM %s) t) s`, parent)},
		{"CTE",
			fmt.Sprintf(`WITH c AS (SELECT 1 AS x FROM %s) SELECT count(*) FROM c`, parent)},
		// ⚠ 关联条件必须打在真实索引列上。第一版这里写的是 `ON true`——
		// 那是 4.7k × 224 万的**笛卡尔积**，单条跑掉 7 分半还没完，
		// 差点被当成「这个形状很慢」而不是「我的判据写错了」。
		{"子查询内 JOIN",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s a JOIN %s b ON a.request_id = b.request_id) s`, hot, parent)},
		{"子查询内 LEFT JOIN",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s a LEFT JOIN %s b ON a.request_id = b.request_id) s`, hot, parent)},
		{"子查询内带 LIMIT",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s LIMIT 3) s`, parent)},
		{"子查询内 UNION（去重，不是 ALL）",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s UNION SELECT 1 FROM %s) s`, parent, hot)},
		{"子查询内 EXCEPT",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s EXCEPT SELECT 1 FROM %s) s`, hot, parent)},
		{"子查询 + 顶层集合算子",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM %s) s UNION ALL SELECT count(*) FROM %s`, parent, hot)},
		{"heap 对照：两腿 UNION ALL 放进子查询",
			fmt.Sprintf(`SELECT count(*) FROM (SELECT 1 FROM public.request_logs_hot UNION ALL SELECT 1 FROM public.request_logs) s`)},
	}

	var bad []string
	for _, s := range safe {
		var n int64
		outcome, detail := classifyPlanErr(pool.QueryRow(ctx, s.sql).Scan(&n))
		if outcome != "ok" {
			bad = append(bad, fmt.Sprintf("  %-34s ⇒ %s\n      %s", s.name, outcome, detail))
		} else {
			t.Logf("  安全形状 %-34s ⇒ ok (%d)", s.name, n)
		}
	}
	if len(bad) > 0 {
		t.Errorf("以下**实测应当安全**的形状在本库上不可执行（%d/%d）：\n%s\n\n"+
			"这说明 §9.197.2 的形状边界**已经不成立**：要么库形态变了，要么 Citus 行为变了。\n"+
			"在这两种情况下，「把读法拆成两段式」这个处置的前提需要重新确认——\n"+
			"请先更新审计 §9.197.2 再改代码，**不要**照着旧结论继续改。",
			len(bad), len(safe), strings.Join(bad, "\n"))
	}
}

// ---------------------------------------------------------------------------
// 子测试 5：静态守卫——别再写出一个这样的查询
// ---------------------------------------------------------------------------

// setopKeywordRE 只认**独立词**的集合算子。
//
// ⚠ 曾经的版本用 `strings.Contains(sql, "except")`，结果把 plpgsql 的
// `EXCEPTION WHEN OTHERS` 当成了 SQL 的 `EXCEPT`，一次普查报出两条命中，
// 两条全是假的（§9.197.3）。这条注释留着就是为了不让它再回来。
var setopKeywordRE = regexpSetop()

// TestProductionGoSQLOverColumnarCandidatesAreVerified_RealDB 是**候选发现 + 真库裁判**门。
//
// # 为什么静态规则不能自己下结论
//
// 第一版把它写成纯静态规则（见到「列存关系 + 子查询内集合算子」就报红），
// 结果报出了 `bg/supplier_error_stats_aggregator.go:64`。手工复核发现那条**是好的**：
// 它的两条腿都带分区键谓词，真库 EXPLAIN 通过。§9.197.6 的三臂对照把判别条件钉死了——
//
//	两条腿都无谓词                        ⇒ FAIL
//	只在 hot 腿加谓词（列存腿仍裸读）      ⇒ FAIL
//	两条腿都加谓词                        ⇒ PASS（启用 Columnar Chunk Group Filters）
//
// ⇒ 真正的判别是「**列存腿自己**有没有谓词」，静态正则看不见这件事
// （它得知道哪个关系是列存的、哪条腿是列存腿、谓词落在哪条腿上）。
//
// ⇒ 所以分工改成：**静态只负责找候选，真库负责裁决**。
// 判据的观测量必须落在被检验对象之外——这里「真库能不能计划这条 SQL」就是那个外部观测量。
//
// # 口径
//
//   - 候选：生产 Go 字符串字面量，提到任一列存关系，且在**括号内部**出现集合算子。
//   - 裁决：把 `$n` 换成 `NULL` 后 EXPLAIN。**只有**两类报错算违规
//     （invalid perminfoindex / cache lookup failed for attribute）；
//     其余任何报错都归入「不可判定」并**指名列出**，绝不静默判过。
//   - 候选数下界断言：可裁决的候选至少 1 条，否则说明抽取器或参数替换坏了。
func TestProductionGoSQLOverColumnarCandidatesAreVerified_RealDB(t *testing.T) {
	pool := openColumnarGatePool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	universe := loadColumnarUniverse(ctx, t, pool)

	bare := make([]string, 0, len(universe))
	for _, r := range universe {
		_, rel, ok := strings.Cut(r, ".")
		if !ok {
			rel = r
		}
		if rel != "" {
			bare = append(bare, rel)
		}
	}
	if len(bare) == 0 {
		t.Fatalf("列存裸名清单为空 —— 量具坏了")
	}

	root := repoRootFromCaller(t)
	type cand struct {
		file   string
		line   int
		sql    string
		detail string
	}
	var candidates []cand

	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(p)
			if base == "vendor" || base == ".git" || base == "node_modules" || base == "installer" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			v, uerr := strconv.Unquote(bl.Value)
			if uerr != nil || !mentionsAnyRelation(v, bare) {
				return true
			}
			ds := setopInsideSubquery(v)
			if len(ds) == 0 {
				return true
			}
			rel, _ := filepath.Rel(root, p)
			candidates = append(candidates, cand{
				file: rel, line: fset.Position(bl.Pos()).Line, sql: v,
				detail: strings.Join(ds, "; "),
			})
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].file != candidates[j].file {
			return candidates[i].file < candidates[j].file
		}
		return candidates[i].line < candidates[j].line
	})
	t.Logf("静态候选（提到列存关系 + 子查询内集合算子）：%d 条", len(candidates))

	var violations, undecidable, safe []string
	for _, c := range candidates {
		q := "EXPLAIN (COSTS OFF) " + sqlParamToNull(c.sql)
		_, execErr := pool.Exec(ctx, q)
		switch {
		case execErr == nil:
			safe = append(safe, fmt.Sprintf("  %s:%d —— 实测可计划（%s）", c.file, c.line, c.detail))
		case strings.Contains(execErr.Error(), "invalid perminfoindex"),
			strings.Contains(execErr.Error(), "cache lookup failed for attribute"):
			violations = append(violations, fmt.Sprintf("  %s:%d —— %s\n      %s",
				c.file, c.line, c.detail, firstLine(execErr.Error())))
		default:
			// ⚠ 不可判定 ≠ 通过。名字必须出现在输出里，否则它就变成一个静默的洞。
			undecidable = append(undecidable, fmt.Sprintf("  %s:%d —— %s\n      %s",
				c.file, c.line, c.detail, firstLine(execErr.Error())))
		}
	}
	for _, s := range safe {
		t.Logf("候选实测安全：%s", s)
	}
	if len(undecidable) > 0 {
		t.Logf("⚠ 以下候选**不可判定**（既没被判违规也没被判安全，不等于通过）：\n%s",
			strings.Join(undecidable, "\n"))
	}

	// 候选数下界：证明「抽取 + 参数替换 + EXPLAIN」这条链真的跑起来了。
	// 没有这条断言，一个改坏的抽取器会安静地报「0 候选、0 违规」。
	if len(safe)+len(violations) == 0 {
		t.Errorf("没有任何候选被真库裁决（候选 %d 条，安全 %d，违规 %d）—— "+
			"要么抽取器坏了，要么参数替换让每条都不可判定。**不允许**把「0 违规」直接读成「没问题」",
			len(candidates), len(safe), len(violations))
	}

	if len(violations) > 0 {
		t.Errorf("以下生产 SQL 在**本库列存布局上无法计划**（%d 条）：\n%s\n\n"+
			"已在本机 7 个列存母表上逐一实测确认。修法见 §9.196 的 "+
			"`validate_sessions_v2/loader.go`：把两腿**顺序**执行，或改成顶层 UNION ALL。\n"+
			"⚠ 判别条件不是「子查询里不能有集合算子」：给**列存腿自己**加上分区键谓词即可通过\n"+
			"（§9.197.6 三臂对照），所以别为了过门把本来正常的查询拆掉。",
			len(violations), strings.Join(violations, "\n"))
	}
}

// sqlParamToNull 把 pgx 的 `$n` 占位符换成字面 NULL，让语句能在 EXPLAIN 下解析。
//
// 换 NULL 而不是猜类型：`$1::interval` 这种带显式转换的位置由类型信息自己定下来；
// 没有转换的位置 PG 会按 text 推断，那类失败会被分类器归入「不可判定」并被指名。
var sqlParamRe = regexp.MustCompile(`\$\d+`)

func sqlParamToNull(s string) string { return sqlParamRe.ReplaceAllString(s, "NULL") }

// mentionsAnyRelation 判断 SQL 文本里有没有以**独立词**形式提到任一关系名。
// isIdentByte 复用 admin 包里既有的那个（见 view_source_column_contract_test.go）——
// 它把 `.` 排除在标识符之外，所以 `public.request_logs_bodies` 里的裸名能匹配上。
func mentionsAnyRelation(sql string, names []string) bool {
	lower := strings.ToLower(sql)
	for _, n := range names {
		ln := strings.ToLower(n)
		for i := 0; ; {
			j := strings.Index(lower[i:], ln)
			if j < 0 {
				break
			}
			at := i + j
			beforeOK := at == 0 || !isIdentByte(lower[at-1])
			end := at + len(ln)
			afterOK := end >= len(lower) || !isIdentByte(lower[end])
			if beforeOK && afterOK {
				return true
			}
			i = at + 1
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 集合算子扫描器
// ---------------------------------------------------------------------------

// regexpSetop 只认**整个词**就是集合算子的情况。
//
// ⚠ 曾经的版本用 `strings.Contains(sql, "except")`，把 plpgsql 的
// `EXCEPTION WHEN OTHERS` 当成了 SQL 的 `EXCEPT`——§9.197.3 的一次普查因此
// 报出两条命中，两条都是假的。`^...$` 的锚定就是为这条留下的。
func regexpSetop() *regexp.Regexp {
	return regexp.MustCompile(`^(union|intersect|except)$`)
}

// setopInsideSubquery 报告 SQL 里所有「处在括号内部的集合算子」。
//
// 它**必须**跳过：单引号串（含双写单引号转义）、双引号标识符、行注释、块注释。
// 不跳的话，一段注释里写着 UNION ALL 就会被当成违规。
//
// 深度语义：左括号使深度 +1。因此
//   - `FROM (SELECT ... UNION ALL SELECT ...) s` ⇒ 深度 1 ⇒ 命中；
//   - 顶层 `A UNION ALL B` ⇒ 深度 0 ⇒ 不命中（实测安全）；
//   - `IN (SELECT ...)` 里的 UNION 也会命中。
//
// ⚠ 本函数是**探测器**，不是判据：它**故意比实测粗**（§9.197.6 实测表明
// 「列存腿自己带分区键谓词」时同样形状是可计划的，而这件事静态看不见）。
// 所以宁可多报——多报的代价只是多跑一次 EXPLAIN，
// 而漏报的代价是一个**永远不会有人去查**的洞。
// 最终结论由 TestProductionGoSQLOverColumnarCandidatesAreVerified_RealDB
// 让真库裁决。
func setopInsideSubquery(sql string) []string {
	var out []string
	depth, i, n := 0, 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == '\'':
			i = skipSQLQuoted(sql, i, '\'')
			continue
		case c == '"':
			i = skipSQLQuoted(sql, i, '"')
			continue
		case c == '-' && i+1 < n && sql[i+1] == '-':
			for i < n && sql[i] != '\n' {
				i++
			}
			continue
		case c == '/' && i+1 < n && sql[i+1] == '*':
			j := strings.Index(sql[i+2:], "*/")
			if j < 0 {
				i = n
			} else {
				i += 2 + j + 2
			}
			continue
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case isWordStart(c):
			j := i
			for j < n && isWordChar(sql[j]) {
				j++
			}
			word := strings.ToLower(sql[i:j])
			if depth > 0 && setopKeywordRE.MatchString(word) {
				out = append(out, fmt.Sprintf("括号深度 %d 处出现集合算子 %s", depth, strings.ToUpper(word)))
			}
			i = j
			continue
		}
		i++
	}
	return out
}

func skipSQLQuoted(s string, i int, quote byte) int {
	i++ // 开引号
	for i < len(s) {
		if s[i] == quote {
			// '' 是转义，不是结束
			if i+1 < len(s) && s[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(s)
}

func isWordStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isWordChar(c byte) bool {
	return isWordStart(c) || (c >= '0' && c <= '9') || c == '_'
}
