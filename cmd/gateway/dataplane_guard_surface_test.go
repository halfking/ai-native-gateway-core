package main

// 数据面端点清单 × 守卫链挂载点 机器对拍门（LESSONS §704 落地）。
//
// # 这道门为什么存在
//
// §704（2026-11-13 脱敏/恢复域逐条审计轮）：/v1/images 与 /v1/videos
// generations（Phase C 骨架）三个月完全绕过 SmartSaniGuard——prompt 明文出
// 上游、响应占位符无从还原——而当时全部既有量具**全绿**：5 序列化器还原矩阵
// 与反射门只管 IR 参数还原轴，脱敏钉测全部经 ChatHandler 入口构造 ⇒
// **轴内绿 ≠ 端点面绿**（两层验收缺一层就是盲区）。发现路径是人工双腿走查，
// 不是任何门报的 ⇒ 本门把那次走查变成机器对拍：
//
//	数据面端点清单（mux 注册面 AST 扫描，机器推导，非手工台账）
//	× 每条路由的守卫分类（chat 管道 / 环回 chat / media 成对 / 豁免 / 登记缺口）
//	× 各分类的挂载点证据（AST 扫描装配函数体）。
//
// **新端点漏挂即红**：新增数据面路由而处理器不在分类台账 ⇒ 测试红，
// 落库者必须显式选一个分类（接线或登记理由），不允许静默绕过。
//
// # 各分类的证据要求（防「列了但没用」，§682/§695 教训）
//
//   - chatPipeline：处理器变量必须被 main.go 的赋值闭包证明携带 chatHandler
//     （守卫链挂在 ChatHandler 实例上，经 ServeHTTP/手动中间件两条路径生效）。
//     台账声称 chatPipeline 而闭包不认 ⇒ 红。
//   - loopbackChat：服务自己的上游调用必须是环回 chat 面（AST 证据：源文件
//     含 loopbackEndpoint），守卫在环回跳生效。
//   - mediaPaired：main.go 必须调用 installSmartSaniGuard 且传入该处理器；
//     goal_control.go 装配体内必须同时存在成对挂载（mwFn+restoreHook）与
//     撤回（nil,nil）两形态（「成对在场或成对缺席」，半装配比不装配更糟）；
//     domains/streaming 侧必须有 SanitizeGuardWired 成对探针。
//   - chat 面三闸挂载证据全局检查：输入中间件 / 还原拦截器 / 输出闸三个
//     挂载点必须全部在装配函数中在场（缺一 ⇒ 红）。
//   - 台账双向对拍：路由头不在台账 ⇒ 红（漏挂）；台账键没有任何路由引用
//     ⇒ 红（死台账，防台账腐烂）。
//
// # 豁免与登记缺口是两个不同分类，不允许混用
//
//   - exempt：确实没有 LLM 内容往返（模型清单、token 计数、管理/编排面）。
//   - registeredGap：内容会出上游但守卫未挂，属已登记的裁决面（embeddings
//     输入侧、ASR multipart 族等）——必须带登记指针，不许伪装成 exempt。
//
// 路由扫描取所有 mux.Handle/HandleFunc 调用的字符串字面量模式（含条件块
// 内注册：静态扫描按「注册面」计，保守正确）；模式不是字面量 ⇒ 直接红
// （对拍面必须可枚举）。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardClass 是路由守卫分类的闭集。
type guardClass string

const (
	classChatPipeline  guardClass = "chatPipeline"  // 经 ChatHandler 管道（守卫链挂在该实例上）
	classLoopbackChat  guardClass = "loopbackChat"  // 上游调用是环回 chat 面，守卫在环回跳生效
	classMediaPaired   guardClass = "mediaPaired"   // media 成对装配（SetSanitizeGuard 双臂）
	classExempt        guardClass = "exempt"        // 无 LLM 内容往返
	classRegisteredGap guardClass = "registeredGap" // 内容出上游但未挂守卫＝已登记裁决面
)

// routeLedger 是数据面路由的守卫分类台账（键＝处理器头标识符）。
// 双向对拍：路由头必须在本表内；本表键必须被 ≥1 条路由引用。
var routeLedger = map[string]struct {
	class  guardClass
	reason string
}{
	"chatRouteHandler":                {classChatPipeline, "chat/completions+completions 经 ChatHandler 管道"},
	"v2DispatchHandler":               {classChatPipeline, "v2 分发包装（管线默认关；包装的三路由处理器均为 chat 家族）"},
	"messagesRouteHandler":            {classChatPipeline, "messages 经 ChatHandler（手动应用 sanitizeInputMiddleware）"},
	"responsesRouteHandler":           {classChatPipeline, "responses 经 ChatHandler 管道"},
	"chatHandler":                     {classChatPipeline, "handoffs/confirm 直接走 ChatHandler"},
	"geminiHandler":                   {classChatPipeline, "Gemini 合成请求委托 chatHandler.ServeHTTP"},
	"imagesGenerationsHandler":        {classMediaPaired, "§704 P2 收口：成对装配（2026-11-13）"},
	"videosGenerationsHandler":        {classMediaPaired, "§704 P2 收口：成对装配（2026-11-13）"},
	"summarizeHandler":                {classLoopbackChat, "summarize/classify 模型路由完全交给环回 chat 面（summarize.go:86）"},
	"modelsHandler":                   {classExempt, "模型清单/列表面，无内容出上游"},
	"streaming.NewCountTokensHandler": {classExempt, "token 计数面，无上游生成往返"},
	"sessionHandler":                  {classExempt, "会话管理面，无模型内容往返"},
	"goalRunHandler":                  {classExempt, "goal 运行管理面"},
	"hostedtask.NewHandler":           {classExempt, "hosted task 编排面，模型流量走内部链路"},
	"userscriptcapture.NewHandler":    {classExempt, "userscript 捕获存储面"},
	"embeddingsHandler":               {classRegisteredGap, "向量面直连上游：响应无文本无还原需求；输入侧未挂＝登记裁决面（§704 轮对拍结果）"},
	"audioTranscriptionsHandler":      {classRegisteredGap, "ASR multipart transport 不同，prompt 未入洗＝已登记另案（§704 轮）"},
	"audioSpeechHandler":              {classRegisteredGap, "TTS 文本入音频出，输入侧未挂＝登记另案"},
	"audioMCPHandler":                 {classRegisteredGap, "音频 MCP 工具面，登记另案"},
	"audioTransformHandler":           {classRegisteredGap, "音频 refine/analyze 面，登记另案"},
}

// muxRoute 是一条从 main.go 注册面扫出的路由。
type muxRoute struct {
	pattern string
	head    string   // 处理器头：裸标识符 / X.Method 的 X / f(...) 的 f
	delegat []string // 表达式内出现的全部标识符（含包装调用实参）
	line    int
}

// repoRootGuardSurface 返回仓库根（cmd/gateway 的上两级），路径一律相对仓库根，
// 与测试进程 cwd 解耦（同 section_14379 repoRoot 惯用法）。
func repoRootGuardSurface(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位仓库根失败: %v", err)
	}
	return root
}

func parseFileAST(t *testing.T, path string) (*token.FileSet, *ast.File, string) {
	t.Helper()
	full := path
	if !filepath.IsAbs(full) {
		full = filepath.Join(repoRootGuardSurface(t), path)
	}
	src, err := os.ReadFile(full)
	if err != nil {
		t.Fatalf("读源文件失败 %s: %v", full, err)
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, full, src, 0)
	if err != nil {
		t.Fatalf("解析 AST 失败 %s: %v", full, err)
	}
	return fset, file, string(src)
}

// collectIdents 递归收集表达式里出现的全部标识符名。
func collectIdents(expr ast.Expr, out *[]string) {
	switch e := expr.(type) {
	case *ast.Ident:
		*out = append(*out, e.Name)
	case *ast.SelectorExpr:
		collectIdents(e.X, out)
	case *ast.CallExpr:
		collectIdents(e.Fun, out)
		for _, arg := range e.Args {
			collectIdents(arg, out)
		}
	case *ast.CompositeLit:
		collectIdents(e.Type, out)
		for _, elt := range e.Elts {
			collectIdents(elt, out)
		}
	case *ast.FuncLit:
		ast.Inspect(e.Body, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok {
				*out = append(*out, id.Name)
			}
			return true
		})
	case *ast.BinaryExpr:
		collectIdents(e.X, out)
		collectIdents(e.Y, out)
	case *ast.UnaryExpr:
		collectIdents(e.X, out)
	case *ast.ParenExpr:
		collectIdents(e.X, out)
	case *ast.StarExpr:
		collectIdents(e.X, out)
	case *ast.IndexExpr:
		collectIdents(e.X, out)
		collectIdents(e.Index, out)
	case *ast.TypeAssertExpr:
		collectIdents(e.X, out)
	case *ast.BasicLit, *ast.KeyValueExpr:
		if kv, ok := e.(*ast.KeyValueExpr); ok {
			collectIdents(kv.Key, out)
			collectIdents(kv.Value, out)
		}
	}
}

// scanMuxRoutes 扫描 main.go 的 mux.Handle/HandleFunc 注册面，过滤数据面
// （模式前缀 /v1，覆盖 /v1 与 /v1beta；/mock、/api、/healthz 等不在数据面）。
func scanMuxRoutes(t *testing.T, fset *token.FileSet, file *ast.File) []muxRoute {
	t.Helper()
	var routes []muxRoute
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		muxIdent, ok := sel.X.(*ast.Ident)
		if !ok || muxIdent.Name != "mux" {
			return true
		}
		if len(call.Args) < 2 {
			t.Fatalf("mux 注册调用实参不足（%d 个），对拍面必须可枚举：offset %d", len(call.Args), call.Pos())
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			t.Fatalf("mux 注册模式不是字符串字面量，对拍面必须可枚举：offset %d（如需变量模式请先登记例外）", call.Pos())
		}
		pattern := strings.Trim(lit.Value, "\"")
		if !strings.HasPrefix(pattern, "/v1") {
			return true // 非数据面（/api /healthz /metrics /mock /internal /admin …）
		}
		var idents []string
		collectIdents(call.Args[1], &idents)
		head := ""
		switch h := call.Args[1].(type) {
		case *ast.Ident:
			head = h.Name
		case *ast.SelectorExpr:
			if x, ok := h.X.(*ast.Ident); ok {
				head = x.Name
			}
		case *ast.CallExpr:
			switch fn := h.Fun.(type) {
			case *ast.Ident:
				head = fn.Name
			case *ast.SelectorExpr:
				// 限定名（pkg.Func）避免不同包同名构造器碰撞
				if x, ok := fn.X.(*ast.Ident); ok {
					head = x.Name + "." + fn.Sel.Name
				}
			}
		}
		if head == "" {
			t.Fatalf("路由 %s 的处理器表达式形态未识别（head 为空），请扩展 head 提取", pattern)
		}
		line := fset.Position(call.Pos()).Line
		routes = append(routes, muxRoute{pattern: pattern, head: head, delegat: idents, line: line})
		return true
	})
	if len(routes) < 15 {
		t.Fatalf("数据面路由扫描只得到 %d 条（下限 15）——扫描面断裂时必须响红而不是空集合恒绿", len(routes))
	}
	return routes
}

// chatFamilyClosure 从 main.go 赋值语句推导「携带 chatHandler」的变量闭包：
// 种子 chatHandler；RHS 引用家族标识符的变量进入家族（传递闭包）。
func chatFamilyClosure(file *ast.File) map[string]bool {
	family := map[string]bool{"chatHandler": true}
	// 收集所有赋值：lhs 变量 → rhs 标识符集合
	type assign struct {
		lhs []string
		rhs []string
	}
	var assigns []assign
	ast.Inspect(file, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		var lhs, rhs []string
		for _, e := range a.Lhs {
			if id, ok := e.(*ast.Ident); ok {
				lhs = append(lhs, id.Name)
			}
		}
		for _, e := range a.Rhs {
			collectIdents(e, &rhs)
		}
		assigns = append(assigns, assign{lhs: lhs, rhs: rhs})
		return true
	})
	// 不动点迭代（家族成员的 RHS 引用逐层传播）
	for changed := true; changed; {
		changed = false
		for _, a := range assigns {
			for _, name := range a.lhs {
				if family[name] {
					continue
				}
				for _, ref := range a.rhs {
					if family[ref] {
						family[name] = true
						changed = true
						break
					}
				}
			}
		}
	}
	return family
}

// assertChatFaceMountPoints 检查 chat 面三闸挂载证据（goal_control.go 装配体）：
// 输入中间件 / 还原拦截器构建 / 输出闸 / 响应链回装，缺一即红。
// 证据取 **AST 真实调用表达式**而不是字符串 Contains——注释掉的装配行不能算在场
// （「关键词包含钉测会被『列了但没用』骗过」，§682；本门 M1 变异亲历）。
func assertChatFaceMountPoints(t *testing.T) {
	t.Helper()
	_, file, _ := parseFileAST(t, "cmd/gateway/goal_control.go")
	var (
		inputMW      bool // chatHandler.SetSanitizeInputMiddleware(...)
		restoreBuilt bool // buildSanitizeRestoreInterceptor(...) 真实调用
		outputGate   bool // sanitize.NewOutputSensitiveInterceptor(...) 真实调用
		respChain    bool // chatHandler.SetResponseInterceptor(...) 真实调用
		orderHelper  bool // insertSanitizeRestoreBeforeOutputCompliance(...) 真实调用
	)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			switch fn.Name {
			case "buildSanitizeRestoreInterceptor":
				restoreBuilt = true
			case "insertSanitizeRestoreBeforeOutputCompliance":
				orderHelper = true
			}
		case *ast.SelectorExpr:
			x, _ := fn.X.(*ast.Ident)
			switch {
			case x != nil && x.Name == "chatHandler" && fn.Sel.Name == "SetSanitizeInputMiddleware":
				inputMW = true
			case x != nil && x.Name == "chatHandler" && fn.Sel.Name == "SetResponseInterceptor":
				respChain = true
			case fn.Sel.Name == "NewOutputSensitiveInterceptor":
				outputGate = true
			}
		}
		return true
	})
	missing := []string{}
	if !inputMW {
		missing = append(missing, "chatHandler.SetSanitizeInputMiddleware（输入侧中间件）")
	}
	if !restoreBuilt {
		missing = append(missing, "buildSanitizeRestoreInterceptor（还原拦截器构建）")
	}
	if !outputGate {
		missing = append(missing, "sanitize.NewOutputSensitiveInterceptor（输出闸）")
	}
	if !respChain {
		missing = append(missing, "chatHandler.SetResponseInterceptor（响应链回装）")
	}
	if !orderHelper {
		missing = append(missing, "insertSanitizeRestoreBeforeOutputCompliance（还原先于输出合规）")
	}
	if len(missing) > 0 {
		t.Fatalf("chat 面守卫链挂载点缺失（AST 调用级证据，注释不算在场）：%v——SmartSaniGuard 三闸（输入/还原/输出闸）必须成套在场（§704）", missing)
	}
}

// assertMediaPairedWiring 检查 media 成对装配证据（AST 调用级）：
// main.go 真实调用 installSmartSaniGuard 且传入两个 media 处理器 + goal_control.go
// 装配体同时存在成对挂载（双非 nil 实参）与撤回（nil,nil）两形态 + domains/streaming
// 两个处理器都有 SanitizeGuardWired 成对探针声明。
func assertMediaPairedWiring(t *testing.T) {
	t.Helper()
	mainFset, mainFile, _ := parseFileAST(t, "cmd/gateway/main.go")
	// 每个 installSmartSaniGuard 调用点（主路径 + lite 回退路径等）都必须把
	// chatHandler 与两个 media 处理器全部传入——任何一个分支漏传＝该部署
	// 模式静默失守（§704：绕过是按部署形态发生的，不是按代码存在的）。
	badCalls := []string{}
	callCount := 0
	ast.Inspect(mainFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "installSmartSaniGuard" {
			return true
		}
		callCount++
		args := map[string]bool{}
		for _, arg := range call.Args {
			if id, ok := arg.(*ast.Ident); ok {
				args[id.Name] = true
			}
		}
		if !(args["chatHandler"] && args["imagesGenerationsHandler"] && args["videosGenerationsHandler"]) {
			badCalls = append(badCalls, fmt.Sprintf("main.go:%d", mainFset.Position(call.Pos()).Line))
		}
		return true
	})
	if callCount == 0 {
		t.Fatalf("main.go 无任何 installSmartSaniGuard 真实调用——守卫链装配入口整体断线（§704）")
	}
	if len(badCalls) > 0 {
		t.Fatalf("installSmartSaniGuard 调用点 %v 未传全 (chatHandler, imagesGenerationsHandler, videosGenerationsHandler)——漏传分支的部署形态将静默绕过守卫（§704：半装配比不装配更糟）", badCalls)
	}
	_, goalFile, _ := parseFileAST(t, "cmd/gateway/goal_control.go")
	type guardCall struct{ recv, arg0, arg1 string }
	var calls []guardCall
	ast.Inspect(goalFile, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "SetSanitizeGuard" {
			return true
		}
		recv, _ := sel.X.(*ast.Ident)
		gc := guardCall{recv: recv.Name}
		if len(call.Args) == 2 {
			if id, ok := call.Args[0].(*ast.Ident); ok {
				gc.arg0 = id.Name
			}
			if id, ok := call.Args[1].(*ast.Ident); ok {
				gc.arg1 = id.Name
			}
		}
		calls = append(calls, gc)
		return true
	})
	for _, handler := range []string{"imagesHandler", "videosHandler"} {
		paired, withdrawn := false, false
		for _, gc := range calls {
			if gc.recv != handler {
				continue
			}
			if gc.arg0 != "nil" && gc.arg1 != "nil" {
				paired = true
			}
			if gc.arg0 == "nil" && gc.arg1 == "nil" {
				withdrawn = true
			}
		}
		if !paired {
			t.Fatalf("media 成对挂载缺臂：%s 必须有 SetSanitizeGuard(mwFn, restoreHook) 双非 nil 真实调用——「输入脱敏+输出还原」成对装配（§704 发现②）", handler)
		}
		if !withdrawn {
			t.Fatalf("media 撤回分支缺臂：%s 必须有 SetSanitizeGuard(nil, nil) 真实调用——还原拦截器构建失败时撤回输入中间件，保持「成对在场或成对缺席」（§704 发现②）", handler)
		}
	}
	// R-7③ 收口门（2026-11-14）：restore 失败分支的撤回纪律必须覆盖 chat 面
	// ——goal_control.go 装配体内必须存在 chatHandler.SetSanitizeInputMiddleware(nil)
	// 真实调用（此前只撤 media 两面，chat 输入中间件单臂在挂 = 半装配：
	// 占位符照发而还原链已断，r1043 会话字典污染族的同启动形态）。
	{
		_, goalFile, _ := parseFileAST(t, "cmd/gateway/goal_control.go")
		found := false
		ast.Inspect(goalFile, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SetSanitizeInputMiddleware" || len(call.Args) != 1 {
				return true
			}
			if id, ok := call.Args[0].(*ast.Ident); ok && id.Name == "nil" {
				recv, _ := sel.X.(*ast.Ident)
				if recv != nil && recv.Name == "chatHandler" {
					found = true
				}
			}
			return true
		})
		if !found {
			t.Fatalf("chat 面撤回臂缺失：goal_control.go 还原拦截器构建失败分支必须真实调用 chatHandler.SetSanitizeInputMiddleware(nil)——成对在场或成对缺席对 chat 面同样适用（R-7①②③ 同族：修复面只盖 media 不盖 chat）")
		}
	}
	for _, probe := range []struct {
		file, recv string
	}{
		{"domains/streaming/images_generations.go", "ImagesGenerationsHandler"},
		{"domains/streaming/videos_generations.go", "VideosGenerationsHandler"},
	} {
		_, probeFile, _ := parseFileAST(t, probe.file)
		found := false
		ast.Inspect(probeFile, func(n ast.Node) bool {
			fd, ok := n.(*ast.FuncDecl)
			if !ok || fd.Name.Name != "SanitizeGuardWired" {
				return true
			}
			if fd.Recv == nil || len(fd.Recv.List) == 0 {
				return true
			}
			star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				return true
			}
			if id, ok := star.X.(*ast.Ident); ok && id.Name == probe.recv {
				found = true
			}
			return true
		})
		if !found {
			t.Fatalf("%s 缺 (*%s).SanitizeGuardWired 成对探针声明（只在校验两半都非 nil 时返回非 nil）", probe.file, probe.recv)
		}
	}
}

// constructorAliases 从 main.go 赋值推导「局部变量 → 构造器限定名」映射：
// `captureHandler := userscriptcapture.NewHandler(...)` ⇒ captureHandler →
// "userscriptcapture.NewHandler"。台账允许两种键：处理器变量本身，或其构造器。
func constructorAliases(file *ast.File) map[string]string {
	aliases := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if !ok || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			return true
		}
		lhsID, ok := a.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := a.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			aliases[lhsID.Name] = fn.Name
		case *ast.SelectorExpr:
			if x, ok := fn.X.(*ast.Ident); ok {
				aliases[lhsID.Name] = x.Name + "." + fn.Sel.Name
			}
		}
		return true
	})
	return aliases
}

func TestDataplaneGuardSurfaceParity(t *testing.T) {
	fset, file, _ := parseFileAST(t, "cmd/gateway/main.go")
	routes := scanMuxRoutes(t, fset, file)
	family := chatFamilyClosure(file)
	aliases := constructorAliases(file)

	// ── 第一向：每条数据面路由必须有台账分类，且分类证据成立 ──
	// 台账键解析两级：处理器头本身 → 头的构造器限定名。没有第三级自动归类：
	// 包装函数若携带 chat 家族成员必须在台账显式登记（如 v2DispatchHandler），
	// 不允许「实参里有 chatHandler 就算守卫在场」的推断式绿（§682 同族）。
	ledgerSeen := map[string]bool{}
	for _, route := range routes {
		entry, ok := routeLedger[route.head]
		head := route.head
		if !ok {
			if ctor, isAlias := aliases[route.head]; isAlias {
				if e2, ok2 := routeLedger[ctor]; ok2 {
					entry, ok = e2, true
				}
			}
		}
		if !ok {
			t.Fatalf("路由 %s（main.go:%d，head=%s）不在守卫分类台账——新数据面端点必须显式选类："+
				"chatPipeline（经 ChatHandler 管道）/ mediaPaired（成对 SetSanitizeGuard）/ "+
				"exempt（无内容往返，附理由）/ registeredGap（登记裁决面，附指针）。静默绕过即 §704 同族缺陷",
				route.pattern, route.line, route.head)
		}
		ledgerSeen[head] = true
		switch entry.class {
		case classChatPipeline:
			inFamily := family[head]
			if !inFamily {
				for _, d := range route.delegat {
					if family[d] {
						inFamily = true
						break
					}
				}
			}
			if !inFamily {
				t.Fatalf("路由 %s 台账声称 chatPipeline，但 head=%s 的表达式与 main.go 赋值闭包均未证明携带 chatHandler（守卫链挂在 ChatHandler 实例上）——分类造假或变量改线，请核实挂载路径", route.pattern, head)
			}
		case classMediaPaired:
			// 证据在 assertMediaPairedWiring 全局检查（装配体级），此处确认台账一致性。
		case classLoopbackChat:
			_, _, sumSrc := parseFileAST(t, "domains/streaming/summarize.go")
			if !strings.Contains(sumSrc, "loopbackEndpoint") {
				t.Fatalf("路由 %s 台账声称 loopbackChat，但 summarize.go 无 loopbackEndpoint 环回证据——若改为直连上游则守卫失效，必须改类或补挂", route.pattern)
			}
		case classExempt, classRegisteredGap:
			if entry.reason == "" {
				t.Fatalf("路由 %s 分类 %s 缺理由字段——exempt 必须说明为何无内容往返，registeredGap 必须带登记指针", route.pattern, entry.class)
			}
		}
	}

	// ── 第二向：台账键必须被 ≥1 条路由引用（死台账 ⇒ 红，防台账腐烂）──
	for key := range routeLedger {
		if ledgerSeen[key] {
			continue
		}
		// 键若以构造器形态登记（如 userscriptcapture.NewHandler），经由别名被引用即算活。
		alive := false
		for head := range ledgerSeen {
			if aliases[head] == key {
				alive = true
				break
			}
		}
		if !alive {
			t.Fatalf("台账键 %s 没有任何数据面路由引用——处理器已改名/删除或注册面漂移，台账必须随注册面对拍更新（死台账=「列了但没用」同族）", key)
		}
	}

	assertChatFaceMountPoints(t)
	assertMediaPairedWiring(t)
}
