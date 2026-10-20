package main

// dual_read_v1_probe_guard_test.go —— 静态棘轮：**读 v1 族 SQL 的门才可以走会探 v1 族的入口**。
//
// ## 它守的是什么（2026-10-21 R79 轮实测）
//
// v1 族（`_request_logs` / `_request_logs_hot`）被迁移 890 整体退役
// ⇒ 在**当前形态库**上，`probeDualReadTables` 必然跳过
// ⇒ 凡是走 `openDualReadGate` 的门都**永远 SKIP ⇒ 永远绿**
// ⇒ 而 `go test` 把 SKIP 记成 PASS ⇒ ⇒ **没人看得见它已经不测任何东西。**
//
// R79 轮实测的族谱（cmd/gateway 的 9 个 dual-read 真库门）：
//
//	6 个真的读 v1 族 SQL   ⇒ 走 openDualReadGate 是对的（但契约已退役 ⇒ 待裁决）
//	3 个**不读**业务表     ⇒ 被共用入口连坐成永远 SKIP ⇒ ★ **本门就是冲它们来的**
//
// ## 规则
//
// 一个门若调用 `openDualReadGate`（会探 v1 族的入口），
// 则它（**含它调用的门内辅助函数**）必须**真的引用 v1 族**；
// 否则它就是「被连坐」⇒ 永远 SKIP ⇒ 永远绿。
//
// ⇒ 修法是**改用 `openDualReadConnection`（只验连通性）**，
// 那道门就会在当前形态库上**真的会跑、真的会红**。
//
// ## ★ 为什么「基础设施函数」不算依赖
//
// 闭包计算**刻意不穿过** `openDualReadGate` / `probeDualReadTables` /
// `openDualReadPool` / `openDualReadConnection` ——
// 因为 `probeDualReadTables` 自己就写着 `_request_logs`，
// 若不排除，任何调用方都会被判成「依赖 v1 族」⇒ ⇒ **判据恒绿**。
// ⇒ ★ **「不许靠基础设施里的字样蒙混过关」** —— 这一条是本门有牙的前提。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// dualReadProbeInfrastructure 基础设施函数：闭包计算**不穿过**它们。
//
// 理由见文件头：「不许靠基础设施里的字样蒙混过关」。
var dualReadProbeInfrastructure = map[string]bool{
	"openDualReadGate":       true,
	"openDualReadPool":       true,
	"openDualReadConnection": true,
	"probeDualReadTables":    true,
	"resolveDualReadDSN":     true,
	"openDualReadValidator":  true,
}

// dualReadV1FamilyMarker v1 族的判别标记（**族语义**，前缀命中即算命中）。
//
// ⚠ 为什么是族语义而不是标识符边界匹配：`_` **就是**标识符字节
// ⇒ 边界匹配会让 `_request_logs_hot` 漏掉整个族。
const dualReadV1FamilyMarker = "_request_logs"

// dualReadFuncFacts 一个门内顶层函数的静态事实。
type dualReadFuncFacts struct {
	name     string
	mentions bool     // 函数体（含字面量）里提到 v1 族
	calls    []string // 它调用的门内函数名
}

// collectDualReadFuncFacts 扫本包所有 *_test.go，收集顶层函数的事实。
func collectDualReadFuncFacts(t *testing.T, dir string, files []string) map[string]dualReadFuncFacts {
	t.Helper()
	out := map[string]dualReadFuncFacts{}
	for _, file := range files {
		src, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", file, err)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, src, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			facts := dualReadFuncFacts{name: fn.Name.Name}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.BasicLit:
					if node.Kind != token.STRING {
						return true
					}
					text, err := strconv.Unquote(node.Value)
					if err != nil {
						return true
					}
					if strings.Contains(text, dualReadV1FamilyMarker) {
						facts.mentions = true
					}
				case *ast.CallExpr:
					if id, ok := node.Fun.(*ast.Ident); ok {
						facts.calls = append(facts.calls, id.Name)
					}
				}
				return true
			})
			out[fn.Name.Name] = facts
		}
	}
	return out
}

// touchesV1Family 沿着门内调用图闭包判断「是否真的依赖 v1 族」。
//
// 闭包**不穿过**基础设施函数（dualReadProbeInfrastructure）。
func touchesV1Family(facts map[string]dualReadFuncFacts, start string) bool {
	seen := map[string]bool{}
	var walk func(name string) bool
	walk = func(name string) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		f, ok := facts[name]
		if !ok {
			return false
		}
		if f.mentions {
			return true
		}
		for _, callee := range f.calls {
			if dualReadProbeInfrastructure[callee] {
				continue
			}
			if walk(callee) {
				return true
			}
		}
		return false
	}
	return walk(start)
}

// TestDualReadGatesUsingV1ProbeMustActuallyReadV1 是本门的主判据。
func TestDualReadGatesUsingV1ProbeMustActuallyReadV1(t *testing.T) {
	files := dualReadTestFiles(t)
	facts := collectDualReadFuncFacts(t, ".", files)

	checked := 0
	for name, f := range facts {
		usesProbe := false
		for _, callee := range f.calls {
			if callee == "openDualReadGate" {
				usesProbe = true
				break
			}
		}
		if !usesProbe {
			continue
		}
		checked++
		if !touchesV1Family(facts, name) {
			t.Errorf("%s 走的是会探 v1 族的入口 openDualReadGate，但它（连同它调用的门内辅助函数）"+
				"**并不读 v1 族** ⇒ 在当前形态库上它永远 SKIP ⇒ 永远绿（go test 把 SKIP 记成 PASS）"+
				"⇒ 请改用 openDualReadConnection（只验连通性）", name)
		}
	}
	if checked == 0 {
		t.Fatal("一个走 openDualReadGate 的门都没找到 ⇒ 扫描范围或入口名变了，本门已恒绿")
	}
}

// dualReadTestFiles 列出本包的测试文件（顶层）。
func dualReadTestFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("列测试文件失败: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("没找到任何 *_test.go ⇒ 扫描范围出了问题（门会恒绿）")
	}
	return matches
}
