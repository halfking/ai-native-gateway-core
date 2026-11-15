package streaming

// key_concurrent_limit_pin_test.go — 2026-11-15 双重标准收口轮 + 三点审计轮。
//
// AST 钉测：executors ExecParams 的 KeyConcurrentLimit 字段在本包内的全部
// 赋值点（handler.go / messages.go / responses.go）必须经
// keyInfo.ConfiguredConcurrent()（explicit-only），不得回退
// EffectiveConcurrent()（tier 默认——2026-11-15 双重标准事故形态：闸是
// explicit-only、executors 却按 tier 默认 6 监控，同 key 两把尺）。
//
// 静态扫描选 AST 而非字符串 grep：注释里的函数名（本文件、
// key_concurrent_gate.go 的文档）不误报。
//
// 变异臂：任一调用点改回 EffectiveConcurrent() ⇒ 本测试红并报出文件:行号。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyConcurrentLimitProducersUseConfigured(t *testing.T) {
	dir := "." // 测试工作目录 = 包目录
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	type site struct {
		file string
		line int
	}
	var producers []site
	banned := map[string]bool{
		"EffectiveConcurrent": true, // tier 默认回退版——不得作为执法/监控依据
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			cl, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, elt := range cl.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "KeyConcurrentLimit" {
					continue
				}
				producers = append(producers, site{file: name, line: fset.Position(kv.Pos()).Line})
				// 值表达式（函数字面量体内）里不得出现被禁选择器。
				used := map[string]bool{}
				ast.Inspect(kv.Value, func(v ast.Node) bool {
					if sel, ok := v.(*ast.SelectorExpr); ok {
						used[sel.Sel.Name] = true
					}
					return true
				})
				for bannedName := range banned {
					if used[bannedName] {
						t.Errorf("%s:%d KeyConcurrentLimit 不得使用 %s（tier 默认回退=双重标准源头；唯一口径=ConfiguredConcurrent）",
							name, fset.Position(kv.Pos()).Line, bannedName)
					}
				}
				if !used["ConfiguredConcurrent"] {
					t.Errorf("%s:%d KeyConcurrentLimit 必须经 keyInfo.ConfiguredConcurrent()（explicit-only 单一口径）",
						name, fset.Position(kv.Pos()).Line)
				}
			}
			return true
		})
	}
	if len(producers) == 0 {
		t.Fatal("未扫描到任何 KeyConcurrentLimit 赋值点——扫描器失效（预期 ≥4 处：handler×2/messages/responses）")
	}
	t.Logf("KeyConcurrentLimit 赋值点 %d 处，全部 ConfiguredConcurrent 口径", len(producers))
}
