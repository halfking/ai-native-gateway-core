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
		{"IS NOT NULL", 3, "缺 api_key 归一后的 IS NOT NULL 过滤 —— " +
			"漏掉它会让口径漂移（实测候选形态 only_new=20 / only_old=0）"},
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
//   grep 会命中注释 ⇒ 门恒绿（或恒红，取决于写法）。
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
//   （生产实测：探针批次两侧都是 0 行；等价性必须由**有返回**的批次承担。）
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
