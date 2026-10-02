// Package metricguard 的仓库级扫描器。
//
// R77：把「指标已声明但从未被记录」从一次性排查变成永久守卫。
//
// 该缺陷类别的危害不是"少了一个计数器"，而是**面板与告警看起来存在、
// 实际永远拿不到数据点**：/metrics 上该 series 恒为 ABSENT（不是 0），
// 用 `rate(...[5m])` 的告警规则永远不触发，而看板显示"无数据"时没人会去查
// 代码。domains/session/v2/README.md 就曾承诺"启用后可通过 Prometheus 监控"
// 4 个 sessions_v2_* 指标，而那 6 个指标全仓每个标识符只有 1 处引用——
// 就是它自己的声明。
//
// 判据是**记录调用**（.Inc/.Add/.Observe/.Set/.WithLabelValues/.With/.Dec/.Sub），
// 不是标识符引用：Go 不允许包级变量未被引用，所以"有没有引用"恒真，
// 问不出任何东西。
package metricguard

import (
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Decl 是一个 prometheus 指标的声明。
type Decl struct {
	Ident  string // Go 标识符
	Metric string // Prometheus 指标名
	File   string // 声明文件（仓库相对路径，斜杠分隔）
}

// recordingMethods 是「记了一次数」的全部形态。少列一个就会产生假警报，
// 多列一个（Delete/LabelValues）会让纯读取被当成记录。
var recordingMethods = []string{
	"WithLabelValues", "With", "Inc", "Add", "AddFloat", "Observe", "Set", "Dec", "Sub",
}

// recordingMethodSet 供令牌级扫描做 O(1) 查表。
var recordingMethodSet = func() map[string]bool {
	m := make(map[string]bool, len(recordingMethods))
	for _, v := range recordingMethods {
		m[v] = true
	}
	return m
}()

// RepoRoot 由调用方注入（测试里从本包位置往上两级）。
func repoRootFrom(pkgDir string) string {
	return filepath.Dir(filepath.Dir(pkgDir))
}

// CollectDecls 返回仓库内所有 prometheus/promauto 声明的指标。
//
// 扫描的是**非测试**文件：指标在 _test.go 里"被记录"不构成生产覆盖
// （测试里的 .Inc() 只让测试通过，不产生数据点）。
func CollectDecls(root string) ([]Decl, error) {
	var decls []Decl
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "node_modules", "vendor", "web", ".build-local", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, p, nil, 0)
		if perr != nil {
			return nil // 解析不了就跳过，不让单个坏文件让整个守卫失效
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			vs, ok := n.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 {
				return true
			}
			for _, v := range vs.Values {
				metric, ok := prometheusName(v)
				if !ok {
					continue
				}
				decls = append(decls, Decl{vs.Names[0].Name, metric, rel})
			}
			return true
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(decls, func(i, j int) bool { return decls[i].Ident < decls[j].Ident })
	return decls, nil
}

// prometheusName 判断一个值表达式是否为 prometheus/promauto 构造调用，
// 并从中取出 Name 字段。
func prometheusName(v ast.Expr) (string, bool) {
	call, ok := v.(*ast.CallExpr)
	if !ok {
		return "", false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || (pkg.Name != "prometheus" && pkg.Name != "promauto") {
		return "", false
	}
	name := ""
	ast.Inspect(call, func(m ast.Node) bool {
		kv, ok := m.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		k, ok := kv.Key.(*ast.Ident)
		if !ok || k.Name != "Name" {
			return true
		}
		if bl, ok := kv.Value.(*ast.BasicLit); ok {
			name = strings.Trim(bl.Value, `"`)
		}
		return true
	})
	return name, name != ""
}

// NeverRecorded 返回「声明了但生产代码里没有任何记录调用」的指标。
//
// 性能：最初的实现是「每个声明 × 每个文件跑一次正则」= 295 × ~3000 次，
// 单测要 40s，两条守卫合计 80s——塞不进 make guards 的 120s 预算，等于
// 一道没人会跑的门。改为**单遍扫描**：每个文件跑一次，收进一个集合，再与
// 声明集合求差。复杂度从 O(声明×文件) 降到 O(文件)。
//
// 2026-10-01 第十八轮审计：匹配从「原始源码字节上的正则」改为 go/scanner
// 令牌流。旧正则会把**注释与字符串字面量**里的 `Foo.Inc()` 也算成"已记录"
// ——注释里一句"记得调用 fooCounter.Inc()"就能让守卫对真正的沉睡指标放行，
// 这正是本仓"grep 文本守卫"反复吃亏的形态。令牌级匹配下 COMMENT/STRING
// 根本不进入 IDENT-.-IDENT 形态，天然免疫；扫描器仍只是词法级（无 AST），
// 单遍成本与正则同量级。
func NeverRecorded(root string, decls []Decl) ([]Decl, error) {
	recorded := map[string]bool{}
	err := WalkSources(root, func(rel string, src []byte) error {
		if strings.HasSuffix(rel, "_test.go") {
			// 指标在 _test.go 里"被记录"不构成生产覆盖：测试里的 .Inc()
			// 只让测试通过，不产生数据点。
			return nil
		}
		recordRecordingCalls(src, rel, recorded)
		return nil
	})
	if err != nil {
		return nil, err
	}

	var out []Decl
	for _, d := range decls {
		if !recorded[d.Ident] {
			out = append(out, d)
		}
	}
	return out, nil
}

// recordRecordingCalls 把 src 里所有 `IDENT.<recording method>` 形态记入
// recorded。只看真实代码令牌：注释（含文档注释）与字符串/字符字面量里的
// 同形态文本不参与。
func recordRecordingCalls(src []byte, rel string, recorded map[string]bool) {
	fset := token.NewFileSet()
	file := fset.AddFile(rel, fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, func(token.Position, string) {}, 0)

	// 形态：IDENT '.' IDENT(方法)。PERIOD 只有紧跟在 IDENT 之后才算链前缀；
	// 其它任何令牌（含 COMMENT / STRING / 字符串拼接的加号）都打断链。
	prevIdent := ""
	afterPeriod := false
	for {
		_, tok, lit := s.Scan()
		switch tok {
		case token.EOF:
			return
		case token.IDENT:
			if afterPeriod && prevIdent != "" && recordingMethodSet[lit] {
				recorded[prevIdent] = true
			}
			prevIdent = lit
			afterPeriod = false
		case token.PERIOD:
			afterPeriod = prevIdent != ""
		default:
			prevIdent = ""
			afterPeriod = false
		}
	}
}

// WalkSources 便于测试侧复用同一套目录跳过规则。
func WalkSources(root string, fn func(rel string, src []byte) error) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "web", ".build-local", "bin":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		b, e := os.ReadFile(p)
		if e != nil {
			return nil
		}
		return fn(filepath.ToSlash(rel), b)
	})
}
