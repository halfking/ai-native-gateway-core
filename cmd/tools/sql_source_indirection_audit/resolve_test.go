package main

import (
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
