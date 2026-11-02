package admin

// ★★ 2026-11-02 第十八轮（F-33 / F-34）：admin 面的**时间戳 JSON 契约**。
//
// ── 这两条为什么现在才做 ──────────────────────────────────────────────────
//
// 第十七轮把 prompt-injection 管理面的**可空列**与**写载荷**收口了，但它在
// §二十.6 顺手记下的两条发现（当时登记为 F-20 / F-21，★ 因与兄弟档 F20/F21
// 撞号，第十八轮重编号为 **F-33 / F-34**）说的是**另一族**：
//
//	F-33  admin/routing.go（credentials.expires_at / availability_recover_at /
//	       quota_recover_at / effective_at / cooling_until）
//	       时间戳列用 `::text` 投影 ⇒ 落进 JSON 的是 **PG 文本**
//	       （"2026-11-02 08:00:00+08"——空格分隔、没有 'T'），
//	       而**同一个 JSON key** 在 admin/provider_credential.go 与
//	       admin/credential_monitor.go 那一侧给的是 **RFC3339**
//	       ⇒ 同一个 key 两种线格式。
//	F-34  admin/auto_route.go（v_task_model_ranking.last_sampled_at /
//	       updated_at、auto_route_selections_all.ts / settled_at）
//	       同上。
//
// ★★ 口径订正（实测证伪一条旧说法）：**「PG 文本时间戳 ⇒ 移动端
// `new Date()` 返回 NaN」不成立**。2026-11-02 实测：
//   · V8（node v22）：`new Date("2026-07-19 21:08:25.836952+08")` ⇒ 解析成功
//   · JSC（osascript -l JavaScript，与 iOS Safari 同引擎）：同样解析成功
//   ⇒ 那条说法在 9d3b190de 的注释与本档 §二十.6 里都**没有依据**。
//   ⇒ 本轮不拿它当理由。**真实**的理由有三条，且都更硬：
//     ① **同一个 JSON key 两种线格式**（跨端点、且 routing resolve 响应
//        **内部**就有两条路径各给一种格式）——这是**契约分叉**，与引擎无关。
//     ② PG 文本**不是 RFC3339**：ECMAScript 的 Date Time String Format 要求
//        'T' 分隔 ⇒ PG 文本落在**规范之外** ⇒ 能不能解析取决于引擎的
//        **宽松解析器**（今天 V8/JSC 都收，但那是实现细节，不是规范保证）。
//     ③ web/src/components/NodeDetailEmergencyPanel.vue 用
//        `new Date(c.effective_at).getTime() <= Date.now()` 做**业务判决**
//        ⇒ 解析一旦失败，NaN 会让比较恒为 false ⇒ 判决静默翻转。
//
// ── 四道门 ──────────────────────────────────────────────────────────────
//
//	门 R1（AST · 分母锚事实源）：admin 生产文件里，**schema 派生的时间戳列**
//	      不许以 `::text` 投影（那是 PG 文本，不是 RFC3339）；
//	      `to_char(<时间戳列>, …)` 允许，但格式串**必须**产出 RFC3339。
//	门 R2（归一函数判决表）：normalizeTimestamp 的逐格判决。
//	门 R3（反证 + 往返）：① PG 文本形态**确实不是** RFC3339（证明 R1/R2 的
//	      判据不是空洞的）；② 归一后的产出**可被** RFC3339 解析，且**时刻**不变。
//	门 R4（接线门 · 位置对应）：本轮触碰的三个查询里，**每一个**时间戳投影
//	      的扫描目标**必须**是 `*time.Time`，且**必须**经 normalizeTimestamp
//	      归一（⇒ 「改了 SQL 却忘了归一」与「归一了却扫错了列」都红）。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ── 事实源：schema 派生的时间戳列 ───────────────────────────────────────

// schemaTimestampColumnRe 认 schema 里的时间戳列声明。
//
// ★ 覆盖三种写法：timestamp with time zone / timestamp without time zone /
//
//	TIMESTAMPTZ（连写）——★ **连写那一种必须覆盖**，否则
//	`last_sampled_at TIMESTAMPTZ` 这类列根本进不了分母
//	（2026-11-02 实测：漏掉连写 ⇒ 分母从 255 掉到 169 ⇒ 4 个真实站点漏网）。
var schemaTimestampColumnRe = regexp.MustCompile(
	`(?im)^\s*([a-z_][a-z0-9_]*)\s+timestamp(?:tz)?(?:\s+with(?:out)?\s+time\s+zone)?\b`)

// schemaTimestampColumns 从**事实源**（schema SQL）派生时间戳列名集合。
//
// ★ 分母为什么必须锚在这里（LESSONS §559）：门里那张「时间戳列清单」如果
//
//	手写，加了新列就不会有人知道 ⇒ 分母变成「人记得的那张表」而不是
//	「事实源里的那批列」⇒ 正是「缩分母 + 改判据」可以合谋出假绿的那条路。
//
// ★ 事实源范围：tables（表定义）+ views（视图定义）+ migrations（历史演进，
//
//	视图与后加的列在这里）⇒ 三处并集。
func schemaTimestampColumns(t *testing.T) map[string]bool {
	t.Helper()
	cols := map[string]bool{}
	roots := []string{
		filepath.Join("..", "sql", "objects", "tables"),
		filepath.Join("..", "sql", "objects", "views"),
		filepath.Join("..", "sql", "migrations"),
	}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".sql") {
				return nil
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, m := range schemaTimestampColumnRe.FindAllStringSubmatch(string(b), -1) {
				cols[strings.ToLower(m[1])] = true
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	// ★ 恒绿护栏：事实源读空了 ⇒ 门会扫不到任何东西 ⇒ 必须报出来。
	if len(cols) < 100 {
		t.Fatalf("schema 派生的时间戳列只有 %d 个（< 100）⇒ 事实源读空了，"+
			"这道门会扫不到任何东西", len(cols))
	}
	return cols
}

// tsAdminSQLLiterals 抽出 admin 生产文件里所有含 SELECT 的 SQL 字符串字面量。
//
// ★ 用 go/ast ⇒ **注释不算**（历史说明里写着 `detected_at::text` 不会被误报）。
func tsAdminSQLLiterals(t *testing.T) []struct {
	File string
	SQL  string
} {
	t.Helper()
	var out []struct {
		File string
		SQL  string
	}
	fset := token.NewFileSet()
	// ★ 复用仓里已有的 adminProductionGoFiles（第十七轮建的），
	//   它自带恒绿护栏（目录读空 ⇒ 报「扫描范围写错了」）。
	files := adminProductionGoFiles(t)
	if len(files) < 50 {
		t.Fatalf("admin 生产 .go 文件只有 %d 个（< 50）⇒ 扫描范围写错了", len(files))
	}
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			if !strings.Contains(strings.ToUpper(val), "SELECT") {
				return true
			}
			out = append(out, struct {
				File string
				SQL  string
			}{File: name, SQL: val})
			return true
		})
	}
	if len(out) < 50 {
		t.Fatalf("admin 里的 SQL 字面量只有 %d 个（< 50）⇒ 扫描范围写错了", len(out))
	}
	return out
}

// ── 门 R1：时间戳列不许 ::text 投影 ─────────────────────────────────────

// TestAdminTimestampColumnsHaveNoTextCast 是门 R1。
//
// ★ 判据：schema 派生的时间戳列一旦被 `::text` 投影，产出的是 **PG 文本**
//
//	（"2026-11-02 08:00:00+08"）⇒ 不是 RFC3339 ⇒ 同一个 JSON key 在别的
//	端点给 RFC3339 时，客户端就得同时认两种写法。
//
// ★ 例外：`to_char(<时间戳列>, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')` 是**已经在
//
//	产出 RFC3339** 的写法（仓内 model_credibility.go / model_iq.go 就是
//	这么写的）⇒ 允许，但格式串**必须**含 ISO 的 'T' 分隔。
func TestAdminTimestampColumnsHaveNoTextCast(t *testing.T) {
	cols := schemaTimestampColumns(t)

	textCastRe := regexp.MustCompile(`(?i)\b([a-z_][a-z0-9_]*)\s*::\s*text\b`)
	toCharRe := regexp.MustCompile(`(?is)to_char\s*\(\s*(?:[a-z_][a-z0-9_]*\.)?([a-z_][a-z0-9_]*)\s*,(.*?)\)`)

	violations := 0
	for _, lit := range tsAdminSQLLiterals(t) {
		sql := lit.SQL
		for _, m := range textCastRe.FindAllStringSubmatch(sql, -1) {
			col := strings.ToLower(m[1])
			if !cols[col] {
				continue
			}
			violations++
			t.Errorf("%s：时间戳列 %s 被 ::text 投影 ⇒ 产出的是 PG 文本"+
				"（空格分隔、没有 'T'），不是 RFC3339 ⇒ 同一个 JSON key 在别的"+
				"端点是 RFC3339 ⇒ 契约分叉；请取原生 timestamptz 再经"+
				" normalizeTimestamp 归一", lit.File, col)
		}
		for _, m := range toCharRe.FindAllStringSubmatch(sql, -1) {
			col := strings.ToLower(m[1])
			if !cols[col] {
				continue
			}
			format := m[2]
			// ★ 只对**日期格式串**生效：`to_char(bucket, "$3")` 里的
			//   bucket 是**计算出来的分桶别名**（不是时间戳列），
			//   格式串是占位符 ⇒ 不是日期格式 ⇒ 不在本门口径内。
			//   （2026-11-02 实测：不加这一层过滤会误报 usage.go /
			//   usage_provider_detail.go 两处。）
			if !strings.Contains(format, "MM") && !strings.Contains(format, "DD") {
				continue
			}
			if !strings.Contains(format, `"T"`) && !strings.Contains(format, "'T'") {
				violations++
				t.Errorf("%s：to_char(%s, …) 的格式串 %q 不产出 RFC3339"+
					"（缺 ISO 的 'T' 分隔）⇒ 与 ::text 同族", lit.File, col, strings.TrimSpace(format))
			}
		}
	}
}

// ── 门 R2：归一函数的逐格判决 ───────────────────────────────────────────

// TestNormalizeTimestampDecisionTable 是门 R2：normalizeTimestamp 的判决表。
func TestNormalizeTimestampDecisionTable(t *testing.T) {
	tz := time.FixedZone("CST", 8*3600)
	cases := []struct {
		name string
		in   *time.Time
		want *string
	}{
		{"nil ⇒ nil（JSON 落 null）", nil, nil},
		{"零值 ⇒ nil（零值不是某年 1 月 1 日）", tsPtrTime(time.Time{}), nil},
		{
			"带 +08 偏移 ⇒ 归一到 UTC 的同一时刻",
			tsPtrTime(time.Date(2026, 11, 2, 8, 0, 0, 0, tz)),
			tsPtrString("2026-11-02T00:00:00Z"),
		},
		{
			"已是 UTC ⇒ 原样",
			tsPtrTime(time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)),
			tsPtrString("2026-11-02T00:00:00Z"),
		},
		{
			"亚秒 ⇒ 截到秒（口径：秒精度）",
			tsPtrTime(time.Date(2026, 11, 2, 8, 0, 0, 123456789, tz)),
			tsPtrString("2026-11-02T00:00:00Z"),
		},
		{
			"1970 纪元不是零值 ⇒ 照常输出",
			tsPtrTime(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)),
			tsPtrString("1970-01-01T00:00:00Z"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := normalizeTimestamp(c.in)
			switch {
			case c.want == nil && got != nil:
				t.Fatalf("normalizeTimestamp = %q, want nil", *got)
			case c.want != nil && got == nil:
				t.Fatalf("normalizeTimestamp = nil, want %q", *c.want)
			case c.want != nil && *got != *c.want:
				t.Fatalf("normalizeTimestamp = %q, want %q", *got, *c.want)
			}
		})
	}
}

// ── 门 R3：反证 + 往返 ──────────────────────────────────────────────────

// TestTimestampContractCounterProofAndRoundTrip 是门 R3。
func TestTimestampContractCounterProofAndRoundTrip(t *testing.T) {
	// ① 反证：PG 文本形态**确实不是** RFC3339
	//   ⇒ 这条判据不是空洞的（不是「什么都能过」）。
	//   ★ 实测补充：V8 与 JSC 的宽松解析器**今天**能吃 PG 文本
	//   ⇒ 但那不是规范保证的行为 ⇒ 不能拿它当契约。
	pgText := "2026-07-19 21:08:25.836952+08"
	if _, err := time.Parse(time.RFC3339, pgText); err == nil {
		t.Fatalf("PG 文本形态 %q 竟被 RFC3339 解析器接受了 ⇒ 门 R1/R2 的判据"+
			"本身需要重估", pgText)
	}
	// ② 同一个时刻的 RFC3339 形态**必须**被接受。
	rfc := "2026-07-19T13:08:25Z"
	if _, err := time.Parse(time.RFC3339, rfc); err != nil {
		t.Fatalf("RFC3339 形态 %q 解析失败：%v", rfc, err)
	}

	// ③ 往返：归一后的产出可被 RFC3339 解析，且**时刻**不变。
	tz := time.FixedZone("CST", 8*3600)
	for _, in := range []time.Time{
		time.Date(2026, 11, 2, 8, 0, 0, 0, tz),
		time.Date(2026, 7, 19, 21, 8, 25, 0, tz),
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	} {
		got := normalizeTimestamp(&in)
		if got == nil {
			t.Fatalf("normalizeTimestamp(%v) = nil", in)
		}
		parsed, err := time.Parse(time.RFC3339, *got)
		if err != nil {
			t.Fatalf("归一后的 %q 不能被 RFC3339 解析：%v", *got, err)
		}
		if !parsed.Equal(in.Truncate(time.Second)) {
			t.Fatalf("归一后的 %q 解析出的时刻 %v ≠ 原时刻 %v",
				*got, parsed.UTC(), in.UTC())
		}
	}
}

// ── 门 R4：接线门（投影 ↔ 扫描目标 ↔ 归一）──────────────────────────────

// adminTimestampProjectionTargets 是门 R4 覆盖的查询。
//
// ★ 范围说明（诚实登记）：门 R4 **逐个**钉住本轮触碰的三个查询；它**不是**
//
//	「admin 全包所有时间戳投影」的普查——那个范围需要 SQL 与 Go 的位置
//	对应，而 admin 里有 700+ 处 rows.Scan，实测只有 22 处能可靠对上
//	⇒ 强行做成通用门会得到**假绿**。
//	⇒ 全包的兜底由**门 R1** 承担（R1 覆盖 admin 每一个 SQL 字面量，
//	分母锚在 schema 上 ⇒ 新增一个 ::text 站点必红）。
var adminTimestampProjectionTargets = []struct {
	File string
	Func string
}{
	{"routing.go", "handleRoutingResolve"},
	{"auto_route.go", "HandleAffinityRanking"},
	{"auto_route.go", "handleAffinitySelections"},
}

// TestTimestampProjectionIsNormalized 是门 R4。
//
// 逐个查询断言：
//
//	① SELECT 列表的**顶层**表达式个数 == rows.Scan 的实参个数（列漂移立刻红）。
//	② 每一个**裸列引用**形态的时间戳投影，其扫描目标**必须**是 `*time.Time`
//	   变量（不是 *string —— 驱动给 timestamptz，扫进 *string 会直接报错）。
//	③ 该变量**必须**被 normalizeTimestamp 归一（⇒ 响应里是 RFC3339）。
func TestTimestampProjectionIsNormalized(t *testing.T) {
	cols := schemaTimestampColumns(t)
	fset := token.NewFileSet()

	for _, target := range adminTimestampProjectionTargets {
		t.Run(target.File+":"+target.Func, func(t *testing.T) {
			file, err := parser.ParseFile(fset, target.File, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", target.File, err)
			}
			fn := tsFindFuncDecl(file, target.Func)
			if fn == nil {
				t.Fatalf("%s 里找不到函数 %s", target.File, target.Func)
			}

			sql := tsFuncSQLLiteral(fn)
			if sql == "" {
				t.Fatalf("%s:%s 里没有找到 SQL 字面量 ⇒ 门 R4 落空了",
					target.File, target.Func)
			}
			scanArgs := tsFuncScanArgs(fn)
			if len(scanArgs) == 0 {
				t.Fatalf("%s:%s 里没有找到 rows.Scan ⇒ 门 R4 落空了",
					target.File, target.Func)
			}
			selects := tsSelectListTopLevel(sql)
			if len(selects) == 0 {
				t.Fatalf("%s:%s 的 SQL 解析不出 SELECT 列表 ⇒ 门 R4 落空了",
					target.File, target.Func)
			}
			// ① 列数与扫描数必须一致。
			if len(selects) != len(scanArgs) {
				t.Fatalf("%s:%s：SELECT 列表 %d 项 vs rows.Scan 实参 %d 项 ⇒ 列漂移",
					target.File, target.Func, len(selects), len(scanArgs))
			}

			timeVars := tsFuncTimePtrVars(fn)
			normalized := tsFuncNormalizedArgs(fn)

			checked := 0
			for i, expr := range selects {
				col, ok := tsBareTimestampColumn(expr, cols)
				if !ok {
					continue
				}
				checked++
				ident, ok := tsScanArgIdent(scanArgs[i])
				if !ok {
					t.Errorf("%s:%s 第 %d 项（%s ⇒ %s）的扫描目标不是 &标识符："+
						"时间戳列必须扫进 *time.Time 变量", target.File, target.Func,
						i+1, col, expr)
					continue
				}
				if !timeVars[ident] {
					t.Errorf("%s:%s 第 %d 项（%s）的扫描目标 %s 不是 *time.Time"+
						"（驱动给 timestamptz，扫进 *string 会直接报错）",
						target.File, target.Func, i+1, col, ident)
				}
				if !normalized[ident] {
					t.Errorf("%s:%s 第 %d 项（%s）的扫描目标 %s 没有经"+
						" normalizeTimestamp 归一 ⇒ 响应里不是 RFC3339",
						target.File, target.Func, i+1, col, ident)
				}
			}
			if checked == 0 {
				t.Fatalf("%s:%s 里一个时间戳投影都没识别出来 ⇒ 门 R4 落空了",
					target.File, target.Func)
			}
		})
	}
}

// ── 门 R4 的辅助解析 ────────────────────────────────────────────────────

// tsFindFuncDecl 按名字找函数声明。
func tsFindFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	var found *ast.FuncDecl
	ast.Inspect(file, func(n ast.Node) bool {
		fd, ok := n.(*ast.FuncDecl)
		if ok && fd.Name.Name == name {
			found = fd
			return false
		}
		return true
	})
	return found
}

// tsFuncSQLLiteral 取出函数体里第一个含 SELECT 的 SQL 字符串字面量。
func tsFuncSQLLiteral(fn *ast.FuncDecl) string {
	out := ""
	ast.Inspect(fn, func(n ast.Node) bool {
		if out != "" {
			return false
		}
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		val, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if strings.Contains(strings.ToUpper(val), "SELECT") {
			out = val
			return false
		}
		return true
	})
	return out
}

// tsFuncScanArgs 取出函数体里第一个 rows.Scan 调用的实参。
func tsFuncScanArgs(fn *ast.FuncDecl) []ast.Expr {
	var args []ast.Expr
	ast.Inspect(fn, func(n ast.Node) bool {
		if args != nil {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Scan" {
			return true
		}
		args = call.Args
		return false
	})
	return args
}

// tsFuncTimePtrVars 收集函数体里声明为 *time.Time 的局部变量名。
func tsFuncTimePtrVars(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.VAR {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil {
				continue
			}
			star, ok := vs.Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			sel, ok := star.X.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Time" {
				continue
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "time" {
				continue
			}
			for _, name := range vs.Names {
				out[name.Name] = true
			}
		}
		return true
	})
	return out
}

// tsFuncNormalizedArgs 收集被 normalizeTimestamp 归一过的变量名。
func tsFuncNormalizedArgs(fn *ast.FuncDecl) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "normalizeTimestamp" {
			return true
		}
		for _, a := range call.Args {
			if id, ok := a.(*ast.Ident); ok {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

// tsScanArgIdent 从扫描实参里取出 &标识符 的标识符名。
func tsScanArgIdent(arg ast.Expr) (string, bool) {
	unary, ok := arg.(*ast.UnaryExpr)
	if !ok || unary.Op != token.AND {
		return "", false
	}
	id, ok := unary.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return id.Name, true
}

// tsBareTimestampColumn 判断一个 SELECT 表达式是不是**裸列引用**形态的时间戳列。
//
// ★ 只认裸列引用（`c.expires_at` / `expires_at`）：像
//
//	`(c.effective_at IS NULL OR c.effective_at <= now()) AS credential_in_effect`
//	这种**布尔**表达式里也出现时间戳列名，但它不是时间戳投影 ⇒ 不算。
func tsBareTimestampColumn(expr string, cols map[string]bool) (string, bool) {
	e := strings.TrimSpace(expr)
	if e == "" {
		return "", false
	}
	// 去掉可能的 AS 别名。
	if idx := strings.Index(strings.ToUpper(e), " AS "); idx >= 0 {
		return "", false
	}
	parts := strings.Split(e, ".")
	if len(parts) > 2 {
		return "", false
	}
	name := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
	if !cols[name] {
		return "", false
	}
	// 名字里若还带非标识符字符 ⇒ 不是裸列引用。
	if strings.ContainsAny(name, "()'\" ") {
		return "", false
	}
	return name, true
}

// tsSelectListTopLevel 从 SQL 里取出 SELECT 列表（按**顶层**逗号切分）。
//
// ★ 顶层 = 不在括号 / 引号 / 注释里。
func tsSelectListTopLevel(sql string) []string {
	s := tsStripSQLComments(sql)
	upper := strings.ToUpper(s)
	idx := strings.Index(upper, "SELECT")
	if idx < 0 {
		return nil
	}
	rest := s[idx+len("SELECT"):]
	// ★ 先在**顶层 FROM** 处截断 —— ★ SELECT 列表的最后一项与 FROM 之间
	//   **没有逗号**（"v.unavailable_reason\nFROM v_routable_…"）⇒ 若先切分
	//   再找 FROM，FROM 会粘在最后一项上 ⇒ 少算一项。
	rest = tsCutAtTopLevelFrom(rest)
	var parts []string
	var cur strings.Builder
	depth := 0
	var quote byte
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if quote != 0 {
			cur.WriteByte(ch)
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			cur.WriteByte(ch)
			continue
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, strings.TrimSpace(cur.String()))
				cur.Reset()
				continue
			}
		}
		cur.WriteByte(ch)
	}
	if cur.Len() > 0 {
		parts = append(parts, strings.TrimSpace(cur.String()))
	}
	return parts
}

// tsCutAtTopLevelFrom 在**顶层 FROM** 关键字处截断（不在括号 / 引号里）。
func tsCutAtTopLevelFrom(s string) string {
	depth := 0
	var quote byte
	upper := strings.ToUpper(s)
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if quote != 0 {
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"':
			quote = ch
			continue
		case '(':
			depth++
			continue
		case ')':
			depth--
			continue
		}
		if depth != 0 {
			continue
		}
		if i+4 <= len(upper) && upper[i:i+4] == "FROM" {
			before := byte(' ')
			if i > 0 {
				before = upper[i-1]
			}
			after := byte(' ')
			if i+4 < len(upper) {
				after = upper[i+4]
			}
			if !tsIsIdentByte(before) && !tsIsIdentByte(after) {
				return s[:i]
			}
		}
	}
	return s
}

// tsIsIdentByte 判断是不是标识符字符。
func tsIsIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// tsStripSQLComments 去掉 SQL 里的 -- 行注释与 /* */ 块注释。
func tsStripSQLComments(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '-' && i+1 < len(s) && s[i+1] == '-' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if s[i] == '/' && i+1 < len(s) && s[i+1] == '*' {
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			b.WriteByte(' ')
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// ── 小工具 ──────────────────────────────────────────────────────────────

func tsPtrTime(t time.Time) *time.Time { return &t }

func tsPtrString(s string) *string { return &s }
