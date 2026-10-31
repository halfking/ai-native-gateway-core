// Command address_family_decision_audit 普查全仓「哪些代码在判断某类型算不算地址族」。
//
// # 它要回答的问题
//
// 「地址族 = 域名 / 地址 / ip」这条口径在 r1038b（2026-10-27）被收进
// `domains/sanitizerules.IsAddressFamilyType` 这**一份 SSOT**，但**只有当时已知的
// 两个面**被改了。r1038b 之后本轮普查发现**还有第三个面**（见下），它持有一份
// **自己的类型映射副本**，而且**已经漂移**：
//
//	domains/outputcompliance/managed_rules.go 的 Checker.RuleConfig()
//	  domain / hostname / address ⇒ (custom, mask)   ← 遮蔽
//	domains/sanitizerules/legacy_baseline.go 的 LegacyPIIRule（SSOT 那一份）
//	  domain / hostname / address ⇒ (server_ip, log)  ← 只记录不遮蔽
//
// ⇒ 同一个**声明类型**，在两条路径上得到**相反待遇** —— 这正是 r1038b 的 D1
// （两份名单漂移）在新位置上的复发。
//
// # 为什么需要一道门
//
// 「口径收进 SSOT」只保证**已知的判定点**调同一个函数；它**不保证**下一个新增的
// 判定点也会调。r1038b 的教训正在这里：它把两个面收进 SSOT，却漏掉了第三个面
// ——**不是因为谁忘了读文档，而是因为没有任何东西在问「全仓还有没有别的面」**。
//
// ⇒ 本工具负责**测量**：用 AST（不是文本 grep）把全仓的「地址族判定面」找出来，
// 分成两类：
//
//	① 调 SSOT 的面（membership-call / canonical-call / list-call）
//	② **自己写了一份类型名单或类型分支的面**（type-list / type-switch）—— 危险面
//
// 判定在同包的 `manifest_test.go`（与 `sql_source_indirection_audit` 同一形态：
// **工具算、清单写，两者互相监督**）。
//
// # 用法
//
//	go run ./cmd/tools/address_family_decision_audit
//	go run ./cmd/tools/address_family_decision_audit -root .
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Kind 是「地址族判定面」的形态。
type Kind string

const (
	// KindMembershipCall：调用 `sanitizerules.IsAddressFamilyType`——
	// 「这个类型算不算地址族」的正牌问法。
	KindMembershipCall Kind = "membership-call"
	// KindCanonicalCall：调用 `sanitizerules.LegacyPIIRule`——
	// **声明类型 → 托管规则类型**的规范化入口（地址族与否由它产出的 type 决定）。
	KindCanonicalCall Kind = "canonical-call"
	// KindListCall：调用 `sanitizerules.AddressFamilyTypes`——
	// 读那份名单本身（例如门禁在遍历它）。
	KindListCall Kind = "list-call"
	// KindTypeList：**自己写了一份地址族类型名的集合**（复合字面量）。
	// ⇒ 危险面：这是「又一份名单」。
	KindTypeList Kind = "type-list"
	// KindTypeSwitch：**自己写了一份按类型分支的 switch**。
	// ⇒ 危险面：这是「又一份口径」。
	KindTypeSwitch Kind = "type-switch"
)

// Dangerous 报告这个形态是不是「自己又写了一份」。
func (k Kind) Dangerous() bool { return k == KindTypeList || k == KindTypeSwitch }

// Site 是一个地址族判定面。
type Site struct {
	// File 是仓库相对路径。
	File string
	// Line 是 1 起的行号。
	Line int
	// Func 是所在函数名（顶层为 ""）。
	Func string
	// Kind 是形态。
	Kind Kind
	// Types 是这个面上出现的地址族类型名（去重、排序）。
	Types []string
}

// String 是给人看的一行。
func (s Site) String() string {
	return fmt.Sprintf("%s:%d (%s) [%s] %s", s.File, s.Line, s.Kind, s.Func, strings.Join(s.Types, ","))
}

// ssotTypeNames 是 SSOT 名单里的类型名（`domains/sanitizerules/address_family.go`）。
//
// ★ 这份副本是**故意的**：它让本工具能**独立地**发现「某处自己写了一份名单」，
// 而不是靠 SSOT 的导出函数去发现 —— 靠 SSOT 的话，抄件与原件长得一样就分不出来。
// ⇒ 副本与 SSOT 不一致时，`manifest_test.go` 里的
// `TestAddressFamilyTypeNameCopyMatchesTheSSOT` 会红（抄件漂移 ⇒ 门红）。
var ssotTypeNames = []string{
	"ip",
	"ip_address",
	"ipv6",
	"server_ip",
	"server_ipv6",
	"internal_ip",
	"domain",
	"hostname",
	"address",
}

// ssotFunctions 是「调 SSOT」的三个函数名。
//
// ★ 同样是有意的副本：SSOT 改名时，本工具会**找不到**这些调用点 ⇒ 实测集合变小
// ⇒ 与清单对不上 ⇒ 门红 ⇒ 改名的人会被迫同步清单与本工具。
var ssotFunctions = map[string]Kind{
	"IsAddressFamilyType": KindMembershipCall,
	"LegacyPIIRule":       KindCanonicalCall,
	"AddressFamilyTypes":  KindListCall,
}

// skipDirs 是扫描时跳过的目录。
var skipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	"web":          true,
	"docs":         true,
	"dist":         true,
	"bin":          true,
}

// AuditRepo 扫描整个仓库，返回所有「地址族判定面」。
func AuditRepo(root string) ([]Site, error) {
	var sites []Site
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			name := info.Name()
			if p != root && (skipDirs[name] || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		fileSites, err := auditFile(root, p)
		if err != nil {
			return err
		}
		sites = append(sites, fileSites...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].File != sites[j].File {
			return sites[i].File < sites[j].File
		}
		if sites[i].Line != sites[j].Line {
			return sites[i].Line < sites[j].Line
		}
		return sites[i].Kind < sites[j].Kind
	})
	return sites, nil
}

// auditFile 扫一个文件。
func auditFile(root, path string) ([]Site, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)

	var sites []Site
	// collect 认领一个节点。fn 是它所在函数名（包级为 ""）。
	collect := func(n ast.Node, fn string) {
		switch node := n.(type) {
		case *ast.CallExpr:
			if kind, ok := ssotCallKind(node); ok {
				sites = append(sites, Site{
					File: rel, Line: fset.Position(node.Pos()).Line,
					Func: fn, Kind: kind,
				})
			}
		case *ast.CompositeLit:
			types := matchedTypeNames(literalStrings(node))
			if len(types) >= 2 {
				sites = append(sites, Site{
					File: rel, Line: fset.Position(node.Pos()).Line,
					Func: fn, Kind: KindTypeList, Types: types,
				})
			}
		case *ast.SwitchStmt:
			var names []string
			for _, stmt := range node.Body.List {
				clause, ok := stmt.(*ast.CaseClause)
				if !ok {
					continue
				}
				for _, expr := range clause.List {
					names = append(names, literalStringsOf(expr)...)
				}
			}
			types := matchedTypeNames(names)
			if len(types) >= 2 {
				sites = append(sites, Site{
					File: rel, Line: fset.Position(node.Pos()).Line,
					Func: fn, Kind: KindTypeSwitch, Types: types,
				})
			}
		}
	}
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok {
			collect(n, "")
			return true
		}
		// 函数体单独走一遍：函数名是 site 的归属标注。
		ast.Inspect(fn, func(inner ast.Node) bool {
			collect(inner, fn.Name.Name)
			return true
		})
		return false
	})
	return sites, nil
}

// ssotCallKind 判断这个调用是不是「调 SSOT」。
func ssotCallKind(call *ast.CallExpr) (Kind, bool) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "sanitizerules" {
		return "", false
	}
	kind, ok := ssotFunctions[sel.Sel.Name]
	return kind, ok
}

// literalStrings 取一个复合字面量里的所有字符串字面量
// （含 `[]string{"a","b"}` 与 `map[string]bool{"a":true}` 两种形态）。
func literalStrings(lit *ast.CompositeLit) []string {
	var out []string
	for _, elt := range lit.Elts {
		switch e := elt.(type) {
		case *ast.BasicLit:
			out = append(out, literalStringsOf(e)...)
		case *ast.KeyValueExpr:
			out = append(out, literalStringsOf(e.Key)...)
		}
	}
	return out
}

// literalStringsOf 取一个表达式里的字符串字面量（`"x"` 与 `[]string{"x"}`）。
func literalStringsOf(expr ast.Expr) []string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind == token.STRING {
			if s, err := unquote(e.Value); err == nil {
				return []string{s}
			}
		}
	case *ast.CompositeLit:
		return literalStrings(e)
	}
	return nil
}

// unquote 去掉 Go 字符串字面量的引号。
func unquote(lit string) (string, error) {
	return strconv.Unquote(lit)
}

// matchedTypeNames 挑出其中属于地址族的名字（去重、排序）。
func matchedTypeNames(names []string) []string {
	set := map[string]bool{}
	for _, n := range names {
		for _, t := range ssotTypeNames {
			if n == t {
				set[t] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func main() {
	root := flag.String("root", ".", "仓库根目录")
	flag.Parse()

	sites, err := AuditRepo(*root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		os.Exit(2)
	}

	var bound, dangerous []Site
	for _, s := range sites {
		if s.Kind.Dangerous() {
			dangerous = append(dangerous, s)
		} else {
			bound = append(bound, s)
		}
	}

	fmt.Printf("== 调 SSOT 的面（%d 处）==\n", len(bound))
	for _, s := range bound {
		fmt.Printf("  %s\n", s)
	}
	fmt.Printf("\n== 自己写了一份类型名单/分支的面（%d 处）==\n", len(dangerous))
	for _, s := range dangerous {
		fmt.Printf("  %s\n", s)
	}
	fmt.Printf("\n合计 %d 个地址族判定面（其中危险面 %d 个）。\n", len(sites), len(dangerous))
	fmt.Println("本工具的输出是证据；判定在 manifest_test.go（与 sql_source_indirection_audit 同一形态）。")
}
