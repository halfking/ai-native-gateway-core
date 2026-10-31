//go:build !integration

// ★ 这个 build tag 是**必需**的（2026-10-28 · R172 补）：本文件用到的
//   realDBPool 定义在 session_family_p0_realdb_test.go
//   （`//go:build !integration`）⇒ 没有 tag 时 `-tags=integration`
//   的整包编译断链（`go vet -tags=integration ./domains/session/v2/`
//   实测报 `undefined: realDBPool`）⇒ 仓内唯一的全仓 integration 作业
//   （`go test -tags=integration ./...`）连这个包都编译不过。
package v2

// R169 · R1 守卫：热路径 turn INSERT 的**幂等检查**必须走两臂直查，
// 不得再打 public.session_turns_with_current_month（该视图的 hot 臂带
// 相关反连接，写路径每请求都要为它付规划成本 —— 252 生产规模实测
// 规划时间 5.8~93 ms vs 两臂 3.8~16 ms，见 db.ExistsByKeySQL 注释）。
//
// 三条独立判据：
//  1. **产物判据**：helper 的产物必须恰好是两臂（hot ∪ 父表），两臂的键谓词
//     **逐字相同**，且产物里不得出现视图名。
//  2. **调用点判据**：turn_writer.go 里**每一个** tx.Exec 的 SQL 文本都不得
//     出现视图名（写路径整体不碰视图），且至少有一处必须调用该 helper。
//     ⇒ 判据落在「这条语句长什么样」，不是「源码里有没有这个词」。
//  3. **真库等价判据**：抽样键上「视图说存在」与「两臂说存在」必须完全一致
//     （等价性论证见 db.ExistsByKeySQL 的注释）。
//
// ★ 为什么第 2 条按「每个 Exec」判而不是「INSERT 那一条」：本轮改的是
//   幂等检查，而同一个文件里还有别的 Exec（压缩字段回填）。按「每个 Exec」
//   判，才能在**下一处**有人把视图写回写路径时立刻红。

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kaixuan/llm-gateway-go/internal/sessionfamily"
)

const familyViewName = "session_turns_with_current_month"

// ── 判据 1：产物 ────────────────────────────────────────────────────────────

func TestSessionFamilyExistsByKeySQLShape(t *testing.T) {
	sql := sessionfamily.ExistsByKeySQL("$3::varchar", "$4")

	if strings.Contains(sql, familyViewName) {
		t.Fatalf("产物里出现了会话族视图名 %q —— 这条 SQL 的目的就是绕开它的反连接臂：\n%s",
			familyViewName, sql)
	}
	hot := "FROM public.session_turns_hot WHERE tenant_id = $3::varchar AND request_id = $4"
	parent := "FROM public.session_turns WHERE tenant_id = $3::varchar AND request_id = $4"
	if !strings.Contains(sql, hot) {
		t.Errorf("产物缺 hot 臂（期望逐字包含 %q）：\n%s", hot, sql)
	}
	if !strings.Contains(sql, parent) {
		t.Errorf("产物缺父表臂（期望逐字包含 %q）：\n%s", parent, sql)
	}
	if !strings.Contains(sql, "UNION ALL") {
		t.Errorf("两臂之间必须是 UNION ALL：\n%s", sql)
	}
	// 两臂的键谓词必须**逐字相同**：口径不一致时，两臂会给出不同的存在性答案，
	// 而 NOT EXISTS 对「哪一条命中」是敏感的（命中行数不同不影响存在性，
	// 但键不同 ⇒ 查的根本不是同一个键）。
	if strings.Count(sql, "tenant_id = $3::varchar") != 2 {
		t.Errorf("两臂的 tenant_id 谓词不是各出现一次：\n%s", sql)
	}
	if strings.Count(sql, "request_id = $4") != 2 {
		t.Errorf("两臂的 request_id 谓词不是各出现一次：\n%s", sql)
	}
}

// ── 判据 2：调用点（写路径不得再碰视图）────────────────────────────────────

// turnWriterExecSQL 抽出 turn_writer.go 里每个 tx.Exec 的 SQL 文本。
//
// 文本重建规则：字符串字面量取其内容；拼接进来的非字面量（helper 调用等）
// 记成 "<splice>"。★ 这样拼出来的文本**不是**可执行 SQL，而是「语句形状」——
// 判据只需要看形状（有没有视图名、有没有调 helper）。
func turnWriterExecSQL(t *testing.T) []string {
	t.Helper()
	path := "turn_writer.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", path, err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Exec" {
			return true
		}
		// ★ pgx 的签名是 Exec(ctx, sql, args...) ⇒ SQL 是**第二个**参数。
		//   取错参数会让判据读到一个标识符（ctx）⇒ 形状退化成 "<splice>"
		//   ⇒ 「用了哪个 helper」这条判据永远看不见。
		sql, ok := execSQLArg(call)
		if !ok {
			return true
		}
		out = append(out, sql)
		return true
	})
	return out
}

// execSQLArg 找出调用里的 SQL 实参：第一个「字符串字面量或含字面量的拼接」实参。
func execSQLArg(call *ast.CallExpr) (string, bool) {
	for _, a := range call.Args {
		switch a.(type) {
		case *ast.BasicLit, *ast.BinaryExpr:
			if text, ok := execSQLText(a); ok && strings.Contains(text, " ") {
				return text, true
			}
		}
	}
	return "", false
}

func execSQLText(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return "", false
		}
		return s, true
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		l, okL := execSQLText(v.X)
		if !okL {
			return "", false
		}
		r, okR := execSQLText(v.Y)
		if !okR {
			return "", false
		}
		return l + r, true
	case *ast.Ident:
		return "<splice>", true
	case *ast.CallExpr:
		// ★ 保留被调用的**名字**：helper 的名字就是判据要看的东西
		//   （把它抹成 "<splice>" 会让「用了哪个 helper」这条判据永远看不见）。
		name := "<splice>"
		switch fn := v.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			name = fn.Sel.Name
		}
		return "<splice:" + name + ">", true
	case *ast.SelectorExpr:
		return "<splice:" + v.Sel.Name + ">", true
	case *ast.IndexExpr:
		return "<splice>", true
	}
	return "", false
}

func TestTurnWriterWritePathNeverTouchesFamilyView(t *testing.T) {
	sqls := turnWriterExecSQL(t)
	if len(sqls) == 0 {
		t.Fatal("没抽出任何 tx.Exec 的 SQL —— 判据的扫描范围失效（会让「0 命中」变成恒绿）")
	}
	usesHelper := false
	for i, sql := range sqls {
		t.Logf("写路径第 %d 条 Exec 的 SQL 形状（前 120 字）：%.120s", i+1, strings.ReplaceAll(sql, "\n", " "))
		if strings.Contains(sql, familyViewName) {
			t.Errorf("写路径的第 %d 条 Exec 又打回会话族视图 %q：\n%s",
				i+1, familyViewName, sql)
		}
		if strings.Contains(sql, "ExistsByKeySQL") {
			usesHelper = true
		}
	}
	if !usesHelper {
		t.Error("写路径里没有任何一条 Exec 使用 ExistsByKeySQL —— " +
			"幂等检查的去视图改动被回退了")
	}
}

// ── 判据 3：真库等价 ───────────────────────────────────────────────────────

// TestSessionFamilyExistsByKeyEquivalenceRealDB 在真库上对拍「视图形态」与
// 「两臂形态」的存在性答案。
//
// ★ 需要 TEST_DATABASE_URL（没有就跳过，不假装通过）。
func TestSessionFamilyExistsByKeyEquivalenceRealDB(t *testing.T) {
	pool := realDBPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 抽样：从 hot 表取一批真实键（近窗），再补一批**父表里才有**的键
	// （promote 过的行）—— 只测 hot 里的键会漏掉「反连接生效」的那一半。
	rows, err := pool.Query(ctx, `
		SELECT tenant_id::text, request_id::text FROM public.session_turns_hot
		 WHERE ts > now() - interval '7 days' LIMIT 100`)
	if err != nil {
		t.Skipf("抽样失败（真库可能没有 hot 表）：%v", err)
	}
	type key struct{ tenant, request string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.tenant, &k.request); err != nil {
			rows.Close()
			t.Fatalf("扫描样本失败：%v", err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	if len(keys) == 0 {
		t.Skip("真库近窗没有 hot 行，跳过等价对拍")
	}

	// 父表样本：只存在于分区父表（已 promote）的行。
	prows, err := pool.Query(ctx, `
		SELECT tenant_id::text, request_id::text FROM public.session_turns
		 WHERE ts > now() - interval '30 days'
		   AND NOT EXISTS (SELECT 1 FROM public.session_turns_hot h
		                    WHERE h.tenant_id::text = session_turns.tenant_id::text
		                      AND h.request_id::text = session_turns.request_id::text)
		 LIMIT 100`)
	if err == nil {
		for prows.Next() {
			var k key
			if err := prows.Scan(&k.tenant, &k.request); err == nil {
				keys = append(keys, k)
			}
		}
		prows.Close()
	}

	twoArm := sessionfamily.ExistsByKeySQL("$1::varchar", "$2::text")
	viewForm := "SELECT 1 FROM public.session_turns_with_current_month " +
		"WHERE tenant_id = $1::varchar AND request_id = $2::text"

	var disagree, onlyView, onlyTwoArm, both int
	for _, k := range keys {
		var inView, inTwo bool
		if err := pool.QueryRow(ctx, viewForm, k.tenant, k.request).Scan(new(int)); err == nil {
			inView = true
		} else if err != pgx.ErrNoRows {
			t.Fatalf("视图形态查询失败：%v", err)
		}
		if err := pool.QueryRow(ctx, twoArm, k.tenant, k.request).Scan(new(int)); err == nil {
			inTwo = true
		} else if err != pgx.ErrNoRows {
			t.Fatalf("两臂形态查询失败：%v", err)
		}
		switch {
		case inView && inTwo:
			both++
		case inView:
			onlyView++
		case inTwo:
			onlyTwoArm++
		default:
			disagree++
		}
	}
	t.Logf("样本 %d 个：两边都说存在 %d · 只有视图说 %d · 只有两臂说 %d · 两边都说无 %d",
		len(keys), both, onlyView, onlyTwoArm, disagree)
	if onlyView != 0 || onlyTwoArm != 0 {
		t.Errorf("存在性口径不等价：只有视图说存在 %d 个 · 只有两臂说存在 %d 个 —— "+
			"幂等检查会因此改变行为（要么重复写，要么漏写）", onlyView, onlyTwoArm)
	}
	if len(keys) > 0 && both == 0 {
		t.Error("全部样本都没命中任何一侧 ⇒ 样本没打中真实行，对拍是空的")
	}
}
