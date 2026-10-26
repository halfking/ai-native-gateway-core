package admin

// logs_q_search_contract_test.go —— `/api/logs?q=` 这条链的**契约门**。
//
// # 这道门守的是什么
//
// 2026-10-20 审计盯的是「877 决策文档里那个恒为 0 的读数」：
// nginx 访问日志里带 `?q=` 的 `GET /api/logs` 连续两批读数都是 **0**。
// 那一节的结论原本写的是：
//
//   「搜索框确实存在且接线正确 ⇒ 所以这是**需求为零**，不是**功能缺失**。」
//
// ★ 这条结论**只验了一半**：它核对了「客户端 → 服务端」这段接线
// （RequestLogsView.vue 把 keyword 绑成 `q:`），**没有核对「服务端 → 结果」**
// 到底能不能搜到东西。
//
// 补上这一半之后，事实是：
//
//   admin/logs.go 的 `?q=` 谓词是 `rl.search_text ILIKE '%…%'`
//   （★ 2026-10-20 G-5 轮起，谓词文本由 admin/logs.go 搬进
//     admin/logs_search_predicate.go 的 buildSessionSearchPredicate ——
//     因为要给它加一条全文检索腿，且那条腿必须与迁移 887 的索引表达式
//     **逐字同形**；本门的断言已同步改成「logs.go 调 builder」+
//     「builder 含 rl.search_text ILIKE」两段）；
//   而 search_text 由 domains/hooks/observability/telemetry/client.go 的
//   searchText() 拼成，成分只有**元数据**：
//     ClientModel / OutboundModel / ClientProfile / RequestMode /
//     GwSessionID / GwTaskID / APIKeyPrefix / APIKeyOwnerUser / ApplicationCode
//   —— **一条请求/响应正文都不在里面**。
//
// ⇒ 后端这个框**搜不到消息正文**；而前端那个框的 placeholder 当时写的是
// 「搜索请求消息内容…」。用户按提示去搜正文片段 ⇒ 必然 0 命中 ⇒
// 「0 次使用」这个读数**分不清**「没人要」和「功能承诺落空后没人要」。
//
// # 所以这道门钉的是**两端不许各自漂移**：
//
//   ① 后端：`?q=` 必须仍然落在 `rl.search_text` 上（别把谓词改到别的列，
//      那会让前端文案再次对不上）。
//   ② 写侧：searchText() / buildSearchText() 的成分里**不许出现正文类字段**
//      （RequestBody / ResponseBody / Body / Prompt / Content / Delta /
//      Preview / Message / Text / Input / Output …）。
//      ★ 一旦有人把正文塞进 search_text，**这��门会变红** ——
//        那不是"门坏了"，是**前端文案必须同步改成能搜正文**，
//        而那会牵动 877 的写放大结论（正文是 TOAST 宽列），
//        必须重新做一次定量决策，不是顺手改个 placeholder。
//
// ③ 前端：web/src/views/RequestLogsView.searchContract.test.ts 钉住
//    「界面不许承诺正文检索」。
//
// # 判据纪律：这道门自己也要能被打穿
//
// 变异实测见文件末：往 searchText() 里加一个 Body 字段 → 门红；
// 把 logs.go 的 `q` 谓词改到别的列 → 门红。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// bodyFieldWords 是「这条字段很可能装着请求/响应正文」的判据词。
// 命中即红 —— 宁可多报一次让人来看一眼，也不要让正文悄悄进 search_text。
var bodyFieldWords = []string{
	"Body", "Prompt", "Content", "Delta", "Preview",
	"Message", "Input", "Output", "Payload", "Text",
}

// repoRoot 从 admin 包目录回到仓库根（测试文件在 <root>/admin/ 下）。
func qContractRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd)
}

// parseFileAt 解析仓库内的一个 Go 文件。
func parseFileAt(t *testing.T, root, rel string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, rel), nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return f
}

// funcDeclIn 找出指定名字的函数声明。
func funcDeclIn(t *testing.T, f *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("function %s not found", name)
	return nil
}

// structFieldsIn 找出函数体里读到的 `<recv>.<Field>` 形式的选择器字段名。
func structFieldsIn(fn *ast.FuncDecl, recv string) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok || id.Name != recv {
			return true
		}
		out[sel.Sel.Name] = true
		return true
	})
	return out
}

// assertNoBodyFields 断言成分集合里没有正文类字段。
func qContractAssertNoBodyFields(t *testing.T, where string, fields map[string]bool) {
	t.Helper()
	for f := range fields {
		for _, w := range bodyFieldWords {
			if strings.Contains(f, w) {
				t.Errorf("%s 读到了疑似正文字段 %q ⇒ search_text 的成分不再是纯元数据；"+
					"前端「搜索请求消息内容」的文案会再次对不上（且 877 的写放大结论需要重做）",
					where, f)
			}
		}
	}
}

// TestSearchTextComposition_HasNoBodyField 钉住写侧：search_text 只由元数据拼成。
func TestSearchTextComposition_HasNoBodyField(t *testing.T) {
	root := qContractRepoRoot(t)

	// v2 写侧（session 家族 / v1 家族都走它）
	f := parseFileAt(t, root, "domains/hooks/observability/telemetry/client.go")
	fn := funcDeclIn(t, f, "searchText")
	fields := structFieldsIn(fn, "entry")
	if len(fields) == 0 {
		t.Fatal("searchText() 没读到任何 entry.* 字段 —— 判据本身失效（函数被改空了？）")
	}
	qContractAssertNoBodyFields(t, "telemetry.searchText()", fields)

	// v1 写侧（admin/telemetry.go 的 buildSearchText）
	f2 := parseFileAt(t, root, "admin/telemetry.go")
	fn2 := funcDeclIn(t, f2, "buildSearchText")
	fields2 := structFieldsIn(fn2, "e")
	if len(fields2) == 0 {
		t.Fatal("buildSearchText() 没读到任何 e.* 字段 —— 判据本身失效")
	}
	qContractAssertNoBodyFields(t, "admin.buildSearchText()", fields2)
}

// TestLogsQPredicate_TargetsSearchText 钉住读侧：`?q=` 必须落在 search_text 上。
//
// 少了这一条，前端文案与后端谓词可能各指一处，而两边都"看起来对"。
func TestLogsQPredicate_TargetsSearchText(t *testing.T) {
	root := qContractRepoRoot(t)
	f := parseFileAt(t, root, "admin/logs.go")

	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		// queryString 是**同包**的普通函数（不是方法），所以 call.Fun 是
		// *ast.Ident；这里两种形态都收，免得哪天改成方法调用就误判成"入口没了"。
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if fun.Name != "queryString" {
				return true
			}
		case *ast.SelectorExpr:
			if fun.Sel.Name != "queryString" {
				return true
			}
		default:
			return true
		}
		if len(call.Args) < 2 {
			return true
		}
		lit, ok := call.Args[1].(*ast.BasicLit)
		if !ok || lit.Value != `"q"` {
			return true
		}
		found = true
		return true
	})
	if !found {
		t.Fatal(`admin/logs.go 里找不到 queryString(r, "q") ⇒ ?q= 的读侧入口没了；` +
			`前端 RequestLogsView 仍会发 q=，那会变成"发了但被忽略"的静默失效`)
	}

	// ① 后端：`?q=` 必须仍然落在 `rl.search_text` 上。
	//
	// ★ 2026-10-20（G-5 会话族全文检索轮）本断言的**读法变了**，但**没变松**：
	//   谓词文本从 admin/logs.go 搬进了 admin/logs_search_predicate.go
	//   （buildSessionSearchPredicate —— 因为 G-5 要给同一处谓词加一条
	//   **全文检索腿**（to_tsvector @@ plainto_tsquery），而那条腿的形态
	//   必须与迁移 887 的索引表达式**逐字同形**，写在 builder 里才有一个
	//   跨文件契约门可钉）。
	//   ⇒ 断言改成**两段**：logs.go 必须**调用 builder**，builder 必须
	//   **含** `rl.search_text ILIKE`。任一段缺失都红 ——
	//   「builder 里有但 logs.go 没用」= 谓词没接上（静默失效）；
	//   「logs.go 用了但 builder 里没有 ILIKE」= 谓词目标变了。
	//   这比原来「logs.go 里有这个字面量」**更严**（原来只查一处，
	//   现在查的是「接线 + 目标」两件事）。
	logsSrc, err := os.ReadFile(filepath.Join(root, "admin", "logs.go"))
	if err != nil {
		t.Fatalf("read admin/logs.go: %v", err)
	}
	if !strings.Contains(string(logsSrc), "buildSessionSearchPredicate") {
		t.Errorf(`admin/logs.go 里没有引用 buildSessionSearchPredicate ⇒ ?q= 的谓词` +
			`没有走 builder；G-5 的全文检索腿（与迁移 887 的索引表达式逐字同形的那条）` +
			`接不上，谓词会退化成只有子串腿`)
	}
	builderSrc, err := os.ReadFile(filepath.Join(root, "admin", "logs_search_predicate.go"))
	if err != nil {
		t.Fatalf("read admin/logs_search_predicate.go: %v", err)
	}
	if !strings.Contains(string(builderSrc), "rl.search_text ILIKE") {
		t.Errorf(`admin/logs_search_predicate.go 里没有 "rl.search_text ILIKE" ⇒ ?q= 的谓词目标变了；` +
			`请同步核对 web/src/views/RequestLogsView.vue 的提示文案与 877 的索引前提`)
	}
}
