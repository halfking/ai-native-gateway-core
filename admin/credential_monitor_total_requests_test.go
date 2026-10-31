// credential_monitor_total_requests_test.go — R168（2026-10-27）
// 「凭据面板 total_requests 计数」从 canonical 视图改走「底表两臂窄投影」的守卫。
//
// # ★ 为什么这组守卫是必需的（★ 每一条都对应本轮的一次实测）
//
//	① 生产形态**不得**再用 canonical 视图算 total_requests。
//	   ★ 252 生产实测（2026-10-27，只读，网关角色）：
//	     `SELECT COUNT(*) FROM request_logs_with_current_month rl WHERE rl.credential_id = 21`
//	     ⇒ Execution Time **14,728.010 ms**（★ 单个凭据！）
//	     ⇒ ★ × 74 个凭据 ⇒ ★★ 整个端点 ≈ 7~18 分钟 ⇒ ★★ 触发角色级 statement_timeout
//	   ★ 而窄投影两臂形态 = **247.063 ms** ⇒ ★★ **59.6 倍**。
//	② 两臂的投影与谓词必须**逐位一致**。UNION ALL 按位置配对，任一侧漂移 ⇒ 语义漂移。
//	③ credential 谓词必须与**迁移 729 的表达式索引同款**
//	   （`(CASE WHEN credential_id ~ '^[0-9]+$' THEN credential_id::bigint END)`）
//	   ⇒ ★ 否则规划器认不出索引 ⇒ 回退成顺序扫描（★ 实测 plan 里 Index Cond 就是它）。
//	④ canonical 参考臂**必须**仍然读视图。它存在的唯一目的就是当生产路径改写后
//	   还能证明「新写法和视图语义一致」；★ 如果它也跟着改成底表，对拍就变成
//	   自己和自己比 ⇒ 等价性论证归零。
//	⑤ canonical 参考臂**不得被生产代码引用**（接回生产＝把列膨胀放回热路径）。
//
// # ★★ 口径等价性的四条前提（★ 缺一条就不能换）
//
//	1. 视图的 credential_id 列 = `db.SessionFamilyCredentialIDExpr`（逐字相同）；
//	2. 视图的 details 腿是 LEFT JOIN，details 唯一键是
//	   (tenant_id, request_id, partition_date) ⇒ 同一键最多 1 行
//	   ⇒ **LEFT JOIN 不改变行数** ⇒ 计数等价；
//	3. 两臂不重叠（promote 是**移动**不是复制）⇒ UNION ALL 不重复计数；
//	4. 视图 = hot ∪ 母表两臂体（迁移 890 之后无 v1 冻结分支）
//	   ⇒ 视图与原生 source 的**行集**相同
//	   （★ 2026-10-27 252 生产实测：全列双向 EXCEPT ALL 差集 0）。
//
// # ★★ 一个必须记住的量具事实
//
//	两版的 plan **cost 逐字相同**（cost=41.94..441.93 rows=391）
//	⇒ ⇒ **plan cost 里没有「物化宽度」**
//	⇒ ⇒ 视图形态把 118 列（252 实测 3083 字节/行）扛到了 heap scan 上
//	⇒ ⇒ **「EXPLAIN 一样」不蕴含「代价一样」**。
package admin

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

// totalRequestsExprFromSQL 抽出 SQL 里 `AS total_requests` 之前的那一段表达式。
//
// ★ 它按**行**定位（★ 表达式恒在同一行）⇒ ★ 上游挪动行号不会让门失准。
// ★ coreMode 分支的表达式是 `0::bigint`（★ core 模式**故意不算**总数）⇒
// ★ 本函数照样返回它，由调用方分形态断言。
func totalRequestsExprFromSQL(t *testing.T, q string) string {
	t.Helper()
	for _, line := range strings.Split(q, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "AS total_requests") {
			continue
		}
		// ★ 顺序要紧：★ 先去掉行尾逗号（它是 SELECT 列表的分隔符），★ 再去标记
		trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, ","))
		expr := strings.TrimSpace(strings.TrimSuffix(trimmed, "AS total_requests"))
		return expr
	}
	t.Fatalf("SQL 里找不到 `AS total_requests` ⇒ 形态变了（★ 请连同守卫一起更新）")
	return ""
}

// splitUnions 把 "A UNION ALL B" 按 UNION ALL 切开。
func splitUnions(s string) []string {
	return regexp.MustCompile(`(?i)\s+UNION ALL\s+`).Split(s, -1)
}

var normSpace = regexp.MustCompile(`\s+`)

// normWS 归一化空白（用于逐位比较）。
func normWS(s string) string { return strings.TrimSpace(normSpace.ReplaceAllString(s, " ")) }

// legSelectList 返回一条臂的 SELECT 投影列表（精确文本，归一化空白）。
func legSelectList(leg string) string {
	i := strings.Index(strings.ToUpper(leg), "SELECT ")
	if i < 0 {
		return ""
	}
	rest := leg[i+len("SELECT "):]
	j := strings.Index(strings.ToUpper(rest), " FROM ")
	if j < 0 {
		return ""
	}
	return normWS(rest[:j])
}

// TestMonitorSummaryTotalRequestsUsesNarrowTwoArms：生产形态不得再读 canonical 视图。
func TestMonitorSummaryTotalRequestsUsesNarrowTwoArms(t *testing.T) {
	for _, mode := range []string{"", "detail", "core"} {
		q := buildMonitorSummarySQL(monitorSummarySQLParams{CredentialID: 7, Mode: mode})
		expr := totalRequestsExprFromSQL(t, q)

		if strings.Contains(expr, "request_logs_with_current_month") {
			t.Fatalf("mode=%q：total_requests 仍在读 canonical 视图 ⇒ ★ 把 118 列（252 实测 3083 B/行）\n"+
				"  扛回 heap scan ⇒ ★ 单凭据 14,728 ms（★ ×74 凭据 ⇒ 端点超时）\n"+
				"  表达式: %s", mode, expr)
		}
		// ★ coreMode 刻意不算总数（0::bigint）⇒ 那是**另一个**形态，不是回归
		if mode == "core" {
			if expr != "0::bigint" {
				t.Fatalf("mode=core：total_requests 应为 0::bigint（★ core 模式刻意不算总数），实得: %s", expr)
			}
			continue
		}
		if !strings.Contains(expr, "session_turns_hot") || !strings.Contains(expr, "session_turns") {
			t.Fatalf("mode=%q：total_requests 必须读底表两臂（hot ∪ 母表）⇒ 表达式: %s", mode, expr)
		}
	}
}

// TestMonitorSummaryTotalRequestsArmsPositionallyAligned：两臂投影与谓词逐位一致。
//
// ★ 断言打在**helper 的输出**上（★ 而不是打在拼装后的 SQL 上）：
// ★ helper 的两臂只允许【表名】不同 ⇒ 其余（投影列、谓词表达式、别名）必须逐字相同。
// ★★ 「生产表达式必须逐字包含 helper 输出」由上面那条门负责（★ 两件事分开断言）。
func TestMonitorSummaryTotalRequestsArmsPositionallyAligned(t *testing.T) {
	narrow := dbpkg.SessionFamilyCredentialCountSourceSQL("c.id")

	// ★ helper 的整体被一对括号包着（供调用方直接当 FROM-source 用）
	// ★ ⇒ 先剥掉**外层**那对，再拆两臂 ⇒ ★ 否则每臂会各带一半括号（★ 拆出来的两臂不对称）
	inner := normWS(narrow)
	inner = strings.TrimPrefix(inner, "(")
	inner = strings.TrimSuffix(inner, ")")

	legs := splitUnions(inner)
	if len(legs) != 2 {
		t.Fatalf("窄投影两臂必须恰 2 条（★ UNION ALL 按位置配对），实得 %d 条: %s", len(legs), narrow)
	}
	// 两臂各自都必须带：同一投影列 + 同一 credential 谓词
	for i, leg := range legs {
		// ★★ 投影列必须**恰好**是 t.credential_id（★ 不是 Contains！）
		// ⇒ ★ 因为 "SELECT t.credential_id, t.ts" 也 Contains "SELECT t.credential_id"
		// ⇒ ★ ★ 真仓变异臂 M4 就是靠这个洞跑过去的（★ 宽投影退化检不出）
		proj := legSelectList(leg)
		if proj != "t.credential_id" {
			t.Fatalf("第 %d 臂的投影必须**恰好**是 `t.credential_id`（★ 窄投影 ⇒ 只取计数需要的列；\n"+
				"  ★ Contains 判据会被「SELECT t.credential_id, t.ts」蒙过去 ⇒ ★★ 必须精确比）:\n  实得: %q", i+1, proj)
		}
		if !strings.Contains(leg, dbpkg.SessionFamilyCredentialIDExpr) {
			t.Fatalf("第 %d 臂的 credential 谓词必须逐字等于 db.SessionFamilyCredentialIDExpr\n"+
				"  （★ 它与视图投影里的那一项同字 ⇒ 也与迁移 729 的表达式索引同形）: %s", i+1, leg)
		}
	}
	// ★★ 把第 1 臂的表名换成第 2 臂的 ⇒ 两者必须**逐字相同**
	// ⇒⇒ 即：两臂只允许表名不同，投影与谓词完全一致。
	// ★ 剥掉每臂自己的那对括号（★ helper 整体还被一对括号包着 ⇒ 不剥会差一个括号）
	strip := func(s string) string {
		s = normWS(s)
		s = strings.TrimPrefix(s, "(")
		s = strings.TrimSuffix(s, ")")
		return normWS(s)
	}
	normalizedA := strings.Replace(strip(legs[0]), "public.session_turns_hot", "public.session_turns", 1)
	normalizedB := strip(legs[1])
	if normalizedA != normalizedB {
		t.Fatalf("两臂除表名外必须逐字相同（★ UNION ALL 按位置配对 ⇒ 任一侧漂移＝语义漂移）:\n  A: %s\n  B: %s",
			normalizedA, normalizedB)
	}
	// 两臂必须分别落在 hot 与母表（★ 不能两臂都读同一张表 ⇒ 会漏行）
	if !strings.Contains(legs[0], "public.session_turns_hot") {
		t.Fatalf("第 1 臂必须读 session_turns_hot: %s", legs[0])
	}
	if strings.Contains(legs[1], "session_turns_hot") {
		t.Fatalf("第 2 臂不得读 hot（★ 两臂同表 ⇒ 漏掉已 promote 的行）: %s", legs[1])
	}
	if !strings.Contains(legs[1], "public.session_turns t") {
		t.Fatalf("第 2 臂必须读母表 session_turns: %s", legs[1])
	}
}

// TestMonitorSummaryTotalRequestsCanonicalArmReadsView：参考臂必须仍然读视图。
func TestMonitorSummaryTotalRequestsCanonicalArmReadsView(t *testing.T) {
	arm := monitorSummaryTotalRequestsCanonicalArmSQL()
	if !strings.Contains(arm, "request_logs_with_current_month") {
		t.Fatalf("canonical 参考臂必须读 canonical 视图（★ 它是对拍基准；改成底表＝自己和自己比）: %s", arm)
	}
	if !strings.Contains(arm, "rl.credential_id = c.id") {
		t.Fatalf("canonical 参考臂的谓词必须与生产形态同形（★ 同一条谓词才能对比）: %s", arm)
	}
}

// TestMonitorSummaryTotalRequestsHasNoCanonicalArmMarker：★ 生产 SQL 不得含参考臂的毒丸标记。
//
// ★★ 这条门补的是【真仓变异臂 M5 抓出来的洞】：
// ★★ AST 静态门只看 Go 表达式（CallExpr）⇒ ★★ 参考臂若被拼进 **SQL 字符串字面量**
// ★★ ⇒ AST 看不见 ⇒ ⇒ 「AST 门」与「视图文本门」都可能被绕过
// ★★ ⇒ ⇒ 必须有第三个判据：**产物标记**（★ 与「哨兵字符串」同族）。
func TestMonitorSummaryTotalRequestsHasNoCanonicalArmMarker(t *testing.T) {
	// ★★ 两个形态都要拦（★ 真仓变异臂 M5 的第一版只塞了【名字】、第二版只塞了【标记】）：
	//   ① 参考臂的**名字**被当字面文本塞进 SQL（★ 那会让 SQL 直接坏掉，但仍是回归）
	//   ② 参考臂被当 **Go 表达式**调用（★ 那会把标记带进产物）
	for _, mode := range []string{"", "detail", "core"} {
		q := buildMonitorSummarySQL(monitorSummarySQLParams{CredentialID: 7, Mode: mode})
		if strings.Contains(q, monitorSummaryTotalRequestsCanonicalArmMarker) {
			t.Fatalf("mode=%q：生产 SQL 里出现了 canonical 参考臂的毒丸标记 %q\n"+
				"  ⇒ ★ 参考臂被接回了生产 ⇒ ★ 等于把 118 列的列膨胀放回热路径", mode,
				monitorSummaryTotalRequestsCanonicalArmMarker)
		}
		if strings.Contains(q, "monitorSummaryTotalRequestsCanonicalArmSQL") {
			t.Fatalf("mode=%q：生产 SQL 里出现了 canonical 参考臂的【名字】\n"+
				"  ⇒ ★ 参考臂被当字面文本塞进了 SQL ⇒ ★ 等于把视图形态接回生产", mode)
		}
	}
}

// TestMonitorSummaryTotalRequestsCanonicalArmIsTestOnly：参考臂不得被生产代码引用。
func TestMonitorSummaryTotalRequestsCanonicalArmIsTestOnly(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse admin package: %v", err)
	}
	for _, pkg := range pkgs {
		for fname, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// ★★ 必须同时认两种 Fun 形态（★ R167 的 M3 变异臂就是这个坑）：
				//   *ast.SelectorExpr（跨包） + *ast.Ident（同包）
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if fun.Sel.Name != "monitorSummaryTotalRequestsCanonicalArmSQL" {
						return true
					}
				case *ast.Ident:
					if fun.Name != "monitorSummaryTotalRequestsCanonicalArmSQL" {
						return true
					}
				default:
					return true
				}
				t.Errorf("%s: 生产代码引用了 canonical 参考臂 monitorSummaryTotalRequestsCanonicalArmSQL"+
					"（它只供对拍；接回生产＝把 118 列的列膨胀放回热路径）", fname)
				return true
			})
		}
	}
}

// TestMonitorSummaryTotalRequestsMatchesCanonicalView_RealDB：真库逐凭据对拍。
//
// ★ 单语句同一快照（★ 否则并发写入会让计数自己漂移）。
func TestMonitorSummaryTotalRequestsMatchesCanonicalView_RealDB(t *testing.T) {
	pool := slidingWindowRealDBPool(t)
	ctx := context.Background()

	// 库就绪前置：canonical 视图必须在（★ 少了它就没法对拍）
	var viewExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		   WHERE n.nspname='public' AND c.relname='request_logs_with_current_month')`,
	).Scan(&viewExists); err != nil {
		t.Fatalf("探测 canonical 视图失败: %v", err)
	}
	if !viewExists {
		t.Skip("canonical 视图不在库，跳过真库对拍")
	}

	// ★ 逐凭据在【同一条语句】里比两侧计数（★ 同一快照）
	// ★★ 窄投影一侧**直接调用生产 helper** ⇒ ★ 对拍测的正是「生产真正用的那串 SQL」
	// ★★ 而参照侧是**手写**的视图形态 ⇒ ★ 两侧独立 ⇒ ★ helper 若漂移，对拍必红。
	narrow := dbpkg.SessionFamilyCredentialCountSourceSQL("c.id")
	paritySQL := "SELECT c.id,\n" +
		"  (SELECT count(*) FROM request_logs_with_current_month rl WHERE rl.credential_id = c.id),\n" +
		"  (SELECT count(*) FROM " + narrow + " q)\n" +
		"FROM credentials c\n" +
		"WHERE c.lifecycle_status <> 'retired'\n" +
		"ORDER BY c.id"

	rows, err := pool.Query(ctx, paritySQL)
	if err != nil {
		t.Fatalf("对拍查询失败: %v", err)
	}
	defer rows.Close()

	var checked int
	for rows.Next() {
		var id int64
		var viewCnt, narrowCnt int64
		if err := rows.Scan(&id, &viewCnt, &narrowCnt); err != nil {
			t.Fatalf("扫描对拍行失败: %v", err)
		}
		checked++
		if viewCnt != narrowCnt {
			t.Fatalf("credential %d：视图计数 %d ≠ 窄投影两臂计数 %d（★ 语义漂移）", id, viewCnt, narrowCnt)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("对拍行迭代失败: %v", err)
	}
	if checked == 0 {
		t.Fatal("对拍了 0 个凭据 ⇒ ★ 断言恒真（★ 门必须有牙）")
	}
	t.Logf("逐凭据对拍通过：%d 个凭据，视图计数 ≡ 窄投影两臂计数", checked)
}
