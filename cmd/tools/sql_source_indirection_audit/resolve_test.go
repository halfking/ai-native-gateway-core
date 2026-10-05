package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 本文件的每一道门都对应 §9.45 实际踩到的一个错误，而且是**三个方向相反**的
// 错误。把它们钉住的理由不是「以后会有人改坏」，而是：本工具的第一版把这三个坑
// 全踩了一遍，而它的输出「格式正确、数量看着合理」，没有任何视觉信号提示它是错的。
//
//  1. 未知标识符被当成表名 ⇒ 判成「退役安全」（把「不知道」报成「安全」）
//  2. 变量按包级绑定 ⇒ 同名局部变量在不同函数里解析成同一个值（自信的错误答案）
//  3. 用前缀判 v1 ⇒ canonical 视图被误报成 v1 读点（方向相反的假阳性）
//
// 夹具里要用到 Go 反引号（SQL raw string），而夹具本身也是 Go 源码。raw string
// 里不能再出现反引号，所以模板用占位符 ¤ 代替，writePkg 时替换回去——
// 这比在一行里叠 ` + "`" + ` 可靠得多（叠法写错时 Go 的报错会指向模板本身，
// 而不是指向「你少了一个反引号」这个真实原因）。

const bt = "¤"

func render(s string) string { return strings.ReplaceAll(s, bt, "`") }

// writePkg 在临时目录里写一个 Go 包，返回目录路径。
func writePkg(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(render(body)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func auditOne(t *testing.T, files map[string]string) []Site {
	t.Helper()
	dir := writePkg(t, files)
	sites, err := AuditRepo(dir)
	if err != nil {
		t.Fatalf("AuditRepo: %v", err)
	}
	return sites
}

// 门 1：未知标识符必须落在「不可判定」，不得被当成一个「非 v1 的表名」。
//
// 这是最坏的一个失效方向：绑定解析不出时，若把标识符原样当作候选表名，
// `logsTable` 会被判成「名为 logsTable 的非 v1 表」⇒ 归入「退役安全」，
// 而它实际在 `days <= 7` 时读 `request_logs_hot`。
func TestUnknownIdentifierIsNotTreatedAsASafeTableName(t *testing.T) {
	sites := auditOne(t, map[string]string{
		"a.go": `package a

func q(logsTable string) {
	_ = ¤SELECT 1 FROM ¤ + logsTable + ¤ WHERE x = 1¤
}
`,
	})
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want 1\n%v", len(sites), sites)
	}
	if got := sites[0].Classification(); got != ClassUnresolved {
		t.Errorf("未知标识符被判为 %v，期望 %v（不可判定）。\n"+
			"把它当成一个「非 v1 的表名」会归入退役安全——"+
			"**把「不知道」报成「安全」是本工具最坏的失效方向**。", got, ClassUnresolved)
	}
	if len(sites[0].Resolved) != 0 {
		t.Errorf("Resolved = %v，期望空（不可判定时不得给出任何候选名）", sites[0].Resolved)
	}
}

// 门 2：变量必须按**函数作用域**解析。
//
// logsTable 在真实代码里被三个互不相干的函数各绑一次、返回三张不同的表。
// 按包级绑定会让三者都拿到「第一个」的值——输出一份看起来很正常的错误报告。
func TestBindingsAreFunctionScoped(t *testing.T) {
	sites := auditOne(t, map[string]string{
		"a.go": `package a

func q1(days int) {
	logsTable, alias := pick(days)
	_, _ = alias, ¤SELECT 1 FROM ¤ + logsTable + ¤ WHERE x = 1¤
}

func pick(days int) (string, string) {
	if days <= 7 {
		return "request_logs_hot AS r", "r"
	}
	return "request_logs_with_current_month AS r", "r"
}

func q2(days int) {
	logsTable, alias := pick2(days)
	_, _ = alias, ¤SELECT 1 FROM ¤ + logsTable + ¤ WHERE x = 1¤
}

func pick2(days int) (string, string) {
	return "request_logs_with_current_month AS r", "r"
}
`,
	})

	if len(sites) != 2 {
		t.Fatalf("sites = %d, want 2\n%v", len(sites), sites)
	}
	var v1Lines, safeLines []int
	for _, s := range sites {
		switch s.Classification() {
		case ClassReadsV1:
			v1Lines = append(v1Lines, s.Line)
		case ClassReadsCanonical:
			safeLines = append(safeLines, s.Line)
		default:
			t.Errorf("第 %d 行判为不可判定，解析结果 = %v", s.Line, s.Resolved)
		}
	}
	if len(v1Lines) != 1 {
		t.Errorf("判为读 v1 的点 = %v，期望恰好 1 个（q1 的 days<=7 分支）。\n"+
			"两个同名局部变量必须各自解析；按包级绑定会让它们都拿到第一个函数的值。\n%v",
			v1Lines, sites)
	}
	if len(safeLines) != 1 {
		t.Errorf("判为退役安全的点 = %v，期望恰好 1 个（q2 恒读视图）。\n%v", safeLines, sites)
	}
}

// 门 3：canonical 视图**不得**被判成 v1 读点。
//
// 视图名 `request_logs_with_current_month*` 同样以 `request_logs_` 开头，
// 用前缀判 v1 会把 6 个只读视图的拼接点报成读 v1——方向完全相反的假阳性。
func TestCanonicalViewIsNotAV1Relation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"hot 表", "request_logs_hot AS r", true},
		{"母表", "request_logs", true},
		{"bodies", "request_logs_bodies", true},
		{"canonical 视图", "request_logs_with_current_month AS r", false},
		{"canonical 视图变体", "request_logs_with_current_month_without_customer_id AS r", false},
		{"带引号", `"request_logs_hot"`, true},
		{"空串", "", false},
	}
	for _, c := range cases {
		if got := isV1Relation(c.in); got != c.want {
			t.Errorf("isV1Relation(%q) [%s] = %v, want %v", c.in, c.name, got, c.want)
		}
	}
}

// 门 4：条件性切换层必须按**最坏情况**算 v1。
//
// `if days <= 7 { return request_logs_hot }; return 视图` 这种形状在真实代码里有
// 6 处（maas/usage.go ×4、maas/credit_buckets.go、maas/consumption_detail.go）。
// 它不是「恒读 v1」，但也绝不是安全：它确实会在某个入参下命中，而扫描器看不见
// 那个条件。
func TestConditionalSwitchLayerCountsAsV1(t *testing.T) {
	sites := auditOne(t, map[string]string{
		"a.go": `package a

func pick(days int) string {
	if days <= 7 {
		return "request_logs_hot"
	}
	return "request_logs_with_current_month"
}

func q(days int) {
	tbl := pick(days)
	_ = ¤SELECT 1 FROM ¤ + tbl + ¤ WHERE x = 1¤
}
`,
	})
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want 1\n%v", len(sites), sites)
	}
	if got := sites[0].Classification(); got != ClassReadsV1 {
		t.Errorf("条件性切换层被判为 %v，期望 %v", got, ClassReadsV1)
	}
	if len(sites[0].Resolved) != 2 {
		t.Errorf("Resolved = %v，期望两个候选都被保留", sites[0].Resolved)
	}
}

// 门 5：完整字面量里的 FROM **不是**拼接点，不该被报出来。
//
// 判据钉在「片段以 FROM/JOIN 结尾」上：`FROM request_logs_hot` 是完整的，
// 文本扫描器看得见它，本工具的职责是补上**看不见**的那部分。把看得见的也报一遍
// 会让输出变噪声，噪声会让人忽略真正的条目。
//
// ⚠ 夹具里第 3 条是**这一道门能不能被变异测出来的关键**（第一次写漏了它，
// 变异验证实测：把 `\s+$` 锚点去掉，门仍然绿）。原因：一条**孤立**的完整字面量
// 不在 `+` 链里，ast.Inspect 根本不会访问它，所以锚点有没有都测不出差别。
// 真正能区分锚点的是「完整字面量**自己参与拼接**」这种形状——
// `完整SQL + 条件片段` 是条件查询拼接的标准写法，本项目里到处都是。
func TestCompleteRelationLiteralIsNotAConcatSite(t *testing.T) {
	sites := auditOne(t, map[string]string{
		"a.go": `package a

func q(cond string) {
	// ① 孤立完整字面量：看得见，不该报
	_ = ¤SELECT id FROM request_logs_hot WHERE x = 1¤
	// ② 拼接：看不见，要报
	_ = ¤SELECT 1 FROM ¤ + "request_logs_hot" + ¤ WHERE x = 1¤
	// ③ 完整字面量自己参与拼接：里面的关系名是**字面量**，仍看得见，不该报
	_ = ¤SELECT 1 FROM request_logs_hot WHERE x = 1¤ + cond
}
`,
	})
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want 1（只有 ②）\n%v", len(sites), sites)
	}
	if !isV1Relation(sites[0].Resolved[0]) {
		t.Errorf("解析结果 = %v，期望指向 v1", sites[0].Resolved)
	}
}

// 门 4：解析结果是**子查询 / UNION** 时，里面的 v1 关系名不许被漏掉。
//
// 这是一条 2026-10-05 实测出来的**自信的错误**：`admin/tenants.go` 的
// logsTable 是
//
//	(SELECT … FROM request_logs_hot
//	 UNION ALL
//	 SELECT … FROM request_logs) -- sqlreadguard:allow …
//
// 「取第一个 token」拿到的是 `(select`，不在 v1Tables 里，于是 tenants.go
// 被列进「退役安全」——而它读的是**两张 v1 底表**。
//
// 关键点：它和门 1 方向相同、**症状不同**。门 1 是不可判定被当成安全，
// 输出里还留着「不可静态解析」的标签，人能看出来；这一条解析**成功**了，
// 输出的形状完全正常，**没有任何视觉信号提示它是错的**。
// ⇒ 只写「不许把未知报成安全」的门，挡不住它。
func TestSubqueryResolvedToV1IsNotCalledRetirementSafe(t *testing.T) {
	const unionSubquery = `(SELECT tenant_id, ts, total_tokens, cost_usd
	                FROM request_logs_hot
	        UNION ALL
	               SELECT tenant_id, ts, total_tokens, cost_usd
	                FROM request_logs) -- sqlreadguard:allow R36-A1 漏热尾根修`

	// 正向：整段子查询必须判成 v1。
	if !isV1Relation(unionSubquery) {
		t.Errorf("isV1Relation(%q) = false，期望 true —— 它 UNION 了 request_logs_hot "+
			"与 request_logs 两张 v1 底表。判成 false 会让 tenants.go 落进「退役安全」。",
			"…UNION ALL…request_logs_hot…request_logs…")
	}

	// 走一遍完整链路，确认 Classification 也翻过来了（只测 isV1Relation 不够：
	// Classification 自己也有「sawV1 / sawOther」两支，改错那里照样绿）。
	s := Site{Resolved: []string{unionSubquery}}
	if got := s.Classification(); got != ClassReadsV1 {
		t.Errorf("Site.Classification() = %v，期望 ClassReadsV1", got)
	}

	// 端到端：真实形状（一段字面量被绑到变量，再拼进 FROM）。
	sites := auditOne(t, map[string]string{
		"a.go": `package a

func q() {
	logsTable := ¤(SELECT tenant_id, ts
	                FROM request_logs_hot
	        UNION ALL
	               SELECT tenant_id, ts
	                FROM request_logs)¤
	_ = ¤SELECT SUM(total_tokens) FROM ¤ + logsTable + ¤ WHERE tenant_id = $1¤
}
`,
	})
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want 1\n%v", len(sites), sites)
	}
	if got := sites[0].Classification(); got != ClassReadsV1 {
		t.Errorf("端到端 Classification() = %v，期望 ClassReadsV1 —— "+
			"这是 tenants.go 的真实形状，它读两张 v1 底表", got)
	}
}

// 门 5：子查询形态**不能**反过来把非 v1 判成 v1。
//
// 门 4 的修法是「扫出所有 FROM/JOIN 后面的关系名」，它天然多报。
// 这里钉住多报的**边界**：canonical 视图名与 session 族名都不得被算成 v1
// 底表——否则这道修法会把整个 §9.45 的假阳性方向重新打开（视图名之所以
// 安全，恰恰是因为它不在 v1Tables 这个精确集合里）。
func TestSubqueryScanStillExcludesCanonicalView(t *testing.T) {
	for _, tc := range []struct{ name, resolved string }{
		{"canonical 视图裸名", "request_logs_with_current_month"},
		{"canonical 视图带别名", "request_logs_with_current_month rl"},
		{"canonical 包装视图", "request_logs_with_current_month_without_customer_id AS r"},
		{"session 族", "session_turns_hot rl"},
		{"session 族带 schema", "public.session_turns_hot"},
		{"带双引号的 canonical 视图", `"request_logs_with_current_month" AS r`},
		{"子查询里只有 session 族", "(SELECT tenant_id FROM session_turns UNION ALL SELECT tenant_id FROM session_dim)"},
		{"子查询里只有 canonical 视图", "(SELECT tenant_id FROM request_logs_with_current_month UNION ALL SELECT tenant_id FROM session_turns)"},
	} {
		if isV1Relation(tc.resolved) {
			t.Errorf("isV1Relation(%q) = true，期望 false —— %s 不得被算成 v1 底表",
				tc.resolved, tc.name)
		}
	}
	// 对照组 A：同样两条，把其中一个换成 v1 底表就必须翻成 true。
	// 没有这一条，上面那张表可以靠「永远返回 false」而全绿。
	if !isV1Relation("(SELECT tenant_id FROM session_turns UNION ALL SELECT tenant_id FROM request_logs)") {
		t.Error("对照组 A：子查询里出现 request_logs 时必须判 true，否则上一道门是恒假的")
	}
	// 对照组 B：带双引号的 v1 关系名。`from "request_logs_hot"` 在 SQL 里合法，
	// 而 firstRelationToken 的剥引号是这条路径上**唯一**处理它的地方。
	// 少了它，上表那行 `"request_logs_with_current_month" AS r` 仍然判 false
	//（视图名本来就不在 v1Tables 里，剥不剥引号都一样），
	// 于是整张表**不会**因为剥引号被删而变红 —— 变异验证实测确实如此。
	// ⇒ 必须单独给一个「剥引号被删就变红」的正例，否则这道门测不到它。
	if !isV1Relation(`"request_logs_hot"`) {
		t.Error("对照组 B：带双引号的 v1 关系名必须判 true")
	}
}

// 门 6：`_test.go` 里的拼接点不得进入审计结果。
//
// # 为什么这不是「测试不算生产代码」
//
// 排除 `_test.go` 的真实理由是：测试文件会把**断言消息**当成关系名，而本工具
// 会把那个「关系名」照常分类。2026-10-05 实测到的输出：
//
//	admin/request_logs_read_inventory_test.go:211
//	  操作数=*ast.BasicLit
//	  → requestLogsReadInventory — S4 would silently stop feeding them:
//	  %s
//
// 落在**「解析到 canonical 视图 / 会话族（退役安全）」**桶里。
// 那不是噪声，是**一句英文断言被当成了一张表名**。本工具的输出是退役清单的
// 输入，一个凭空出现的表名会让人以为某个文件读过 v1（或反过来，让人以为它安全）。
//
// 排除之后，口径与 requestLogsReadInventory / exposure 抽取器一致：
// 总体都是「非 _test.go 的生产文件」。
func TestTestFilesAreExcludedFromAudit(t *testing.T) {
	// 这段内容**必须**能产出拼接点——否则下面的排除断言可能是因为夹具本身
	// 写坏了而绿。所以先在非测试文件名下证明它会。
	body := `package a

var requestLogsReadInventory = map[string]int{"x.go": 1}

func check() error {
	return fmt.Errorf(¤%%d file(s) read request_logs but are absent from ¤ +
		requestLogsReadInventory + ¤ — S4 would silently stop feeding them:\n  %%s¤)
}
`
	prod := auditOne(t, map[string]string{"a.go": body})
	if len(prod) != 1 {
		t.Fatalf("对照组：同样的内容放在 a.go 里应产出 1 个拼接点，实得 %d —— "+
			"夹具写坏了，下面的排除断言就没有意义\n%v", len(prod), prod)
	}
	// 只断言「产出了拼接点」，**不**断言它的 Class。
	//
	// 曾在这里写死 `== ClassReadsCanonical`，实测是 ClassUnresolved：操作数
	// requestLogsReadInventory 绑的是 map 字面量，resolve 解析不出字符串。
	// 全仓真实输出里那条落在「退役安全」桶，是因为那边的绑定形态不同。
	// ⇒ 承重事实是「测试文件不该产出审计条目」，与分类无关；
	// 把分类写进断言只会让夹具与仓库的绑定形态耦合，然后给出一次假红。
	t.Logf("对照组：a.go 里的同一段产出 %d 个条目，class=%v（不作为判据）",
		len(prod), prod[0].Classification())

	// 同一份内容放到 _test.go 里，必须一个都不报。
	testFiles := auditOne(t, map[string]string{"a_test.go": body})
	if len(testFiles) != 0 {
		t.Errorf("_test.go 里的 %d 个拼接点不该进审计结果（测试文件会把断言消息当成关系名）：\n  %v",
			len(testFiles), testFiles)
	}
}

// 门 7：`IS DISTINCT FROM` 不是 FROM 子句。
//
// 2026-10-05 实测：`fragmentTailRE` 是 `(?i)\b(from|join)\s+$`，
// 它分不清两种 `from`：
//
//	… LEFT JOIN          + tbl     ← FROM 子句，拼的是**关系名**（要报）
//	… IS DISTINCT FROM   + balArg  ← 比较运算符，拼的是**值**（不该报）
//
// 后果实测：admin/provider_credential.go:637 以「不可静态解析」进了全仓报告，
// 而清单的 Consequence 那一栏要写「退役时会怎样」——
// 对着一个比较运算符写不出有意义的话。
//
// 方向上这是多报（本工具自述的安全方向），所以它不会让人漏掉 v1 读点；
// 但它让清单里多一条无法评估的条目 ⇒ 修工具。
func TestIsDistinctFromIsNotARelationFragment(t *testing.T) {
	// 负向：比较运算符不是关系名拼接点。
	for _, frag := range []string{
		"balance_usd IS DISTINCT FROM ",
		"x IS NOT DISTINCT FROM\t",
		"  IS DISTINCT FROM\n",
	} {
		if isRelationFragmentTail(frag) {
			t.Errorf("isRelationFragmentTail(%q) = true，期望 false —— "+
				"这是比较运算符，不是 FROM 子句", frag)
		}
	}
	// 对照组：真正的 FROM/JOIN 子句必须仍然被认出来。
	// 没有这一条，上面三条可以靠「永远返回 false」而全绿。
	for _, frag := range []string{
		"SELECT COUNT(*) FROM ",
		"SELECT 1 FROM session_turns\n  LEFT JOIN ",
		"DELETE FROM ",
		"INSERT INTO x SELECT * FROM ",
		"\n\t\tFROM ",
	} {
		if !isRelationFragmentTail(frag) {
			t.Errorf("isRelationFragmentTail(%q) = false，期望 true —— "+
				"这是真的 FROM/JOIN 子句", frag)
		}
	}
	// 端到端：整段表达式里含 IS DISTINCT FROM 时，不应产出站点。
	sites := auditOne(t, map[string]string{
		"a.go": `package a

func q(balArg string) {
	// ① 比较运算符：不该报
	_ = ¤balance_usd IS DISTINCT FROM ¤ + balArg
	// ② 真的 FROM：该报
	_ = ¤SELECT 1 FROM ¤ + ¤request_logs_hot¤ + ¤ WHERE x = 1¤
}
`,
	})
	if len(sites) != 1 {
		t.Fatalf("sites = %d, want 1（只有 ②）\n%v", len(sites), sites)
	}
}

// TestPackageLevelVarSitesAreEnumerated is the control pair for the §9.237 fix.
//
// Before it, a SQL string held in a package-level `var` was invisible to the
// site enumeration: its bindings were collected into env.globals for
// *resolution*, but only function bodies were scanned, so the site itself was
// never reported. Two real instances existed and neither was reported —
// `admin/compression_stats.go`'s compressionStatsEstimatedOrigSQL and both SQL
// vars in `domains/sessionforensics/export.go`.
//
// The fixture is written so the two container kinds are the **only** difference
// between the two cases below. If the function-body form is found and the
// package-var form is not, the enumeration is still function-only and the
// regression is back.
//
// The package-var case is deliberately built out of a concat whose operand is a
// package-level function call, because that is the shape the retirement switch
// layers produce (`const` → `var` happens precisely when a function call is
// introduced).
func TestPackageLevelVarSitesAreEnumerated(t *testing.T) {
	// The literal must END with `FROM`/`JOIN` (trailing space trimmed) — that is
	// what isRelationFragmentTail requires. My first fixture used
	// "…FROM session_turns ", which ends with the *table name* rather than the
	// keyword, so zero sites were found and the test failed for the wrong
	// reason. A fixture that matches nothing cannot distinguish "the fix is
	// gone" from "my fixture is wrong".
	//
	// And a real func declaration, not a variable holding a func literal:
	// collectFuncReturns only records func declarations, so a var-held func
	// literal would be unresolvable for reasons unrelated to what this test is
	// about — while the second half asserts the var site *resolves*.
	const src = `package fixture

// A real func declaration: this is the shape production has
// (dbpkg.SessionBodiesSourceSQL), not a var holding a func literal.
func turnsSource() string { return "request_logs_hot" }

var packageLevelSQL = "SELECT * FROM " + turnsSource() + " t"

func inFunction() string {
	return "SELECT * FROM " + turnsSource() + " t"
}
`
	// Line numbers are derived from the fixture text, not hardcoded. An earlier
	// version hardcoded them and went stale the moment a comment line was added
	// to the fixture above — the test then failed for a reason that had nothing
	// to do with the behaviour it exists to pin.
	lineOf := func(marker string) int {
		for i, ln := range strings.Split(src, "\n") {
			if strings.Contains(ln, marker) {
				return i + 1
			}
		}
		t.Fatalf("fixture no longer contains %q — update the fixture, not the expectation", marker)
		return 0
	}
	varLine, funcLine := lineOf("var packageLevelSQL"), lineOf(`return "SELECT * FROM "`)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sites, err := scanParsed(fset, map[string]*ast.File{"fixture.go": file})
	if err != nil {
		t.Fatalf("scanParsed: %v", err)
	}

	seen := map[int]int{}
	for _, s := range sites {
		if s.File == "fixture.go" {
			seen[s.Line]++
		}
	}

	if seen[varLine] == 0 {
		t.Errorf("包级 var 里的拼接点（第 %d 行）没有被枚举出来 —— "+
			"§9.237 的修复回退了，或从未生效。实测点位：%+v", varLine, sites)
	}
	if seen[funcLine] == 0 {
		t.Fatalf("函数体里的拼接点（第 %d 行）都没找到，fixture 本身失效：%+v", funcLine, sites)
	}
	// The var form must resolve, not merely be counted. A site reported with no
	// resolved candidate lands in the unresolved bucket for the wrong reason,
	// and the distinction between "unseen" and "seen but unresolvable" — which
	// is exactly what the manifest gate exists to surface — would be lost.
	for _, s := range sites {
		if s.Line == varLine && len(s.Resolved) == 0 {
			t.Errorf("包级 var 的点位被报告为不可判定（%+v）；它应当与函数体里的同形点位"+
				"一样解析到关系名 —— 否则「看不见」与「看得见但解析不出」混成一类", s)
		}
	}
}

// TestPackageLevelVarResolvesAgainstGlobalsNotStaleLocals pins the enterFunc()
// call that precedes the package-level var scan.
//
// Found by mutation Q4: removing it left every gate green, because the
// single-file fixture has no name for a stale local to shadow. The hazard is
// real though — resolve() consults `locals` before `globals`, files are scanned
// in sorted order, and nothing else clears `locals` between them. A package
// initialiser in file B can therefore resolve an identifier using a local left
// behind by the last function of file A, and it will look resolved.
//
// That is the worst kind of wrong: a confident answer, produced from a binding
// the initialiser could not actually see at package level. The two-file fixture
// is the only way to exercise it, which is why this is a separate test rather
// than an extra case in the one above.
func TestPackageLevelVarResolvesAgainstGlobalsNotStaleLocals(t *testing.T) {
	// Both files declare `tableFor`; the local inside a's function must not
	// decide how b's package var resolves.
	//
	// ⚠ Two fixture properties are load-bearing, and I got both wrong first.
	//
	// 1. The shadowing local must be a **string** binding. `tableFor := func()
	//    string {...}` is not recorded by collectStringBindings at all, so no
	//    stale local ever existed and the test stayed green with the mutation
	//    applied — it proved nothing.
	// 2. b.go must reference `tableFor` as a **bare identifier**. With
	//    `tableFor()` the operand is a CallExpr, and resolve() returns
	//    env.funcs[name] without ever consulting locals or globals — so the
	//    hazard cannot manifest at all, and again the test was vacuously
	//    green. A func call is *immune* to stale locals; only a bare
	//    identifier is exposed. That is why Q4 went uncaught twice.
	//
	// Sorted file order (a.go before b.go) plus locals-never-cleared-between-
	// files is the mechanism; enterFunc() in the GenDecl branch is the fix.
	srcA := `package fixture
func a() string {
	tableFor := "credential_model_bindings"
	return "SELECT * FROM " + tableFor
}
`
	srcB := `package fixture
var tableFor = "session_turns_hot"
var packageSQL = "SELECT * FROM " + tableFor
`

	fset := token.NewFileSet()
	parsed := map[string]*ast.File{}
	for name, src := range map[string]string{"a.go": srcA, "b.go": srcB} {
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		parsed[name] = f
	}
	sites, err := scanParsed(fset, parsed)
	if err != nil {
		t.Fatalf("scanParsed: %v", err)
	}

	bSites, hit := 0, false
	for _, s := range sites {
		if s.File != "b.go" {
			continue
		}
		bSites++
		for _, r := range s.Resolved {
			if strings.Contains(r, "credential_model_bindings") {
				t.Fatalf("b.go 的包级 var 被 a.go 留下的局部绑定解析成了 %q（%+v）—— "+
					"它只能看到自己的包级 tableFor。跨文件的 stale locals 泄漏", r, s)
			}
			if r == "session_turns_hot" {
				hit = true
			}
		}
	}
	if len(sites) == 0 {
		t.Fatal("夹具没有产出任何点位，fixture 失效")
	}
	// 上行证人：断言「解析到了包级 tableFor」而不只是「没解析到脏值」。
	// 只写否定断言的话，夹具哪天不再产出 b.go 点位也会照样绿 ——
	// 那正是这个夹具已经栽过一次的形状。
	if bSites == 0 || !hit {
		t.Fatalf("b.go 的包级 var 点位没解析到自己的表名（bSites=%d sites=%+v）—— "+
			"夹具失效，不能证明任何事", bSites, sites)
	}
}

// auditWithSubdirs 与 auditOne 相同，但允许 "db/x.go" 这种带目录的文件名
// （§9.257 的跨包夹具必须真的摆在 db/ 下，因为 crossProviderFuncs 按目录找包）。
func auditWithSubdirs(t *testing.T, files map[string]string) []Site {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(render(body)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	sites, err := AuditRepo(dir)
	if err != nil {
		t.Fatalf("AuditRepo: %v", err)
	}
	return sites
}

func findSite(t *testing.T, sites []Site, file string) Site {
	t.Helper()
	for _, s := range sites {
		if strings.HasSuffix(filepath.ToSlash(s.File), file) {
			return s
		}
	}
	t.Fatalf("没找到 %s 的拼接点（全部 %d 处）", file, len(sites))
	return Site{}
}

// 门 14（§9.257）：**跨包切换层必须能被解析**，且解析出的是**两个分支的并集**。
//
// # 这道门对应本轮修掉的两个真缺陷
//
// 缺陷 A：`calleeName` 对 `dbpkg.X()` 返回 `""`，于是 19 处 bodies 读方 + 7 处
// logs 读方全部落「不可判定」（unresolved 53 处）。
//
// 缺陷 B（更隐蔽，**我自己在这一轮里写的**）：`env.crossAlias` 只在扫描阶段设置，
// funcs/globals 收集阶段没设。于是
//
//	func logsSourceFromSQL() string {
//	    if native { return db.SessionFamilyTurnsSourceSQL() + " rl" }   // ← 这一支解析不出
//	    return "request_logs_with_current_month rl"                     // ← 这一支解析得出
//	}
//
// 收集到的并集**只剩 v1 那一支**。★ 危险在于它的表现：不是「不可判定」，
// 而是**一个看起来完全正常的成功解析结果**（少一半，但格式规整、数量合理）。
// 实测症状：`admin/logs.go:632` 只解析出 `rl | request_logs_with_current_month rl`，
// 会话族那一支整个不见了。
//
// ⇒ 夹具刻意复刻这个形状：跨包函数的**一臂是另一跨包函数调用**。
// 只测「跨包能解析」会漏掉缺陷 B —— 那道断言在缺陷 B 下**照样绿**。
func TestCrossPackageSwitchLayerResolvesToBothArms(t *testing.T) {
	sites := auditWithSubdirs(t, map[string]string{
		"db/source.go": `package db

func SessionTurnsSourceSQL() string {
	return "(SELECT 1 FROM public.session_turns_hot t)"
}
`,
		// ★ 切换层**住在消费包**（admin），跨包调用是**带包前缀**的。
		// 这是缺陷 B 的真实形状：若把它放进 db 包内部，调用就成了同包
		// `SessionTurnsSourceSQL()`，`calleeName` 直接拿到名字、根本不需要
		// crossAlias ⇒ 夹具测不到任何东西（第一版夹具就犯了这个错，
		// 于是 MUT-B 变异下这道门**照样绿**）。
		"admin/logs_turns_source.go": `package admin

import (
	dbpkg "github.com/kaixuan/llm-gateway-go/db"
)

var nativeTurnsRead bool

func logsSourceFromSQL() string {
	if nativeTurnsRead {
		return dbpkg.SessionTurnsSourceSQL() + " rl"
	}
	return "request_logs_with_current_month rl"
}
`,
		"admin/logs.go": `package admin

func q() string {
	logsFrom := logsSourceFromSQL()
	return "SELECT COUNT(*) FROM " + logsFrom + " WHERE 1=1"
}
`,
	})

	s := findSite(t, sites, "admin/logs.go")
	if len(s.Resolved) == 0 {
		t.Fatalf("跨包切换层解析不出（不可判定）：%s", s)
	}
	joined := strings.Join(s.Resolved, " | ")
	// 缺陷 A 的判据：v1 那一臂
	if !strings.Contains(joined, "request_logs_with_current_month") {
		t.Errorf("并集里没有 v1 那一臂 —— 跨包函数调用没解析上。实际：%q", joined)
	}
	// ★ 缺陷 B 的判据：会话族那一臂**必须也在**。
	// 少了它，解析结果是「成功但只有一半」，而缺陷 A 测不出来。
	if !strings.Contains(joined, "public.session_turns_hot") {
		t.Errorf("★ 并集里**丢了会话族那一臂**（`return 另一跨包函数() + \" rl\"` 那一支）——\n"+
			"这正是 §9.257 修掉的缺陷 B：它的表现不是「不可判定」，而是**一个少了分支的\n"+
			"成功结果**，看起来完全正常。实际：%q", joined)
	}
}

// 门 15（§9.258）：v1 臂视图集合必须从 DDL **推导**出来，且要认得 schema 限定名。
//
// # 为什么要单独一道
//
// 第四桶是在 `viewsWithV1Arm` 返回空时**恒为 0**的，而且那个 0 看起来
// 和「仓库里真的没有 v1 臂视图」完全一样。本轮实测就在这里卡住过一轮：
// `fromRelationRE` 的捕获范围不含 schema 限定，于是视图 DDL 里的
// `FROM public.request_logs_bodies_hot` 只捕获到 `public` ⇒ 推导器认不出任何视图。
//
// # 夹具照着真实 DDL 抄（§9.257.3 的教训）
//
// 三条形态都照抄 `sql/objects/views/request_logs_bodies_with_current_month.sql`：
//
//	① `CREATE VIEW public.<name> AS`（带 schema）
//	② `   FROM public.<v1 底表>`（**带 schema 的关系名**）
//	③ 注释里提到 v1 表（**必须不算数**）
func TestV1ArmViewsAreDerivedFromDDL(t *testing.T) {
	dir := t.TempDir()
	vdir := filepath.Join(dir, "sql", "objects", "views")
	if err := os.MkdirAll(vdir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(vdir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 真·有 v1 臂
	write("request_logs_bodies_with_current_month.sql",
		"--\n-- Name: x; Type: VIEW\n--\n\nCREATE VIEW public.request_logs_bodies_with_current_month AS\n"+
			" SELECT request_logs_bodies_hot.request_id FROM public.request_logs_bodies_hot\n"+
			"UNION ALL\n SELECT request_logs_bodies.request_id FROM public.request_logs_bodies;\n")
	// 真·无 v1 臂（会话族投影）
	write("session_only_view.sql",
		"CREATE VIEW public.session_only_view AS\n SELECT 1 FROM public.session_turns t\n"+
			" WHERE 'request_logs' <> ''; -- ← 注释/字面量里提到 v1 表，不该算数\n")
	// ★ 纯字面量提及，**绝不能**被算成有臂
	write("mentions_only.sql",
		"-- 这个视图的历史实现读 request_logs，已改\nCREATE VIEW public.mentions_only AS SELECT 1;\n")

	got := viewsWithV1Arm(dir)
	if len(got) == 0 {
		t.Fatalf("一个视图都没推出来 —— ★ 最可能的原因：`fromRelationRE` 的捕获范围被改窄、" +
			"不再吃 schema 限定名（`FROM public.request_logs_bodies_hot` 只捕获到 `public`）。" +
			"本轮就卡在这里一轮，而第四桶当时**恒为 0 且看起来正常**。")
	}
	if !got["request_logs_bodies_with_current_month"] {
		t.Errorf("request_logs_bodies_with_current_month 应被判为含 v1 臂，实际集合=%v", v1ArmViewNames(got))
	}
	if got["session_only_view"] {
		t.Errorf("session_only_view 的 body 里没有 FROM v1 底表（只有字面量与注释提及），"+
			"不该被判成含 v1 臂。集合=%v", v1ArmViewNames(got))
	}
	if got["mentions_only"] {
		t.Errorf("mentions_only 只在**注释**里提到 request_logs，不该被判成含 v1 臂。集合=%v",
			v1ArmViewNames(got))
	}
}

// 门 16（§9.260）：推导器必须认得**运行时由 composer 创建、没有仓库 DDL 文件**的视图。
//
// 真库 `pg_class` 里有 4 个 `request_logs*_with_current_month*` 视图，
// 4 个**全部含 v1 臂**；而 `sql/objects/views/` 的 54 个 DDL 文件只覆盖其中 2 个。
// 缺的两个由 composer 在运行时建（`db/request_logs_view_schema.go:128` / `:140`）。
//
// ⇒ 第一版只看 DDL 目录，于是读那两个视图的文件被判成 **canonical**
// —— 方向是**「把有风险的报成安全」**，也就是本工具最坏的失效方向。
func TestV1ArmViewsIncludeComposerDefinedOnes(t *testing.T) {
	set := viewsWithV1Arm(repoRoot(t))
	want := []string{
		"request_logs_with_current_month",
		"request_logs_with_current_month_without_customer_id",          // 无 DDL 文件
		"request_logs_with_current_month_without_request_class_due_at", // 无 DDL 文件
		"request_logs_bodies_with_current_month",
	}
	for _, w := range want {
		if !set[w] {
			t.Errorf("视图 %q 没被推出含 v1 臂。\n"+
				"★ 最可能的原因：它由 composer 在**运行时**创建、仓库里没有 DDL 文件 ⇒ "+
				"推导器只读 sql/objects/views/ 就会漏。\n"+
				"真库 pg_class 里有 4 个 request_logs*_with_current_month* 视图，**全部含 v1 臂**；"+
				"漏掉任何一个都会把它下面的读方判成 canonical（把有风险的报成安全）。\n"+
				"当前推出集合：%v", w, v1ArmViewNames(set))
		}
	}
}
