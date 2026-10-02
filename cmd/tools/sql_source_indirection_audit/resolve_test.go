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
