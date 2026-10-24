package admin

// turns_sessions_enrich_latest_test.go —— R93 的契约门：
// 会话列表页的 **api_key 回填查询**（enrich）必须走「每个会话最新一轮」的形态，
// 不得再走「视图 ⋈ 全量 history source」⇒ 后者把**同一批行扫两遍**再 1:1 join。
//
// # 一句话背景（★ 全部为 2026-10-24 生产只读实测，结论可复算）
//
// 【症状】GET /api/admin/turns/sessions 带 `?model=` 时生产**恒 500@30s**。
//
//	★ ★★ 但 **R90/R92 修的是【列表查询】**，而 500 一直由**另一条语句**造成：
//	生产 journal（build 2842）：
//	  WARN admin handleTurnsSessions enrich meta failed
//	       err=iterate api_key enrich failed: … statement timeout (57014)
//	⇒ ⇒ ★ **「按端点归因」≠「按 SQL 语句归因」** ⇒ 必须先定位「失败的语句」。
//
// 【R90/R92 的修法确实生效】列表查询 **17,501 ms → 342 ms**（生产实测）。
//
// 【真因】旧 enrich 形态：
//
//	FROM public.session_turns_with_current_month t          -- 视图
//	JOIN <hot ∪ 全部历史, 113 列投影> rl ON rl.request_id = t.request_id
//
//	★ 视图的**名字骗人**：父表腿实测是 `FROM session_turns`（**全部分区**）
//	  ⇒ 视图 = hot ∪ 全部历史
//	  ⇒ 视图侧 `Heap Fetches 86,507` ＋ source 侧同样 86,507 行
//	  ⇒ 再按 `request_id` **1:1 join**（**join 冗余**）。
//	★ `?model=` 命中的 21 个**探针会话**在 2026_09 有 **85,712 轮**
//	  ⇒ 该查询实测 **13,066 ms**；无过滤批次只有 21×1 轮 ⇒ **23 ms**。
//
// 【新形态】每个 source 先 `GROUP BY session_id + max(turn_no)`（index-only，不回表）
//	再回定位那一行 ⇒ 实测同批次 **1,339 ms**（**9.8×**）。
//
// # 这道门钉什么
//
//	① **形态门**：生产用的 enrich 查询
//	   - 不得引用 `session_turns_with_current_month`（它 = hot ∪ 全部历史 ⇒ 全量物化）；
//	   - 不得引用 `session_turn_details`（113 列 source 的 details JOIN，纯浪费）；
//	   - 必须含 `max(turn_no)` + `GROUP BY session_id`（**聚合下推**）；
//	   - 必须含 api_key 归一后的 `IS NOT NULL` 过滤（★ 漏掉它 ⇒ 口径漂移，
//	     生产实测候选形态出现 `only_new=20 / only_old=0`）。
//	② **旧形态不得被生产引用**（go/ast 扫生产文件 ⇒ 旧形态只允许出现在门禁里）。
//	③ ★★ **等价门（真库）**：在**同一批次**上用旧形态与新形态各跑一次，
//	   **双向 EXCEPT ALL** 必须两边都为 0；
//	   ★ 且**要求两侧行数 > 0** ⇒ 空集**不算通过**（显式 SKIP 并写明理由）——
//	   ★ 纪律（LESSONS §354.2）：「空集」不能自证「等价」。
//
// # 判据纪律（本门自身）
//
// 每条**负向**断言都配一条**阳性对照**：先证明检测器能认得出「不合格」
// （喂一份合成样本，断言它被判红），再证明真实现被判绿。

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── ① 形态门 ───────────────────────────────────────────────────────────────

// r93CheckEnrichShape 校验 enrich 查询的渲染形态，返回问题描述列表（空 = 合格）。
func r93CheckEnrichShape(sqlText string) []string {
	var problems []string

	if strings.Contains(sqlText, "session_turns_with_current_month") {
		problems = append(problems, "引用了 session_turns_with_current_month —— "+
			"它的父表腿实测是 `FROM session_turns`（全部分区）⇒ 视图 = hot ∪ 全部历史 ⇒ "+
			"同一批行会被扫两遍 ⇒ 生产 13,066 ms ⇒ 端点 500@30s")
	}
	if strings.Contains(sqlText, "session_turn_details") {
		problems = append(problems, "引用了 session_turn_details —— "+
			"那是 113 列全量 source 的 details JOIN，本查询根本用不到 ⇒ 纯浪费")
	}
	for _, need := range []struct {
		needle string
		want   int
		why    string
	}{
		{"max(turn_no)", 2, "缺「按会话聚合出最大 turn_no」—— 没有它就会物化该会话的全部轮次"},
		{"GROUP BY session_id", 2, "缺「按会话分组」—— 同上"},
		{"AND t.api_key_id ~ '^[0-9]+$'", 2,
			"缺「裸正则」形式的 api_key 过滤 —— ★ 必须是裸正则：" +
				"CASE 形式用不上迁移 906 的部分索引（PG 的谓词蕴含判定是语法的）" +
				"⇒ 探针批次退回 1,350~1,569 ms（实测）"},
		{"IS NOT NULL", 1,
			"IS NOT NULL 只应剩外层那处（x.api_key_id IS NOT NULL）—— " +
				"聚合腿里若还有 IS NOT NULL ⇒ 说明过滤被写回 CASE 形式 ⇒ 用不上部分索引"},
		{"public.session_turns_hot", 2, "缺 hot 腿（或只写了一处 FROM/JOIN）"},
		{"public.session_turns t", 2, "缺父表腿（或只写了一处 FROM/JOIN）"},
	} {
		got := strings.Count(sqlText, need.needle)
		if got != need.want {
			problems = append(problems, fmt.Sprintf(
				"%q 出现 %d 次（必须恰好 %d）—— %s", need.needle, got, need.want, need.why))
		}
	}
	return problems
}

func TestR93_EnrichQueryShape(t *testing.T) {
	if problems := r93CheckEnrichShape(turnsSessionsAPIKeyEnrichSQL("")); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("enrich 查询形态不合格：%s", p)
		}
	}
}

// TestR93_EnrichShapeDetectorHasTeeth 阳性对照：坏样本必须被判红。
func TestR93_EnrichShapeDetectorHasTeeth(t *testing.T) {
	good := turnsSessionsAPIKeyEnrichSQL("")
	if problems := r93CheckEnrichShape(good); len(problems) != 0 {
		t.Fatalf("阳性对照失败：真形态被判为不合格 —— 检测器坏了：%v", problems)
	}
	// 坏样本①：退回旧形态（视图 ⋈ 全量 source）。
	bad1 := turnsSessionsAPIKeyEnrichLegacySQL("")
	if len(r93CheckEnrichShape(bad1)) == 0 {
		t.Error("坏样本①（旧形态：视图 + 全量 source）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本②：去掉聚合（退化成物化全部轮次）。
	bad2 := strings.ReplaceAll(good, "max(turn_no)", "turn_no")
	if len(r93CheckEnrichShape(bad2)) == 0 {
		t.Error("坏样本②（去掉 max(turn_no) 聚合）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本③：漏掉 api_key 过滤（★ 口径漂移的那个形态）。
	bad3 := strings.ReplaceAll(good, " IS NOT NULL", "")
	if len(r93CheckEnrichShape(bad3)) == 0 {
		t.Error("坏样本③（漏掉 IS NOT NULL）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本④：**只**去掉一条腿的聚合（⇒ 形态门必须按【次数】判定，不能只判「存在」）。
	bad4 := strings.Replace(good, "max(turn_no)", "turn_no", 1)
	if len(r93CheckEnrichShape(bad4)) == 0 {
		t.Error("坏样本④（只去掉一条腿的 max(turn_no)）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑤：**只**去掉一条腿的 IS NOT NULL（同上）。
	bad5 := strings.Replace(good, " IS NOT NULL", "", 1)
	if len(r93CheckEnrichShape(bad5)) == 0 {
		t.Error("坏样本⑤（只去掉一条腿的 IS NOT NULL）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑥：★ **把聚合腿的过滤写回 CASE 形式**（R94 的核心风险）。
	//   两种写法**口径等价**，但只有裸正则能被规划器认出来 ⇒ 只有 CASE 形式时
	//   迁移 906 的部分索引**用不上** ⇒ 探针批次退回 1,350~1,569 ms（实测）。
	bad6 := strings.ReplaceAll(good, "AND t.api_key_id ~ '^[0-9]+$'",
		"AND (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) IS NOT NULL")
	if bad6 == good {
		t.Fatal("坏样本⑥构造失败：good 里找不到裸正则过滤子串 —— 形态门与实现已脱钩")
	}
	if len(r93CheckEnrichShape(bad6)) == 0 {
		t.Error("坏样本⑥（过滤写回 CASE 形式 ⇒ 用不上部分索引）被判合格 —— 检测器没有鉴别力")
	}
	// 坏样本⑦：**只**把一条腿写回 CASE 形式（⇒ 必须按【次数】判定）。
	bad7 := strings.Replace(good, "AND t.api_key_id ~ '^[0-9]+$'",
		"AND (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) IS NOT NULL", 1)
	if len(r93CheckEnrichShape(bad7)) == 0 {
		t.Error("坏样本⑦（只把一条腿写回 CASE 形式）被判合格 —— 检测器没有鉴别力")
	}
}

// ── ② 旧形态不得被生产引用（go/ast） ────────────────────────────────────────

// r93CountLegacyCalls 统计源码里对旧形态函数的调用次数。
//
// ★★★ **必须同时认两种调用形态**（★ 这是本门被变异臂 M4 打穿后的修复）：
//
//	a. `pkg.Func(...)` ⇒ call.Fun 是 *ast.SelectorExpr
//	b. `Func(...)`     ⇒ call.Fun 是 *ast.Ident（本文件的旧形态函数是**本地函数**）
//
// ★ 只认 (a) 会**漏掉本地函数调用** ⇒ 门恒绿 ⇒ ★ **断言的「识别面」必须覆盖被测对象的全部写法**。
func r93CountLegacyCalls(t *testing.T, filename, src string) int {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("解析 %s: %v", filename, err)
	}
	calls := 0
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var name string
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		case *ast.Ident:
			name = fn.Name
		default:
			return true
		}
		if name == "turnsSessionsAPIKeyEnrichLegacySQL" {
			calls++
		}
		return true
	})
	return calls
}

// TestR93_LegacyShapeNotUsedInProduction 用 go/ast 扫**生产文件**，
// 断言没有调用 `turnsSessionsAPIKeyEnrichLegacySQL`。
//
// ★ 为什么必须用 go/ast 而不是 grep：源码里到处是**解释**旧形态的注释，
//
//	grep 会命中注释 ⇒ 门恒绿（或恒红，取决于写法）。
func TestR93_LegacyShapeNotUsedInProduction(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	prodFile := filepath.Join(wd, "turns_sessions.go")
	src, err := os.ReadFile(prodFile)
	if err != nil {
		t.Fatalf("读 %s: %v", prodFile, err)
	}
	if calls := r93CountLegacyCalls(t, prodFile, string(src)); calls > 0 {
		t.Errorf("生产文件 turns_sessions.go 里调用了 turnsSessionsAPIKeyEnrichLegacySQL（%d 处）—— "+
			"旧形态会把同一批行扫两遍 ⇒ 生产 13,066 ms ⇒ 端点 500@30s", calls)
	} else {
		t.Log("生产文件未引用旧形态 ✓")
	}
}

// TestR93_LegacyCallDetectorHasTeeth 阳性对照：
// 合成样本里**两种调用形态**都必须被检出（★ 本门曾因只认一种而恒绿）。
func TestR93_LegacyCallDetectorHasTeeth(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"本地函数调用", "package p\nfunc f() { q := turnsSessionsAPIKeyEnrichLegacySQL(\"\") ; _ = q }\n", 1},
		{"包限定调用", "package p\nfunc f() { q := db.turnsSessionsAPIKeyEnrichLegacySQL(\"\") ; _ = q }\n", 1},
		{"两处混合", "package p\nfunc f() {\n a := turnsSessionsAPIKeyEnrichLegacySQL(\"\")\n b := db.turnsSessionsAPIKeyEnrichLegacySQL(\"\")\n _, _ = a, b\n}\n", 2},
		{"只有注释提及（不该被计入）", "package p\n// turnsSessionsAPIKeyEnrichLegacySQL 只是注释\nfunc f() {}\n", 0},
		{"干净的源码", "package p\nfunc f() { q := turnsSessionsAPIKeyEnrichSQL(\"\") ; _ = q }\n", 0},
	}
	for _, c := range cases {
		got := r93CountLegacyCalls(t, "synthetic.go", c.src)
		if got != c.want {
			t.Errorf("%s：检出 %d 次（期望 %d）—— 检测器的识别面不对", c.name, got, c.want)
		}
	}
}

// ── ③ ★★ 等价门（真库） ────────────────────────────────────────────────────

// TestR93_RealDB_EnrichNewEqualsLegacy 在真库上用**同一批 session_id**
// 分别跑旧形态与新形态，**双向 EXCEPT ALL** 必须都为 0。
//
// ★ 纪律：★ **要求两侧行数 > 0** ⇒ 空集**不算通过**（显式 SKIP 并写明理由）。
//
//	（生产实测：探针批次两侧都是 0 行；等价性必须由**有返回**的批次承担。）
func TestR93_RealDB_EnrichNewEqualsLegacy(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 显式 SKIP，不是「通过」")
	}

	// 取一批**真实且轮次多**的会话（模拟端点命中的那批：多轮 ⇒ 最容易暴露口径差）。
	rows, err := pool.Query(ctx, `
		SELECT session_id FROM public.session_turns
		WHERE api_key_id ~ '^[0-9]+$'
		GROUP BY session_id
		ORDER BY count(*) DESC, session_id
		LIMIT 21`)
	if err != nil {
		t.Fatalf("取候选会话失败: %v", err)
	}
	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			t.Fatalf("scan 候选会话: %v", err)
		}
		ids = append(ids, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("迭代候选会话: %v", err)
	}
	if len(ids) == 0 {
		t.Skip("真库没有可用的候选会话 ⇒ 显式 SKIP，不是「通过」")
	}

	newSQL := turnsSessionsAPIKeyEnrichSQL("")
	oldSQL := turnsSessionsAPIKeyEnrichLegacySQL("")

	q := `
		WITH n AS (` + newSQL + `), o AS (` + oldSQL + `)
		SELECT
		  (SELECT count(*) FROM (SELECT * FROM n EXCEPT ALL SELECT * FROM o) x) AS only_new,
		  (SELECT count(*) FROM (SELECT * FROM o EXCEPT ALL SELECT * FROM n) x) AS only_old,
		  (SELECT count(*) FROM n) AS new_rows,
		  (SELECT count(*) FROM o) AS old_rows`

	var onlyNew, onlyOld, newRows, oldRows int64
	if err := pool.QueryRow(ctx, q, ids).Scan(&onlyNew, &onlyOld, &newRows, &oldRows); err != nil {
		t.Fatalf("等价查询失败: %v", err)
	}
	t.Logf("候选会话 %d 个 ⇒ 新形态 %d 行 / 旧形态 %d 行，only_new=%d / only_old=%d",
		len(ids), newRows, oldRows, onlyNew, onlyOld)

	if newRows == 0 || oldRows == 0 {
		t.Skipf("两侧存在空集（新 %d 行 / 旧 %d 行）⇒ 空集不能自证等价，显式 SKIP 不是「通过」",
			newRows, oldRows)
	}
	if onlyNew != 0 {
		t.Errorf("新形态多出 %d 行（≠旧形态）⇒ 口径漂移", onlyNew)
	}
	if onlyOld != 0 {
		t.Errorf("旧形态多出 %d 行（≠新形态）⇒ 口径漂移", onlyOld)
	}
}

// TestR93_RealDB_EnrichNewIsExecutable 真库执行：新形态必须**真的跑得通**
// （列名/别名错误只有运行期才暴露 ⇒ 静态门证明不了）。
func TestR93_RealDB_EnrichNewIsExecutable(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 显式 SKIP，不是「通过」")
	}
	var ids []string
	rows, err := pool.Query(ctx, `
		SELECT session_id FROM public.session_turns
		WHERE api_key_id ~ '^[0-9]+$'
		GROUP BY session_id ORDER BY count(*) DESC LIMIT 21`)
	if err != nil {
		t.Fatalf("取候选会话失败: %v", err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, s)
	}
	rows.Close()
	if len(ids) == 0 {
		t.Skip("真库没有可用的候选会话 ⇒ 显式 SKIP，不是「通过」")
	}

	q := `SELECT count(*) FROM (` + turnsSessionsAPIKeyEnrichSQL("") + `) z`
	var n int64
	if err := pool.QueryRow(ctx, q, ids).Scan(&n); err != nil {
		t.Fatalf("新形态在真库上执行失败（⇒ 端点 500）: %v", err)
	}
	t.Logf("新形态在真库上执行成功，返回 %d 行", n)
	_ = fmt.Sprint(n)
}

// ── ④ ★★ R94 真库门：部分索引必须**真的**被 enrich 查询用上 ──────────────────
//
// 【为什么需要这道门】R94 的修法依赖一个**很细**的前提：
//
//	PG 的「谓词蕴含」判定是**语法的** ⇒ 它能认出
//	  `WHERE api_key_id ~ '^[0-9]+$'` 蕴含部分索引的谓词 `WHERE api_key_id ~ '^[0-9]+$'`
//	但**认不出** CASE 形式
//	  `WHERE (CASE WHEN api_key_id ~ '^[0-9]+$' THEN api_key_id::bigint END) IS NOT NULL`
//
// ⇒ ★ 两者**口径完全等价**，但**性能差 295×**（1,350~1,569 ms vs 4.57 ms）。
// ⇒ ⇒ 这道门证明的是「**规划器认得这个谓词**」，而不是「SQL 长得对」——
//
//	静态断言（字符串计数）证明不了这一条，**只有真库的 EXPLAIN 能证明**。
//
// 【零副作用】建索引与 EXPLAIN 都在**同一个事务**里 ⇒ 结束前 ROLLBACK
//
//	⇒ 真库上不留下任何索引（★ 门可安全指向任何库）。
//
// 【阳性对照】同时用 CASE 形式跑一遍 ⇒ 必须出现 `Filter: ... api_key_id ...`
//
//	⇒ 证明「有 Filter」与「无 Filter」确实可区分 ⇒ 检测器本身有鉴别力。
func TestR94_RealDB_PartialIndexIsUsedByEnrichQuery(t *testing.T) {
	dsn := r90DSN(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()
	if !r90HasSessionFamily(ctx, pool) {
		t.Skip("真库没有会话族四张表 ⇒ 显式 SKIP，不是「通过」")
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// 强制走索引（本地库很小 ⇒ 不然规划器可能选顺序扫）。
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatalf("SET enable_seqscan: %v", err)
	}
	for _, ddl := range []string{
		`CREATE INDEX IF NOT EXISTS idx_session_turns_api_key_numeric
		    ON public.session_turns (session_id, turn_no DESC) WHERE api_key_id ~ '^[0-9]+$'`,
		`CREATE INDEX IF NOT EXISTS idx_session_turns_hot_api_key_numeric
		    ON public.session_turns_hot (session_id, turn_no DESC) WHERE api_key_id ~ '^[0-9]+$'`,
	} {
		if _, err := tx.Exec(ctx, ddl); err != nil {
			t.Fatalf("建部分索引失败: %v", err)
		}
	}

	explain := func(sqlText string, ids []string) string {
		rows, err := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sqlText, ids)
		if err != nil {
			t.Fatalf("EXPLAIN 失败: %v", err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatalf("scan EXPLAIN: %v", err)
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("迭代 EXPLAIN: %v", err)
		}
		return sb.String()
	}

	ids := []string{"__r94_probe_nonexistent__"}

	// ① 正向：裸正则形式 ⇒ 聚合腿的扫描节点
	//    · 必须是 **Index Only Scan**（不回表）；
	//    · 且**不能**带 api_key_id 的 Filter（谓词被部分索引吃掉 ⇒ 不用回表判定）。
	//
	// ★★ 判据为什么必须定位到「聚合腿」而不是「整份计划」：
	//   外层的 `WHERE x.api_key_id IS NOT NULL` 会被 PG **下推**到「回定位腿」
	//   （每个会话只取一行 ⇒ 20 行 ⇒ 无害），那份计划里**确实**有一处
	//   `Filter: ... api_key_id ...` ⇒ **整份计划里找 Filter 会误判**。
	//   ★ 真正的分水岭是**聚合腿**（那个要扫**全部候选行**的子扫描）。
	posPlan := explain(turnsSessionsAPIKeyEnrichSQL(""), ids)
	t.Logf("裸正则形态计划：\n%s", posPlan)
	aggScans := r94AggregateChildScans(posPlan)
	if len(aggScans) == 0 {
		t.Errorf("裸正则形态的计划里找不到聚合腿的扫描节点 —— 判据失效，请人工复核：\n%s", posPlan)
	} else {
		if !r94ScansHasIndexOnly(aggScans) {
			t.Errorf("裸正则形态的聚合腿里没有 Index Only Scan ⇒ 仍要回表判定 api_key_id ⇒ 收益消失：\n%s", posPlan)
		}
		if r94ScansHaveAPIKeyFilter(aggScans) {
			t.Errorf("裸正则形态的聚合腿仍有 api_key_id 的 Filter ⇒ 谓词**没**被索引吃掉"+
				"（说明过滤被写回 CASE 形式 ⇒ 扫全部候选行）：\n%s", posPlan)
		}
	}

	// ② 阳性对照：CASE 形式（口径等价但规划器认不出）⇒ 聚合腿**必须**出现
	//    api_key_id 的 Filter ⇒ 证明「检测器有鉴别力」。
	//    ★ 若 CASE 形态也走 Index Only Scan 且无 Filter，说明本门根本测不出
	//      谓词耦合 ⇒ 门无牙。
	caseSQL := strings.ReplaceAll(turnsSessionsAPIKeyEnrichSQL(""),
		"AND t.api_key_id ~ '^[0-9]+$'",
		"AND (CASE WHEN t.api_key_id ~ '^[0-9]+$' THEN t.api_key_id::bigint END) IS NOT NULL")
	if caseSQL == turnsSessionsAPIKeyEnrichSQL("") {
		t.Fatal("阳性对照构造失败：找不到裸正则过滤子串 —— 实现已漂移")
	}
	negPlan := explain(caseSQL, ids)
	t.Logf("CASE 形式计划：\n%s", negPlan)
	negScans := r94AggregateChildScans(negPlan)
	if len(negScans) == 0 {
		t.Errorf("阳性对照失败：CASE 形态的计划里找不到聚合腿的扫描节点 —— 判据失效：\n%s", negPlan)
	} else if !r94ScansHaveAPIKeyFilter(negScans) {
		t.Errorf("阳性对照失败：CASE 形态的**聚合腿**里**没有** api_key_id 的 Filter —— "+
			"说明本门测不出谓词耦合（检测器无鉴别力）：\n%s", negPlan)
	}
}

// r94ScanNode 是 EXPLAIN 计划里的一个扫描节点（及其自带的 Filter）。
type r94ScanNode struct {
	kind   string // "Index Only Scan" / "Index Scan" / "Seq Scan" / ...
	filter string // 该扫描节点自己的 Filter（空 = 无）
}

// r94AggregateChildScans 收集**聚合腿**（GroupAggregate / HashAggregate 子树里）
// 的全部扫描节点。
//
// ★★ 为什么必须按「聚合节点的子树」定位（而不是整份计划）：
//
//	外层的 `WHERE x.api_key_id IS NOT NULL` 会被 PG **下推**到「回定位腿」
//	（每个会话只取一行 ⇒ 20 行 ⇒ 无害）⇒ **整份计划**里必然有一处
//	`Filter: ... api_key_id ...` ⇒ 在整份计划里找 Filter **必然误判**。
//	★ 真正的分水岭：聚合腿（那个要扫**全部候选行**的子扫描）里还有没有那个 Filter
//	  —— 有 ⇒ 谓词没进索引 ⇒ 仍然要回表扫全部候选行。
//
// ★ 实现：EXPLAIN 的缩进即树形 ⇒ 聚合节点**之后、缩进更深**的行才是它的子树；
//
//	缩进回退即出子树。★ 回定位腿与聚合腿是**兄弟**（同缩进）⇒ 天然被排除。
func r94AggregateChildScans(plan string) []r94ScanNode {
	lines := strings.Split(plan, "\n")
	var out []r94ScanNode
	for i, line := range lines {
		if !strings.Contains(line, "GroupAggregate") && !strings.Contains(line, "HashAggregate") {
			continue
		}
		aggIndent := r94Indent(line)
		for j := i + 1; j < len(lines); j++ {
			sub := lines[j]
			if strings.TrimSpace(sub) == "" {
				continue
			}
			if r94Indent(sub) <= aggIndent {
				break // 出了聚合节点的子树
			}
			st := strings.TrimSpace(sub)
			kind := r94ScanKind(st)
			if kind == "" {
				continue
			}
			node := r94ScanNode{kind: kind}
			// 该扫描节点自己的 Filter：在它之后、下一个节点之前的 Filter: 行。
			for k := j + 1; k < len(lines); k++ {
				ft := strings.TrimSpace(lines[k])
				if ft == "" {
					continue
				}
				if strings.Contains(ft, "Filter:") {
					node.filter = ft
					break
				}
				if !strings.Contains(ft, ":") {
					break // 下一个节点
				}
			}
			out = append(out, node)
		}
	}
	return out
}

// r94ScanKind 判断一行是否是扫描节点；是则返回其种类，否则返回 ""。
func r94ScanKind(trimmed string) string {
	for _, k := range []string{"Index Only Scan", "Index Scan", "Seq Scan", "Bitmap Index Scan", "Bitmap Heap Scan", "Tid Scan"} {
		if strings.Contains(trimmed, k) {
			return k
		}
	}
	return ""
}

// r94Indent 数一行的前导空格数（EXPLAIN 的缩进即树形）。
func r94Indent(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' {
			break
		}
		n++
	}
	return n
}

// hasIndexOnly 是否存在 Index Only Scan。
func (n r94ScanNode) hasIndexOnly() bool { return n.kind == "Index Only Scan" }

// hasAPIKeyFilter 该扫描节点的 Filter 是否作用在 api_key_id 上。
func (n r94ScanNode) hasAPIKeyFilter() bool {
	return strings.Contains(n.filter, "Filter:") && strings.Contains(n.filter, "api_key_id")
}

// r94ScansHasIndexOnly 批量判断。
func r94ScansHasIndexOnly(ns []r94ScanNode) bool {
	for _, n := range ns {
		if n.hasIndexOnly() {
			return true
		}
	}
	return false
}

// r94ScansHaveAPIKeyFilter 批量判断。
func r94ScansHaveAPIKeyFilter(ns []r94ScanNode) bool {
	for _, n := range ns {
		if n.hasAPIKeyFilter() {
			return true
		}
	}
	return false
}
