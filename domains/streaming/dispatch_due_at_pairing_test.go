package streaming

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDispatchDueAtIsAlwaysPairedWithRequestClass 钉住 request_class 与 due_at
// 的**配对不变量**：任何写进 request_logs 的 logCtx，要么两者皆空（immediate），
// 要么两者皆非空（scheduled）。不存在只填其一的形态。
//
// 它由 applyRequestClassToLogCtx 一处保证——该函数要么把两者都写成非空常量
// （scheduled + dueAt），要么把 dueAt 清零写 immediate。
//
// ── 为什么这条不变量需要一道门 ────────────────────────────────────────
// telemetry 的 UPDATE 里，due_at 的判据**复用**了 $98
// （`due_at = CASE WHEN $98 IS NULL THEN due_at ELSE $99 END`），即「class
// 为空就不动 due_at」。今天这不构成缺陷——正因这条不变量让
// `$98 IS NULL AND $99 IS NOT NULL` 不可达。
//
// 不变量的**真实危害不是 due_at 被丢弃**，而是请求被记错档：
// 新增协议 handler 若只调 parseDispatchDueAt 而忘了 stamp，logCtx 上
// RequestClass 与 DueAt 都是零值，该行落库时 request_class 取列默认
// 'immediate'——**一个实际等到期才发出的 scheduled 请求，被记成 immediate，
// due_at 也一并为空**。不报错、不告警，只是账单和容量统计整体偏了。
// 判据 ① 抓这个；判据 ② 抓绕过 stamp 直接写 DueAt 的等价形态。
//
// 两条判据，各自对应一种破坏方式：
//  ① **调用点配对**：每个 `X := parseDispatchDueAt(...)` 的 X 必须原样作为第二
//     实参传给同函数里的 applyRequestClassToLogCtx。**按实参身份比，不只比次数**——
//     只比次数抓不到 `a := parse...; apply(ctx, b)` 这种「次数对、对象错」的形态。
//  ② **直写禁令**：形参类型是 *RequestLogContext 的函数，只允许在
//     applyRequestClassToLogCtx 本体内给 `.DueAt` 赋值。判据认的是
//     **接收者标识符**而不是「所有 .DueAt」——`reqLog.DueAt = requestDueAtPtr(logCtx)`
//     是从 logCtx 取值写到另一个结构，那是正确写法，不该被扫进来
//     （第一版把它报成红，判据错了，不是产品错了）。
//
// ── 为什么用「所在函数是不是 owner」而不是「位置在不在 owner 区间内」──
// 位置比较要求 token.Pos 在**同一次解析**上取到；一旦对同一文件 parse 两次，
// 两次的 base offset 不同，位置比较会静默变成跨集合比较——而症状是
// 「门恒红」而不是「门报假红」，很难往解析上想。本版全部改用结构判据，
// 不再比较任何位置，因此不存在这个失效面。
//
// 范围：只扫本包（domains/streaming 顶层）。logCtx 只在这里构造。
//
// NOT TESTED HERE: RequestLogContext 是否还有第四种入口能被构造；子包
// executors 里的 qr.DueAt 属于 dispatch 队列而非 logCtx，不在本门范围。
// 本门管的是「赋值点」与「调用点配对」，这两条正是不变量被打破的全部已知路径。
func TestDispatchDueAtIsAlwaysPairedWithRequestClass(t *testing.T) {
	const (
		parseFn = "parseDispatchDueAt"
		stampFn = "applyRequestClassToLogCtx"
		ctxType = "RequestLogContext"
	)

	entries, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	fset := token.NewFileSet()
	type srcFile struct {
		path string
		af   *ast.File
	}
	var files []srcFile
	for _, path := range entries {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, srcFile{path: path, af: af})
	}

	// ownerParamNames = owner 形参里那个 *RequestLogContext 的名字（今天叫 logCtx）。
	// 判据 ② 用它确认「接收者确实是 logCtx 本体」而不是碰巧同名的局部变量。
	ownerParamNames := map[string]bool{}
	ownerFound := 0
	writes := 0 // 判据② 命中的 logCtx.DueAt 赋值总数（含 owner 体内），用于空集守卫

	// ── 判据 ① 调用点配对 ──────────────────────────────────────────────
	type fnCalls struct {
		file    string
		name    string
		parsed  map[string]token.Pos // X := parseDispatchDueAt(...) → X
		stamped map[string]token.Pos // stamp 的第二实参（标识符形式）→ 位置
		stampN  int
	}
	var calls []*fnCalls

	for _, f := range files {
		for _, d := range f.af.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			// 本函数的 *RequestLogContext 形参名 + owner 登记。
			// 注意：这里**不能**跳过方法——新协议 handler 通常是 ChatHandler 的方法。
			ctxParams := map[string]bool{}
			for _, p := range fn.Type.Params.List {
				id, ok := p.Type.(*ast.StarExpr)
				if !ok {
					continue
				}
				sel, ok := id.X.(*ast.Ident)
				if !ok || sel.Name != ctxType {
					continue
				}
				for _, n := range p.Names {
					ctxParams[n.Name] = true
				}
			}
			isOwner := fn.Recv == nil && fn.Name.Name == stampFn
			if isOwner {
				ownerFound++
				for n := range ctxParams {
					ownerParamNames[n] = true
				}
			}

			// ── 判据 ② 直写禁令 ──────────────────────────────────────
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					sel, ok := lhs.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "DueAt" {
						continue
					}
					base, ok := sel.X.(*ast.Ident)
					if !ok || !ownerParamNames[base.Name] || !ctxParams[base.Name] {
						continue // 典型是 reqLog.DueAt：从 logCtx 取值写到另一个结构，正确
					}
					writes++
					if isOwner {
						continue // owner 体内是这条不变量的**定义处**
					}
					t.Errorf("%s:%d 绕过 %s 直接给 %s.DueAt 赋值。\n"+
						"    配对不变量靠 %s 维持：它要么把 class/dueAt 都写成非空"+
						"（scheduled + dueAt），要么清零 dueAt 写 immediate。\n"+
						"    绕过它会造出 request_class 与 due_at 不一致的 logCtx；"+
						"落库时 request_class 取列默认 'immediate'，"+
						"实际 scheduled 的请求被记成 immediate，且 due_at 为空——"+
						"不报错、不告警。\n"+
						"    要改 due_at 请改 %s 本体，让它同时决定 request_class。",
						f.path, fset.Position(sel.Pos()).Line, stampFn, base.Name, stampFn, stampFn)
				}
				return true
			})

			// 建配对记录。**不能**跳过方法：三个真实调用点都在 ChatHandler 的方法体内，
			// 跳过方法会让判据①在空集合上恒绿（第一版就踩了这个，靠空集守卫抓到的）。
			fc := &fnCalls{
				file:    f.path,
				name:    fn.Name.Name,
				parsed:  map[string]token.Pos{},
				stamped: map[string]token.Pos{},
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
					return true
				}
				lhs, ok := as.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				call, ok := as.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == parseFn {
					fc.parsed[lhs.Name] = lhs.Pos()
				}
				return true
			})
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != stampFn {
					return true
				}
				fc.stampN++
				if len(call.Args) != 2 {
					t.Errorf("%s:%d %s 调用有 %d 个实参，应为 2（logCtx, dueAt）",
						f.path, fset.Position(id.Pos()).Line, stampFn, len(call.Args))
					return true
				}
				if arg, ok := call.Args[1].(*ast.Ident); ok {
					fc.stamped[arg.Name] = arg.Pos()
				}
				return true
			})
			if len(fc.parsed) > 0 || fc.stampN > 0 {
				calls = append(calls, fc)
			}
		}
	}

	if ownerFound != 1 {
		t.Fatalf("找到 %d 个 %s，应恰好 1 个 —— 不变量的定义处已变，这条门需要在 owner 上重写",
			ownerFound, stampFn)
	}

	parseN, stampN := 0, 0
	for _, fc := range calls {
		parseN += len(fc.parsed)
		stampN += fc.stampN
		for name, pos := range fc.parsed {
			if _, ok := fc.stamped[name]; ok {
				continue
			}
			t.Errorf("%s:%d %s 的 %s 结果 %s 没有原样传给 %s"+
				"（本函数 stamp 实参：%v）——logCtx 上 request_class 与 due_at 都会留零值，"+
				"该行落库时 request_class 取列默认 'immediate'，"+
				"实际 scheduled 的请求被记错档。",
				fc.file, fset.Position(pos).Line, fc.name, parseFn, name, stampFn, keysOf(fc.stamped))
		}
	}
	if parseN == 0 || stampN == 0 {
		t.Fatalf("判据①在空集合上恒绿：%s 命中 %d 处 / %s 命中 %d 处。"+
			"若协议确实不再支持 X-Gw-Due-At，请连同这条门一起删或改。",
			parseFn, parseN, stampFn, stampN)
	}
	if writes == 0 {
		t.Fatalf("判据②在空集合上恒绿：没扫到任何 logCtx.DueAt 赋值。"+
			"若 owner 不再写 DueAt，请连同这条门一起改。")
	}
	t.Logf("判据① %s 命中 %d 处 / %s 命中 %d 处；判据② owner(%s) 形参 %v，"+
		"logCtx.DueAt 赋值 %d 处全在 owner 内",
		parseFn, parseN, stampFn, stampN, stampFn, keysOf(ownerParamNames), writes)
}

// keysOf 只为让失败信息可读（map 迭代无序，断言前先排掉顺序噪声）。
func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
