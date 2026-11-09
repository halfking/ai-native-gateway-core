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
	"fmt"
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

// ── 读数的归属：★ 扫描根 ★──────────────────────────────────────────────
//
// ★★★ 为什么需要它（★ 实测、★ 第五十轮续记之七 ★）：★★ ★
//
//	★ 这道门扫的是【★ 工作树目录 ★】⇒ ★ 而【★ 同一条命令在两棵工作树上
//	★ 会给出两个答案 ★】⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 谁在错、★ 谁在错 ★】都无从判定 ★】★★ ★
//	★ ⇒ ★ ⇒ ★ **⇒ 【★ 所以【★ 任何一次读数【★ 都必须能回答「★ 它扫的是哪棵树 ★」★★ ★】★★ ★
//
// ★ ★★ 而【★ 它【★ 不只是一句说明 ★】—— ★ ★ 它【★ 就挂在【★ 失败消息 ★】上 ★】★★ ★
// ★ ⇒ ★ ⇒ ★ **⇒ 【★ 因为【★ 门红的时候、★ 那正是【★ 最需要知道它扫的是谁 ★】的时刻 ★】★★ ★
const scanRootNotePrefix = "★ 扫描根："

// ScanRootNote 返回【★ 读数归属说明 ★】——★ 含扫描根本身。
//
// ★ 它被用在两处：★ ① 【★ 每一条违规消息里 ★】—— ★ ★ **无条件** ★（★ CI 上可见 ★）｜
// ★ ② 【★ 每次运行都 t.Logf 一次 ★】—— ★ ★ 而【★ Go 1.24+ 在【★ 非 -v 】下【★ 丢弃
// ★ 【★ 通过的 ★】测试日志 ★】⇒ ★ ⇒ ★ **⇒ 【★ 所以【★ 「★ 每次都打印 ★」★ 这条
// ★ 【★ 只在 -v 模式下成立 ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 唯一【★ 无条件可见 ★】的
// ★ 归属载体【★ 就是【★ 违规消息 ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 所以【★ 文档里引用任何
// ★ 读数时【★ 仍必须【★ 连树态 ＋ 平台 ＋ locale 一起引用 ★】★★ ★**】★★ ★
//
// ★ ★★ 两处【★ 共用【★ 同一条字符串 ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 护栏【★ 断言
// ★ 「★ 消息里含这条 note 的全文 ★」★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 就【★ 顺带证明了 note
// ★ 【★ 本身【★ 是【★ 可被机器读到的 ★】★★ ★】★★ ★**】★★ ★
func ScanRootNote(root string) string {
	return scanRootNotePrefix + root + " ★（★ 这道门扫的是【★ 工作树 ★】⇒ ★ 同一条命令在两棵树上会给出不同读数 ★ ⇒ ★ 所以【★ 引用这条读数时【★ 必须连这棵工作树一起引用 ★】★★ ★）"
}

// FormatViolation 把一条违规渲染成【★ 带扫描根 ★】的消息。
//
// ★ ★★ 口径（★ 与改之前逐字一致、★ 只多了【★ 扫描根那一行 ★】★★ ★）：
// ★ 门红的时候【★ 读的人第一眼就该看到它扫的是哪棵树 ★】⇒ ★ ⇒ ★
// ★ **⇒ 【★ 而【★ 把它挂在【★ 消息构造函数 ★】上【★ 而不是【★ 散落在调用点 ★】★★ ★
// ★ ⇒ ★ ⇒ ★ **⇒ 【★ 这样【★ 将来有人【★ 只改调用点、不改这里 ★】★★ ★ ⇒ ★ ⇒ ★
// ★ **⇒ 【★ 那条消息【★ 就不带根了 ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 本轮新增的护栏
// ★ 【★ 断言的正是【★ 消息里【★ 必须含 root ★】★★ ★】★★ ★ ⇒ ★ ⇒ ★ **⇒ 【★ 于是
// ★ 【★ 这个洞【★ 机器看得见 ★】★★ ★**】★★ ★**
func FormatViolation(root string, d Decl) string {
	return d.Ident + "  (" + d.Metric + ")  声明于 " + d.File + "\n" +
		"  " + ScanRootNote(root) + "\n" +
		"  它会出现在 /metrics 上却恒为 ABSENT（不是 0），面板无数据、rate() 告警永不触发。\n" +
		"  要么接线一个记录调用，要么在 knownUnwired 里写明不接线的原因。"
}

// ViolationResult 是主判据的一次运行结果。
type ViolationResult struct {
	// Root 是本次扫描的根目录（★ 读数归属 ★）。
	Root string
	// DeclCount 是扫到的指标声明数（★ 量具自校准的读数 ★）。
	DeclCount int
	// Violations 是违规消息（★ 每条都含扫描根 ★）。
	Violations []string
}

// CollectViolations 是这道门的主判据（★ 可被合成夹具【★ 同管线 ★】调用 ★）。
//
// ★ ★★ 为什么要抽成【★ 非测试代码里的函数 ★】★★ ★：
//
//	★ ① 【★ 护栏【★ 要能【★ 在【★ 合成树 】上跑【★ 同一条判据 ★】★★ ★】
//	  ⇒ ★ ⇒ ★ **⇒ 【★ 而【★ 合成树【★ 不在测试包目录里 ★】⇒ ★ ⇒ ★ **⇒ 【★ 判据
//	  【★ 必须在【★ 非 _test.go 的文件里 ★】★★ ★**】★★ ★
//	★ ② 【★ 以后【★ 再有人【★ 要给这道门做变异探针 ★】★★ ★ ⇒ ★ ⇒ ★
//	  **⇒ 【★ 不必【★ 再开一个 worktree ★】★★ ★**
func CollectViolations(root string, exempt map[string]string) (ViolationResult, error) {
	res := ViolationResult{Root: root}
	decls, err := CollectDecls(root)
	if err != nil {
		return res, fmt.Errorf("collect declarations: %w", err)
	}
	res.DeclCount = len(decls)

	never, err := NeverRecorded(root, decls)
	if err != nil {
		return res, fmt.Errorf("scan recording calls: %w", err)
	}
	for _, d := range never {
		if _, ok := exempt[d.Ident]; ok {
			continue
		}
		res.Violations = append(res.Violations, FormatViolation(root, d))
	}
	sort.Strings(res.Violations)
	return res, nil
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
