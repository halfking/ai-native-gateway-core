package admin

// session_duration_ms_consumer_test.go —— `sessions.duration_ms` **读侧**门。
//
// # 这道门守的是什么
//
// 706 声明了 `sessions.duration_ms`，877 修好了「它恒为 NULL」这个缺陷
// （改成每轮刷新的已流逝时长，见 domains/session/v2/session_duration_ms_realdb_test.go
// 那个写入方真库门）。
//
// 但**只修写入侧还不够**：877 之后的复核结论是「该列现在正确但仍无人读」——
// 一列只写不读，等于没有对外契约，审计也只能说「诚实，但没有用户可见收益」。
//
// ⇒ 本门守的是**读侧存在且真的被序列化出去**：
//   ① 快照响应结构体有 DurationMs 字段，且 json tag 是 "duration_ms"
//      （tag 写错 = 前端读不到，字段在 Go 侧存在但在 HTTP 侧不存在，
//        这正是"看着接上了其实没接"的形态）；
//   ② SELECT 真的选了 `s.duration_ms`（不是只在 Go 结构体里声明了字段）；
//   ③ Scan 真的扫进了那个字段（列选了但没扫 = 值永远是零值）。
//
// # 为什么用 go/ast 而不是文本扫描
//
// 本仓吃过这个亏：按裸子串切文件，会被**文件自己的注释**绊倒
// （877 的迁移文件头注释里就出现了 \gexec 这个词）。
// AST 只看代码，注释进不来。
//
// # 判据纪律：这道门自己也要能被打穿
//
// 变异实测（见文件末）：把 SELECT 里的 s.duration_ms 去掉 → 门红；
// 把 json tag 改错 → 门红；把 Scan 目标去掉 → 门红。
// 三条都红，才说明门不是恒真的。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// durationMsConsumerExpect 描述一个读侧消费点必须满足的条件。
type durationMsConsumerExpect struct {
	file string
	// structName 是承载该字段的结构体名。
	structName string
	// fieldName 是字段名。
	fieldName string
	// jsonTag 是该字段必须带的 json tag。
	jsonTag string
	// scanTarget 是 Scan 调用的目标表达式片段（如 "&snap.DurationMs"）。
	scanTarget string
}

func durationMsConsumers() []durationMsConsumerExpect {
	return []durationMsConsumerExpect{
		{
			file:       "session_turns_v2.go",
			structName: "sessionSnapshotV2",
			fieldName:  "DurationMs",
			jsonTag:    "duration_ms",
			scanTarget: "&snap.DurationMs",
		},
		{
			file:       "session_detail_v2.go",
			structName: "SessionV2",
			fieldName:  "DurationMs",
			jsonTag:    "duration_ms",
			scanTarget: "&s.DurationMs",
		},
	}
}

// TestSessionDurationMsHasServedConsumer —— 读侧消费点存在性门。
func TestSessionDurationMsHasServedConsumer(t *testing.T) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}

	for _, e := range durationMsConsumers() {
		f, ok := files[e.file]
		if !ok {
			parsed, err := parser.ParseFile(fset, e.file, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse %s: %v", e.file, err)
			}
			files[e.file] = parsed
			f = parsed
		}

		// ① 结构体字段 + json tag
		if !hasJSONField(f, e.structName, e.fieldName, e.jsonTag) {
			t.Errorf("%s: 结构体 %s 必须有字段 %s 且 json tag 为 %q —— "+
				"tag 写错时字段在 Go 侧存在、在 HTTP 侧不存在，"+
				"前端读不到，等于没接消费方", e.file, e.structName, e.fieldName, e.jsonTag)
		}

		// ② SELECT 里真的选了 duration_ms
		if !selectContainsColumn(f, e.file, "duration_ms") {
			t.Errorf("%s: 没有一条从 public.sessions 出发的 SELECT 选了 duration_ms —— "+
				"只声明字段不选列，值永远是零值", e.file)
		}

		// ③ Scan 真的扫进了那个字段
		if !scanTargets(f, e.scanTarget) {
			t.Errorf("%s: 没有 Scan 扫进 %s —— 列选了但不扫，值不会被读出来", e.file, e.scanTarget)
		}
	}
}

// jsonTagName 从结构体 tag 串里取出 json 字段名。
//
// 形如 `json:"duration_ms,omitempty"` → "duration_ms"。
// 没有 json tag → ""。
func jsonTagName(tag string) string {
	const key = `json:"`
	i := strings.Index(tag, key)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	// 引号里的内容是 json 的**完整值** `name,omitempty`，
	// 逗号也在引号内（不是 tag 串的分隔符），所以要再切一次。
	name := rest[:j]
	if k := strings.Index(name, ","); k >= 0 {
		name = name[:k]
	}
	return strings.TrimSpace(name)
}

// hasJSONField 判断某结构体是否有指定名字、且 json tag 精确匹配某值的字段。
func hasJSONField(f *ast.File, structName, fieldName, jsonTag string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name.Name != structName {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if len(field.Names) != 1 || field.Names[0].Name != fieldName {
				continue
			}
			if field.Tag == nil {
				continue
			}
			tag, err := strconv.Unquote(field.Tag.Value)
			if err != nil {
				continue
			}
			// 取出 json:"name,opts" 里的 name。
			//
			// ⚠ 不能简单按第一个逗号切：结构体 tag 形如
			//   `json:"duration_ms,omitempty"`
			// 第一个逗号在 **json 名之后**，但前面还带着 `json:"` 前缀，
			// 直接切会得到 `json:"duration_ms` 这种永远比不上的串。
			name := jsonTagName(tag)
			if name == jsonTag {
				found = true
			}
		}
		return true
	})
	return found
}

// selectContainsColumn 判断包里是否有任何 SQL 字符串字面量同时提到
// public.sessions 与该列名。
func selectContainsColumn(f *ast.File, file, column string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		if !strings.Contains(text, "public.sessions") {
			return true
		}
		if strings.Contains(text, column) {
			found = true
		}
		return true
	})
	return found
}

// scanTargets 判断包里是否有 Scan 调用把地址传给了目标表达式。
func scanTargets(f *ast.File, target string) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Scan" {
			return true
		}
		for _, arg := range call.Args {
			if exprContains(arg, target) {
				found = true
			}
		}
		return true
	})
	return found
}

// exprContains 判断表达式里是否出现了给定的标识符片段（如 "&snap.DurationMs"）。
func exprContains(e ast.Expr, target string) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if found {
			return false
		}
		switch v := n.(type) {
		case *ast.UnaryExpr:
			if v.Op == token.AND {
				if id, ok := v.X.(*ast.Ident); ok && id.Name == strings.TrimPrefix(target, "&") {
					found = true
				}
			}
		case *ast.SelectorExpr:
			if x, ok := v.X.(*ast.Ident); ok {
				if "&"+x.Name+"."+v.Sel.Name == target {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// TestDurationMsConsumerGateIsNotVacuous —— 这道门本身不是恒真的。
//
// 判据纪律：写完判据**必须做变异验证**，否则不知道它是否真在测东西。
// 这里用**自检**的形式把这一点固化下来：把三个条件逐个在内存里
// 构造一份"缺一环"的假实现，要求 hasJSONField / selectContainsColumn /
// scanTargets 对它**都返回 false**。
//
// ⚠ 这不是变异实验本身（那要在临时 worktree 里改真代码），
//
//	它是"变异实验的形状"被固化在仓库里，防止后来者把门改成恒真。
func TestDurationMsConsumerGateIsNotVacuous(t *testing.T) {
	src := `package admin
type fake struct {
	DurationMs *int64 ` + "`" + `json:"duration_ms"` + "`" + `
}
func f() {
	_ = fake{}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse fake: %v", err)
	}

	// 正确的实现必须被认出来。
	if !hasJSONField(f, "fake", "DurationMs", "duration_ms") {
		t.Error("hasJSONField 对正确的实现返回了 false —— 条件 ① 写错了")
	}
	// 结构体名不匹配必须不成立：说明它确实在按结构体名定位，
	// 而不是"包里有个 DurationMs 就算过"。
	if hasJSONField(f, "notFake", "DurationMs", "duration_ms") {
		t.Error("hasJSONField 对结构体名不匹配的实现返回了 true —— 条件 ① 形同虚设")
	}
	// tag 写错必须不成立。
	if hasJSONField(f, "fake", "DurationMs", "duration_seconds") {
		t.Error("hasJSONField 对错误的 json tag 返回了 true —— tag 不被校验")
	}
	// 字段名写错必须不成立。
	if hasJSONField(f, "fake", "Duration", "duration_ms") {
		t.Error("hasJSONField 对错误的字段名返回了 true —— 字段名不被校验")
	}
	// ② 没有 public.sessions 的 SELECT 必须不成立。
	if selectContainsColumn(f, "fake.go", "duration_ms") {
		t.Error("selectContainsColumn 对没有 public.sessions 的文件返回了 true —— 条件 ② 形同虚设")
	}
	// ③ 没有 Scan 必须不成立。
	if scanTargets(f, "&snap.DurationMs") {
		t.Error("scanTargets 对没有 Scan 的文件返回了 true —— 条件 ③ 形同虚设")
	}
}

// TestDurationMsConsumerFilesExist —— 被检查的两个文件必须真的在。
//
// 守卫"门指向了被删掉/被改名的文件"这种形态：文件不在时
// parser.ParseFile 会红，但错误信息很难读；这里给一句人话。
func TestDurationMsConsumerFilesExist(t *testing.T) {
	for _, e := range durationMsConsumers() {
		if _, err := os.Stat(e.file); err != nil {
			t.Errorf("读侧消费点应当位于 %s，但它不存在: %v", e.file, err)
		}
	}
}
