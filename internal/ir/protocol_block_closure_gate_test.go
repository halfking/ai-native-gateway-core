// protocol_block_closure_gate_test.go —— Anthropic 合法内容块闭集的**三方对账**
// （★ 2026-11-11 审计 R-3 的钉测）
//
// ── 为什么要有这道门 ────────────────────────────────────────────────
//
// Anthropic 请求侧合法内容块的闭集在仓里有**三份**表述：
//
//	① internal/protocolformat/validate.go 的 anthropicBlockTypes（map）
//	② internal/ir/serialize_anthropic.go 的 anthropicLegalBlockType（switch）
//	③ internal/ir/responses_reasoning_item_anthropic_test.go 里那段
//	   **注释**里手写的枚举
//
// ★ R-3 的原形态：★ ①② 两处生产闭集**恰好一致**（★ 各 12 项、★ 差集为空）
// ★ ★ 而 ③ 那段注释里有 **guard_content**（★ **不属于任何一处实现**）
// ★ ★ 且**漏了** search_result / container_upload（★ 两处实现都有）★★
// ★★ ⇒ ★★ 那段注释与**两处实现都不一致** ⇒ ★★ 它是**假的** ★★。
//
// ★★ 而「三份要手工同步」这件事本身就是个**没有牙的约定**：★★
// ★★ 三份里只有 ①② 是代码、③ 是散文 ⇒ ★★ 没有任何机器会再核一遍
// ★★ ⇒ ★★ 注释必然漂移（★ 这就是 R-3 的根因，★ 而不是「写错了」）。
//
// ── 这道门断言什么 ──────────────────────────────────────────────────
//
//	门 R3-a（两份生产闭集必须逐项相等）：★ ① 与 ② 的集合必须**完全相等**
//	   （★ 双向对账：★ 缺一个、多一个都红）。
//	门 R3-b（注释枚举必须逐项等于实现）：★ ③ 里那段注释解析出来的枚举
//	   必须**恰好**等于 ①② 的并集（★ 注释里多写一个少写一个都红）。
//	门 R3-c（guard_content 不得出现）：★ guard_content 只存在于旧注释里，
//	   **两处实现都没有** ⇒ ★ 它不得出现在任何一份闭集表述中。
//
// ★ 为什么用 **AST 直读实现源码**而不是加一个导出的读取口：★
// ★ 加读取口就得**再手抄一份**（★ 那就是**第三份真相源**★ ⇒ ★★ 正是本门
// ★★ 要消灭的东西）★★ ⇒ ★★ AST 直读**实现本身**才没有第二份清单 ★★。
//
// ★ 诚实的覆盖边界（★ 本门**管不到**什么）───────────────────────────
//
//	① AST 提取认的是「map 字面量的键」与「switch 的 case 字符串」这两种形态；
//	   ★ 若实现改成函数调用/正则生成闭集，★ 本门会**提取不到** ⇒ ⇒门红
//	   （★ fail-closed：★ 提取不到 ≠ 闭集为空）。
//	② ③ 的解析靠注释的**固定标记句**（★ "content-block closure:" ⇒ …）。
//	   ★ 注释被重写成别的形状 ⇒ 门红（★ fail-closed）。
package ir

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// irDir 返回 internal/ir 目录（★ 从本测试文件的位置反推 ★ 不依赖工作目录）。
func irDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) 失败")
	}
	return filepath.Dir(thisFile)
}

// extractMapStringKeys 用 AST 从某个 map 字面量声明里取出全部字符串键。
//
// ★ 认的形态：`var <name> = map[string]bool{ "k": true, ... }`
func extractMapStringKeys(t *testing.T, src, varName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", src, err)
	}
	var out []string
	found := false
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != varName {
				continue
			}
			found = true
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatalf("%s 里 %s 的初值不是复合字面量（★ 提取不到 ⇒ 必须判失败）", src, varName)
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					t.Fatalf("%s 里 %s 的元素不是键值对（★ 提取不到 ⇒ 必须判失败）", src, varName)
				}
				bl, ok := kv.Key.(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					t.Fatalf("%s 里 %s 的键不是字符串字面量（★ 提取不到 ⇒ 必须判失败）", src, varName)
				}
				out = append(out, strings.Trim(bl.Value, `"`))
			}
		}
	}
	if !found {
		t.Fatalf("在 %s 里找不到 var %s", src, varName)
	}
	if len(out) == 0 {
		t.Fatalf("%s 里 %s 提取到 0 个键（★ 提取器坏了 ⇒ 门会退化成「什么都不查」）", src, varName)
	}
	sort.Strings(out)
	return out
}

// extractSwitchCaseStrings 用 AST 从某个函数的 switch 语句里取出全部 case 字符串。
//
// ★ 认的形态：`switch t { case "a", "b": ... }`
func extractSwitchCaseStrings(t *testing.T, src, fnName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败：%v", src, err)
	}
	var out []string
	found := false
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != fnName {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			for _, stmt := range sw.Body.List {
				cc, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range cc.List {
					bl, ok := expr.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						t.Fatalf("%s 的 %s 里 case 表达式不是字符串字面量（★ 提取不到 ⇒ 必须判失败）", src, fnName)
					}
					out = append(out, strings.Trim(bl.Value, `"`))
				}
			}
			return true
		})
	}
	if !found {
		t.Fatalf("在 %s 里找不到函数 %s", src, fnName)
	}
	if len(out) == 0 {
		t.Fatalf("%s 的 %s 里提取到 0 个 case（★ 提取器坏了 ⇒ 门会退化成「什么都不查」）", src, fnName)
	}
	sort.Strings(out)
	return out
}

// extractClosureComment 解析测试注释里那段闭集枚举。
//
// ★ 认的形状：★ 含 "content-block closure:" 的注释块 ★ 从该标记之后
// ★ 到 `."reasoning"` 之前的那一段 ⇒ 按逗号切开。
func extractClosureComment(t *testing.T, src string) []string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", src, err)
	}
	const marker = "content-block closure:"
	const terminator = `"reasoning"`
	lines := strings.Split(string(raw), "\n")

	var joined []string
	capturing := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !capturing {
			if strings.HasPrefix(trimmed, "//") && strings.Contains(trimmed, marker) {
				capturing = true
				joined = append(joined, strings.TrimPrefix(trimmed, "//"))
			}
			continue
		}
		if !strings.HasPrefix(trimmed, "//") {
			break // ★ 注释块结束。
		}
		joined = append(joined, strings.TrimPrefix(trimmed, "//"))
	}
	if len(joined) == 0 {
		t.Fatalf("在 %s 里找不到含 %q 的注释块（★ 注释被改写了 ⇒ 必须判失败）", src, marker)
	}
	text := strings.Join(joined, " ")
	idx := strings.Index(text, marker)
	if idx < 0 {
		t.Fatalf("注释块里找不到标记 %q", marker)
	}
	text = text[idx+len(marker):]
	end := strings.Index(text, terminator)
	if end < 0 {
		t.Fatalf("注释块里找不到结束标记 %q（★ 注释被改写了 ⇒ 必须判失败）", terminator)
	}
	text = text[:end]

	var out []string
	for _, part := range strings.Split(text, ",") {
		item := strings.TrimSpace(part)
		// ★ 最后一项后面跟着句号（★ "citations. "reasoning"…"）⇒ 剥掉。
		item = strings.TrimRight(item, ".")
		if item == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		t.Fatalf("从 %s 的注释里解析出 0 个条目", src)
	}
	sort.Strings(out)
	return out
}

// TestProtocolBlockClosureThreeWayReconciliation 是 R-3 的**三方对账门**：
// 两份生产闭集 ①② 与那份注释枚举 ③ 必须**逐项相等**。
//
// ★ 变异臂：
//
//	① 给 protocolformat 的 map 加一个键 ⇒ 门红（★ 两处闭集不再相等）
//	② 给 ir 的 switch 加一个 case ⇒ 门红
//	③ 往注释里加一个条目 ⇒ 门红（★ 注释与实现不一致）
//	④ 从注释里删一个条目 ⇒ 门红
//	⑤ 把注释改回含 guard_content ⇒ 门红
func TestProtocolBlockClosureThreeWayReconciliation(t *testing.T) {
	dir := irDir(t)
	protocolSrc := filepath.Join(dir, "..", "protocolformat", "validate.go")
	irSrc := filepath.Join(dir, "serialize_anthropic.go")
	commentSrc := filepath.Join(dir, "responses_reasoning_item_anthropic_test.go")

	implA := extractMapStringKeys(t, protocolSrc, "anthropicBlockTypes")
	implB := extractSwitchCaseStrings(t, irSrc, "anthropicLegalBlockType")
	comment := extractClosureComment(t, commentSrc)

	// 门 R3-a：两份生产闭集逐项相等（★ 双向对账）。
	assertSameSet(t, "protocolformat.anthropicBlockTypes", implA, "ir.anthropicLegalBlockType", implB)

	// 门 R3-b：注释枚举逐项等于实现闭集。
	assertSameSet(t, "注释里的闭集枚举", comment, "两份生产闭集的并集", unionOf(implA, implB))

	// 门 R3-c：guard_content 不得出现在任何一份表述里（★ 它只存在于旧注释）。
	for _, group := range []struct {
		name  string
		items []string
	}{
		{"protocolformat.anthropicBlockTypes", implA},
		{"ir.anthropicLegalBlockType", implB},
		{"注释里的闭集枚举", comment},
	} {
		for _, item := range group.items {
			if item == "guard_content" {
				t.Errorf("%s 里出现了 guard_content（★ 它不属于任何一处实现 ⇒ 是旧注释留下的幽灵）", group.name)
			}
		}
	}
}

func unionOf(a, b []string) []string {
	seen := map[string]bool{}
	for _, x := range a {
		seen[x] = true
	}
	for _, x := range b {
		seen[x] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assertSameSet 双向对账两个集合（★ 缺一个、多一个都报出来 ★便于定位）。
func assertSameSet(t *testing.T, nameA string, a []string, nameB string, b []string) {
	t.Helper()
	inA := map[string]bool{}
	for _, x := range a {
		inA[x] = true
	}
	inB := map[string]bool{}
	for _, x := range b {
		inB[x] = true
	}
	for _, x := range a {
		if !inB[x] {
			t.Errorf("%s 有 %q 而 %s 没有（★ 少的一方）", nameA, x, nameB)
		}
	}
	for _, x := range b {
		if !inA[x] {
			t.Errorf("%s 有 %q 而 %s 没有（★ 多的一方）", nameB, x, nameA)
		}
	}
	if len(a) != len(b) {
		t.Errorf("%s 有 %d 项、%s 有 %d 项 ⇒ 集合大小不等", nameA, len(a), nameB, len(b))
	}
}
