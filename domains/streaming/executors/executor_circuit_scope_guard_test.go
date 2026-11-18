package executors

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ★ P0-a（2026-11-19 审计轮）生产调用点的**作用域一致性门禁**。
//
// 前面的钉测证明的是「给定同一组 (provider, credential, model) 时，各路径
// 落在同一把 breaker 上」。但真正会出事的是**调用点传了不同的 model 分量**：
// 准入传 cand.RawModel、记账传 cand.BindingRawModel() ⇒ 准入查 A、记账写 B
// ⇒ 熔断永不跳闸。
//
// 纯正则抓不住这种分裂（参数形态太多），所以这里用 go/ast 把生产代码里
// 每一个熔断调用点逐个拆开看，检查它的 model 实参是否来自**同一个定义源**
// （circuitScopeModel / decision.BreakerScopeModel / scopeModel / 协议腿 model 形参）。
//
// 变异实验：把 executor_dispatch.go 的准入改回 cand.RawModel ⇒ 本门禁转红。

// breakerModelArgIndex 列出每个熔断 Manager 方法里 model 实参的位置。
var breakerModelArgIndex = map[string]int{
	"AllowPerModel":                2,
	"RecordFailurePerModel":        2,
	"RecordSuccessPerModel":        2,
	"RecordFailureWithBillingMode": 2,
	"RecordFailureWithReset":       2,
	"GetPerModel":                  2,
	"GetModelState":                2,
	"ReleaseProbe":                 2,
	"PeekAllowed":                  2,
	"RecoveredAt":                  2,
	"Reset":                        2,
	"RecordModelScopedFailure":     3,
}

// allowedScopeExprs 是「作用域 model 分量」的合法来源。
//
//	· circuitScopeModel(x)         —— offer 身份（生产统一入口）
//	· decision.BreakerScopeModel   —— nodehealth 决策携带的作用域分量
//	· scopeModel                   —— nodehealth 适配器内的局部别名
//	· model                        —— 协议腿函数的 model 形参（其值由
//	                                 circuitScopeModel(cand) 传入）
var allowedScopeExprs = map[string]bool{
	"scopeModel":                 true,
	"model":                      true,
	"decision.BreakerScopeModel": true,
}

// TestCircuitScopeGuardProductionCallSitesUseSameScopeSource 扫描
// domains/streaming/executors 下的**生产**文件，断言每个熔断调用点的
// model 实参都来自统一的作用域源。
func TestCircuitScopeGuardProductionCallSitesUseSameScopeSource(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "domains", "streaming", "executors")

	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}

	var problems []string
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			idx, tracked := breakerModelArgIndex[sel.Sel.Name]
			if !tracked {
				return true
			}
			if !isBreakerReceiver(exprString(sel.X)) {
				return true
			}
			pos := fset.Position(call.Pos())
			if len(call.Args) <= idx {
				problems = append(problems, fmt.Sprintf("%s:%d: %s 调用缺少 model 实参（实参数=%d）",
					pos.Filename, pos.Line, sel.Sel.Name, len(call.Args)))
				return true
			}
			checked++
			arg := exprString(call.Args[idx])
			if allowedScopeExprs[arg] || strings.HasPrefix(arg, "circuitScopeModel(") {
				return true
			}
			problems = append(problems, fmt.Sprintf("%s:%d: %s 的 model 实参 %q 不来自统一作用域源"+
				"（须用 circuitScopeModel(...) / decision.BreakerScopeModel / scopeModel / model）",
				pos.Filename, pos.Line, sel.Sel.Name, arg))
			return true
		})
	}

	if checked == 0 {
		t.Fatal("门禁没有匹配到任何熔断调用点——检查器本身可能失效（扫描路径/方法表被改动）")
	}
	if len(problems) > 0 {
		t.Fatalf("熔断调用点作用域来源不一致（★ P0-a 2026-11-19 审计轮）：\n%s",
			strings.Join(problems, "\n"))
	}
}

// isBreakerReceiver 判断接收者是否确实是熔断器 Manager。
//
// 只认已知的接收者形态（*.Circuit / mgr），避免把别的类型的同名方法
// （如 MnfStreak.Reset）误判进来。
func isBreakerReceiver(recv string) bool {
	if strings.HasSuffix(recv, ".Circuit") {
		return true
	}
	return recv == "mgr"
}

// exprString 把表达式还原成便于断言的短字符串。
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprString(v.Fun) + "(...)"
	case *ast.BasicLit:
		return v.Value
	case *ast.BinaryExpr:
		return exprString(v.X) + " " + v.Op.String() + " " + exprString(v.Y)
	default:
		return "?"
	}
}

// repoRoot 定位仓库根目录（本测试文件在 domains/streaming/executors 下）。
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
}
