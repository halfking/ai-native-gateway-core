package streaming

// provider_failover_wiring_test.go —— 2026-11-28（glm-5.3-flash 单候选 429 事故）
// 的两道**入口装配门**。
//
// 事故的修复主体在 executors（指纹槽预过滤不再丢弃候选）。但同一轮暴露了两个
// 「缺陷不在判定逻辑、在入口漏填/漏接」的形态——与 2026-11-13
// DispatchAllowProviderChange（dispatch_provider_change_wiring_test.go）和
// 2026-11-18 provider_circuit_open（provider_circuit_open_test.go）同型：
//
//	① 内部回环流量的 ExecParams.InternalRequest：三个入口闭包在巨型 handler
//	   里无法在单测中实例化 ⇒ 源码形状门（AST）。
//	② 「全部候选被限流」的专属出口：kind 判据在 rate_limit_exhausted.go 有
//	   纯函数测试且全部通过——漏的是某个协议面没调用它 ⇒ 面级调用点门。
//
// 变异臂（红→绿验证过）：见各用例注释。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/authentication"
)

// TestEveryExecParamsAssemblyPointMarksInternalRequest：三个协议面文件里的
// **每一个** executors.ExecParams 复合字面量都必须设置 InternalRequest，且值
// 必须是 internalKeyRequest(...) 调用（不许内联 true/false——常量绕过
// KeyInfo.IsSystem / IsInternal 的动态判定）。
//
// 变异臂：删掉任一面的 InternalRequest 行 ⇒ 红；改成字面量 ⇒ 红。
func TestEveryExecParamsAssemblyPointMarksInternalRequest(t *testing.T) {
	for _, file := range []string{"handler.go", "messages.go", "responses.go"} {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		var literals []*ast.CompositeLit
		ast.Inspect(parsed, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
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
		if len(literals) == 0 {
			t.Fatalf("%s: no executors.ExecParams literal found", file)
		}
		for _, lit := range literals {
			pos := fset.Position(lit.Pos())
			found := false
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "InternalRequest" {
					continue
				}
				found = true
				call, ok := kv.Value.(*ast.CallExpr)
				if !ok {
					t.Errorf("%s:%d: InternalRequest value = %s, inline constants bypass KeyInfo.IsSystem", file, pos.Line, exprString(kv.Value))
					continue
				}
				if fn, ok := call.Fun.(*ast.Ident); !ok || fn.Name != "internalKeyRequest" {
					t.Errorf("%s:%d: InternalRequest value = %s(...), want internalKeyRequest(...)", file, pos.Line, exprString(call.Fun))
				}
			}
			if !found {
				t.Errorf("%s:%d: executors.ExecParams literal does not set InternalRequest — internal traffic on this assembly point would hold an identity slot (2026-11-28 incident shape)", file, pos.Line)
			}
		}
	}
}

// TestInternalKeyRequestKeysOnSystemOrInternal pins the predicate the faces
// share: only a system / internal key is internal traffic.
func TestInternalKeyRequestKeysOnSystemOrInternal(t *testing.T) {
	if internalKeyRequest(nil) {
		t.Fatal("nil key info is not internal traffic")
	}
	if internalKeyRequest(&authentication.KeyInfo{ID: 89}) {
		t.Fatal("an ordinary user key must not be treated as internal traffic")
	}
	if !internalKeyRequest(&authentication.KeyInfo{ID: 398, IsSystem: true}) {
		t.Fatal("a system key (api_keys.is_system) is internal traffic")
	}
	if !internalKeyRequest(&authentication.KeyInfo{ID: 1, IsInternal: true}) {
		t.Fatal("an internal loopback key is internal traffic")
	}
}

// TestAllProtocolEntryFacesHandleRateLimitExhaustion：三个协议面都必须有
// 「全部候选被上游限流」的专属出口。
//
//   - chat 面：既有内联分支（execErrTyped.LastKind == errorsx.KindRateLimit）；
//   - messages / responses 面：共享判定 rateLimitExhaustedRejection 必须有
//     **两处**调用（落库分类 + 渲染）——只接一半 = 库里对、客户端错，或反之。
//
// 变异臂：删掉 messages.go 渲染段的调用 ⇒ 计数 1 ⇒ 红。
func TestAllProtocolEntryFacesHandleRateLimitExhaustion(t *testing.T) {
	chatSrc, err := os.ReadFile("handler.go")
	if err != nil {
		t.Fatalf("read handler.go: %v", err)
	}
	if !strings.Contains(string(chatSrc), "execErrTyped.LastKind == errorsx.KindRateLimit") {
		t.Error("handler.go lost its rate-limit exhaustion branch (HTTP 429 + Retry-After since 2026-09-07)")
	}

	for _, file := range []string{"messages.go", "responses.go"} {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		calls := 0
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "rateLimitExhaustedRejection" {
				calls++
			}
			return true
		})
		if calls < 2 {
			t.Errorf("%s: rateLimitExhaustedRejection call sites = %d, want >= 2 (persisted classification AND client rendering)", file, calls)
		}
	}
}
