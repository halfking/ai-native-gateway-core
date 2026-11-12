package streaming

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestAllProtocolEntryFacesWireDispatchProviderChange 钉住 2026-11-13 事故的
// 修复形态：三个协议入口（chat=handler.go / anthropic=messages.go /
// responses=responses.go）的主 ExecParams 必须都携带 DispatchAllowProviderChange。
//
// ── 事故（211 实证，client_request_id 494d79b7，request 6bdd7066）──────
// ZCode 经 /v1/messages 调 glm-5.3-flash：dispatch 选中 bigmodel coding
// (provider 32) ⇒ 3 个兄弟凭据全部 request_rejected（上游 1210「API 调用
// 参数有误」，body digest a45592035ace5eae 逐字节一致）⇒ PlanSwitchCred 被
// providerSwitchAllowed 拦住（AllowProviderChange=false ⇒ 只许同供应商兄弟）
// ⇒ 换模许可也是 false ⇒ 终态 503「All providers unavailable」。而 resolve
// 面里同一模型有 10 个跨供应商候选、provider 18 同窗口 869 次真实成功。
//
// 根因＝「同一缺陷只修一半」的协议面形态：chat 面（handler.go）与
// responses 面（responses.go）早已设置 DispatchAllowProviderChange :=
// hasMultipleProviders(candidates)，唯独 messages.go 漏了 ⇒ ExecParams 零值
// false。缺陷不在 dispatch 的切换逻辑——在那套逻辑看得见但摸不着的许可位。
//
// ── 门为什么是 AST 形状而不是行为测试 ────────────────────────────────
// 缺陷本体就是「字段漏填」：所有运行时行为（providerSwitchAllowed、
// PlanSwitchCred）在 dispatch 包里都有自己的测试且全部通过——许可位是
// 假值时它们的行为也完全正确。唯一能抓住这类缺陷的层＝入口装配层，
// 而三个入口闭包在巨型 handler 里无法在单测里实例化 ⇒ 源码形状门。
//
// 判据（对三个文件的 buildExecParams 闭包体里的 ExecParams 复合字面量）：
//
//	① 字段 DispatchAllowProviderChange 存在；
//	② 它的值是标识符 dispatchAllowProviderChange（不许内联 true/false——
//	  内联常量会绕过 hasMultipleProviders 的动态判定）；
//	③ 同文件里存在 hasMultipleProviders(candidates) 的调用（值的来源）。
//	④ messages.go / responses.go 必须走共享语义源 requestDispatchOptions
//	  （防第四份平行实现；chat 面因 retry 循环形态差异保留内联实现，
//	  由 ①-③ 覆盖）。
//
// 变异臂（红→绿验证过）：
//   - 删除 messages.go 的 DispatchAllowProviderChange 行 ⇒ ① 红；
//   - 把值改成字面量 true ⇒ ② 红；
//   - 删除 requestDispatchOptions 调用 ⇒ ④ 红。
func TestAllProtocolEntryFacesWireDispatchProviderChange(t *testing.T) {
	faces := []struct {
		file          string
		requireShared bool
	}{
		{file: "messages.go", requireShared: true},
		{file: "responses.go", requireShared: true},
		{file: "handler.go", requireShared: false},
	}

	for _, face := range faces {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, face.file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", face.file, err)
		}

		// 找到 `buildExecParams := func(...) *executors.ExecParams {...}`
		// 闭包体里的 ExecParams 复合字面量。判据认「赋值目标名」，不比位置。
		var literals []*ast.CompositeLit
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}
			for _, lhs := range assign.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name != "buildExecParams" {
					continue
				}
				fl, ok := assign.Rhs[0].(*ast.FuncLit)
				if !ok {
					continue
				}
				ast.Inspect(fl.Body, func(inner ast.Node) bool {
					cl, ok := inner.(*ast.CompositeLit)
					if !ok {
						return true
					}
					se, ok := cl.Type.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if x, ok := se.X.(*ast.Ident); ok && x.Name == "executors" && se.Sel.Name == "ExecParams" {
						literals = append(literals, cl)
					}
					return true
				})
			}
			return true
		})
		if len(literals) == 0 {
			t.Fatalf("%s: no executors.ExecParams literal found under the buildExecParams closure", face.file)
		}

		found := false
		for _, lit := range literals {
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "DispatchAllowProviderChange" {
					continue
				}
				found = true
				val, ok := kv.Value.(*ast.Ident)
				if !ok || val.Name != "dispatchAllowProviderChange" {
					t.Errorf(
						"%s: DispatchAllowProviderChange value = %s, want identifier dispatchAllowProviderChange (inline constants bypass hasMultipleProviders)",
						face.file, exprString(kv.Value),
					)
				}
			}
		}
		if !found {
			t.Errorf("%s: main ExecParams does not set DispatchAllowProviderChange — /v1 traffic on this face cannot fail over across providers (2026-11-13 incident shape)", face.file)
		}

		// 判据③：值的来源必须在同文件可见（hasMultipleProviders 调用）。
		src, err := os.ReadFile(face.file)
		if err != nil {
			t.Fatalf("read %s: %v", face.file, err)
		}
		if !strings.Contains(string(src), "hasMultipleProviders(candidates)") {
			t.Errorf("%s: missing hasMultipleProviders(candidates) call — dispatchAllowProviderChange has no live source", face.file)
		}

		// 判据④：messages/responses 面必须委托共享语义源（防第四份平行实现）。
		if face.requireShared && !strings.Contains(string(src), "requestDispatchOptions(") {
			t.Errorf("%s: must derive dispatch options from the shared requestDispatchOptions helper (single semantic source)", face.file)
		}
	}
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.BasicLit:
		return v.Value
	default:
		return "<expr>"
	}
}
