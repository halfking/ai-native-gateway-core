package sqlguard

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSQLLiteralGuard_SeesPastConcatenation 与 TestSQLLiteralGuard_CoverageBoundary
// 是本门的**自证**（量具自己也要被量）：证明它有牙，同时把它的**覆盖边界**
// 写进门里，而不是只写在报告里。
//
// ★★ 为什么必须有边界声明（2026-10-29，模态接受判据同源化那一轮实测出来的）：
//
//	本门用 **Go AST** 扫 raw string ⇒ `+` 拼接出来的两半是两个 BasicLit ⇒
//	**AST 本身**不会在拼接点失明（这一点与 provider 侧那道**词法**门不同——
//	词法门按「行尾单个反引号 = 块开头」配对，拼接点会失明，见
//	provider/sql_comment_syntax_test.go 的 TestSQLCommentScanner_SeesPastConcatenation）。
//
//	★ 但本门**认 SQL 的方式是「形状」**：字面量的第一个有效行必须以语句
//	关键字开头（SELECT/INSERT/UPDATE/WITH/…）。⇒ 后果：
//	  · 一个**被 `+` 拆开的 SQL 语句的后半段**，它自己的第一个有效行不是
//	    语句关键字 ⇒ **不被认成 SQL** ⇒ 后半段里的 `//` 注释**看不见**。
//	  · 一个 **SQL 表达式片段**（例如 2026-10-29 抽出来的「模态接受臂唯一
//	    片段」——它是一条布尔表达式，不是语句）⇒ 同样**不被认成 SQL** ⇒
//	    **不在本门的覆盖范围内**。
//
//	⇒ ★ 所以「仓里有一道 repo-wide 的 SQL 注释门」这句话**不等于**
//	   「所有 SQL 片段都被覆盖」。表达式片段的覆盖归 provider 侧那道**词法**
//	   门（它扫整个包的所有 raw string，不问形状）⇒ 两道门必须同时存在。
//	⇒ ★ 这也是本轮把模态接受臂抽成唯一片段时**必须**同时做的事：把 provider
//	   侧那道词法门的扫描范围从「client.go 一个文件」扩到「整个 provider 包」
//	   （手写清单会腐化：判据搬家时没同步的清单会让门继续守着旧家）。
func TestSQLLiteralGuard_SeesPastConcatenation(t *testing.T) {
	dir := t.TempDir()
	// 夹具：一个**被 `+` 拆开**的 SQL 字面量，`//` 注释落在**后半段**。
	// ★ 两半都是合法的 Go 源码（门会跳过解析不过的文件）。
	src := "package fixture\n\nfunc q() string {\n\treturn `\n   SELECT 1\n   FROM t WHERE (` + arm + `\n   // go style comment\n   AND x = 1\n`\n}\n"
	if err := os.WriteFile(filepath.Join(dir, "fixture.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("写夹具失败：%v", err)
	}
	findings, sqlLiterals, err := findGoCommentsInSQLLiterals(dir)
	if err != nil {
		t.Fatalf("扫描夹具失败：%v", err)
	}
	if sqlLiterals == 0 {
		t.Fatal("夹具里一个 SQL 字面量都没认出来 —— 检测器坏了（不是夹具坏了）")
	}
	if len(findings) != 1 {
		t.Errorf("拼接形态命中 %d 条，want 1 —— ★门在拼接形态下没有牙 ⇒ 拼接之后的那一半 SQL 里的 `//` 注释对门隐形", len(findings))
	}
}

// TestSQLLiteralGuard_CoverageBoundary 登记本门的覆盖边界：**只认 SQL 语句
// 形态的字面量**。
//
// ★ 为什么把边界写成断言（而不是注释）：LESSONS §531 通用式六「声明 > 事实
//
//	要靠覆盖边界声明来消」。⇒ 边界一旦漂了（有人把 isSQL 放宽/收紧），
//	这条断言立刻红 ⇒ 边界不能被悄悄改掉。
//
// ★ 三档：
//
//	① SQL 语句形态（第一个有效行以 SELECT 开头）⇒ **被覆盖**；
//	② SQL **表达式**片段（第一个有效行不是语句关键字）⇒ **不被本门覆盖**
//	   （它的覆盖归 provider 侧那道词法门）；
//	③ 非 SQL 文本（提示词）⇒ 不被覆盖。
func TestSQLLiteralGuard_CoverageBoundary(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		// ① 语句形态 + `//` ⇒ 必须被检出。
		"stmt.go": "package fixture\n\nfunc a() string {\n\treturn `\n   SELECT 1\n   // go style comment\n   FROM t\n`\n}\n",
		// ② 表达式片段 + `//` ⇒ 本门**不覆盖**（provider 侧词法门覆盖它）。
		"expr.go": "package fixture\n\nfunc b() string {\n\treturn `\n   $MODALITY = ''\n   // go style comment\n   OR $MODALITY = 'text'\n`\n}\n",
		// ③ 提示词 + `//` ⇒ 不被覆盖。
		"prompt.go": "package fixture\n\nfunc c() string {\n\treturn `\n   你是一个助手。\n   // 补充说明\n   请回答。\n`\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("写夹具 %s 失败：%v", name, err)
		}
	}
	findings, _, err := findGoCommentsInSQLLiterals(dir)
	if err != nil {
		t.Fatalf("扫描夹具失败：%v", err)
	}
	for _, f := range findings {
		if f.file != "stmt.go" {
			t.Errorf("命中 %s:%d —— ★本门只覆盖 SQL **语句**形态的字面量；表达式片段与提示词不在范围内（它们的覆盖归 provider 侧词法门）⇒ 边界漂了", f.file, f.line)
		}
	}
	if len(findings) != 1 {
		t.Errorf("命中 %d 条，want 1（只有 stmt.go 里的那一条）", len(findings))
	}
}
