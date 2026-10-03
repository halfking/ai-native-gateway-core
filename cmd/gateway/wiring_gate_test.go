package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAdminHandlersAreWired 锁住一类反复出现的部署缺陷：
// handler 实现了、RegisterRoutes 也写了，唯独没接到 main mux 上，
// 于是对应页面的 API 全部 404、页面永远空，而单测还是绿的
//（测试自己 new handler 直接打，覆盖不到「路由注册没有」这一层）。
//
// 本仓已中过两次：
//   - prompt-injection：main.go:3104 注释里写明「修复前端调用 404 的 bug」
//   - output-compliance：2026-10-03，/admin/output-compliance 页面全 404
//
// 判据：cmd/gateway/main.go 里每出现一次 `admin.New<Name>Handler(`（或
// 任何 `New<Name>Handler(`）赋给局部变量，该变量名必须在同文件的某处
// 出现在 `.RegisterRoutes(` 调用里。
//
// 为什么要静态扫而不是起个真 server 打请求：起真 server 需要 DB/配置，
// 变成一个跑不动的门；静态扫虽然认不出运行时条件分支，但「构造函数调用」
// 和「RegisterRoutes 调用」都是字面量级的，足够把上面两次都挡住。
func TestAdminHandlersAreWired(t *testing.T) {
	mainPath := filepath.Join("main.go")
	src, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("读 %s 失败: %v", mainPath, err)
	}
	code := string(src)

	// 1) 找出所有被赋值的 New<Name>Handler(...) 变量。
	//    只认赋值形式 `x := admin.NewFooHandler(` / `x = admin.NewFooHandler(`，
	//    忽略作为实参内联传入的（那些不需要单独的 RegisterRoutes 变量名）。
	assignRe := regexp.MustCompile(`(?m)^\s*([A-Za-z_][A-Za-z0-9_]*)\s*:?=\s*(?:admin\.)?New[A-Za-z0-9_]*Handler\(`)
	declared := map[string]string{} // 变量名 → 构造器名
	for _, m := range assignRe.FindAllStringSubmatch(code, -1) {
		declared[m[1]] = m[0]
	}
	if len(declared) == 0 {
		t.Fatal("判据失效：一个 New*Handler 变量都没匹配到。" +
			"这通常意味着 main.go 的写法变了，门会静默放过所有未接线的 handler。")
	}

	// 2) 收集所有「已接线」证据。本仓一共有四种合法接线写法，缺一不可——
	//    第一版判据只认 RegisterForms，结果把 7 个正常 handler 报成未接线：
	//
	//      a) x.RegisterRoutes(mux)              直接注册
	//      b) adminHandler.SetXHandler(x)        交给父 Handler 代注册
	//      c) mux.HandleFunc(path, x.Method)     逐路由挂载
	//      d) NewY(x) / newMount(x) / .Wrap(x)   作为实参传入组装点
	//
	//    只认其中一种的门不是门，是噪音。
	//
	//    2026-10-03 R38：两处修正。
	//    ① 原第④条证据是 `\b(ident)\s*\)`——任何「标识符后跟右括号」都算接线，
	//    而 Go 编译器本身强制局部变量必须被使用，于是该模式几乎恒真，门对
	//    「构造了、传给了不挂路由的组装函数」这类真缺陷形态给的是假通过。
	//    ② 原第②条 `x.SetXHandler(` 抓的是**接收者**（adminHandler），不是被
	//    接线的**实参**（incidentHandler）——一直靠第①条的泛匹配兜底才没漏报。
	//    现在：Set*Handler 抓实参；第④类收敛为显式组装汇点的实参
	//    （构造器 New*/new*、Wrap/Handle/HandleFunc/Use/Mount），变量被
	//    log.Println 之类「使用」不再算接线。
	used := map[string]bool{}
	for _, pat := range []string{
		`([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*RegisterRoutes\s*\(`,
		`([A-Za-z_][A-Za-z0-9_]*)\s*\.\s*[A-Z][A-Za-z0-9_]*\s*\)`,
		`\.Set[A-Za-z0-9_]*Handler\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)`,
	} {
		re := regexp.MustCompile(pat)
		for _, m := range re.FindAllStringSubmatch(code, -1) {
			used[m[1]] = true
		}
	}
	for _, sink := range []string{
		`[Nn]ew[A-Za-z0-9_]*\s*\(`,
		`\.\s*(?:Wrap|Handle|HandleFunc|Use|Mount)\s*\(`,
	} {
		for _, arg := range ctorCallArgs(code, sink) {
			used[arg] = true
		}
	}
	if len(used) < 10 {
		t.Fatalf("判据失效：只识别出 %d 个「已接线」变量，远低于预期。门会静默放过所有未接线的 handler。", len(used))
	}

	var unwired []string
	for name, ctor := range declared {
		if used[name] {
			continue
		}
		unwired = append(unwired, name+"  (构造于: "+strings.TrimSpace(ctor)+")")
	}
	if len(unwired) > 0 {
		t.Errorf("以下 handler 构造了但没有任何接线痕迹（RegisterRoutes / Set*Handler / 挂到 mux / 传入组合根）—— 对应 API 会全部 404：\n  - %s",
			strings.Join(unwired, "\n  - "))
	}
}

// ctorCallArgs 找出 code 里所有形如 callRe（如 `NewFoo(`）的调用点，取其
// 平衡括号内的实参表，按逗号切分（只切深度 0 的逗号），返回每个 trim 后的
// 实参字面值。用于「变量作为构造函数实参传入组合根」的接线证据——必须
// 精确到实参位置，不能用 `\bident\s*\)` 这类泛匹配（几乎恒真，见上）。
func ctorCallArgs(code, callRe string) []string {
	var args []string
	re := regexp.MustCompile(callRe)
	for _, loc := range re.FindAllStringIndex(code, -1) {
		// callRe 以 `\(` 结尾，故 loc[1]-1 正是 '('
		start := loc[1] - 1
		depth := 0
		end := -1
		for i := start; i < len(code); i++ {
			switch code[i] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			continue // 未闭合（跨行字符串/语法怪形状），跳过不猜
		}
		argStr := code[start+1 : end]
		// 只切深度 0 的逗号：NewFoo(Bar(1, 2), x) 里 Bar 的逗号不属于顶层
		d := 0
		top := strings.Builder{}
		for i := 0; i < len(argStr); i++ {
			c := argStr[i]
			if c == '(' {
				d++
			} else if c == ')' {
				d--
			}
			if c == ',' && d == 0 {
				top.WriteByte('\n')
			} else {
				top.WriteByte(c)
			}
		}
		for _, a := range strings.Split(top.String(), "\n") {
			if a = strings.TrimSpace(a); a != "" {
				args = append(args, a)
			}
		}
	}
	return args
}

// TestOutputComplianceHandlerIsWired 是上面那条的定点回归。
// 单测里 setupOutputComplianceHandler 直接 new handler，
// 覆盖不到接线；这条从 main.go 的真实注册面把它钉住。
func TestOutputComplianceHandlerIsWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("读 main.go 失败: %v", err)
	}
	code := string(src)
	if !strings.Contains(code, "NewOutputComplianceHandler(") {
		t.Fatal("main.go 里找不到 NewOutputComplianceHandler( —— handler 可能被删了或改了名，请确认 /admin/output-compliance 是否仍需接线")
	}
	if !strings.Contains(code, "outputComplianceHandler.RegisterRoutes(") {
		t.Error("outputComplianceHandler 构造了但没 RegisterRoutes —— " +
			"/admin/output-compliance 的 stats/policy/keywords/review-queue 会全部 404，页面永远空")
	}
}
