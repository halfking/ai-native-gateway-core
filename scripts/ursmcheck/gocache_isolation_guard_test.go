package ursm_test

// AUDIT-GUARD: scripts/ursmcheck 属 GUARD_PACKAGES_SLOW ⇒ 本门在 `make guards-ursmcheck` 上真跑。

// gocache_isolation_guard_test.go —— r1047 轮（2026-10-28）：「会编译 Go 的子进程 ⇒ 必须走【专用 GOCACHE】」
//
// # 为什么需要这个文件（★ 实测，2026-10-28）
//
// ★★★ **背景**：★ 仓里有若干「门 / 测试 / 工具」会在**子进程**里跑
// ★★★ `go build` / `go test` / `go vet` / `go run` ⇒ ⇒ 子进程会往 **Go 的构建缓存**
// ★★★ 里写产物 ⇒ ⇒ 而那个缓存默认是**用户那个全局的**。
//
// ★★★★ **实测（★ r1046 轮，本机）**：★ 掏空门每跑一次（★ 在**新树**上）往全局
// ★★★★ `GOCACHE` 里塞 **~1.3 GB** ⇒ ⇒ 本机全局缓存一度到 **72 GB** ⇒ ⇒ 磁盘
// ★★★★ 100% 满 ⇒ ⇒ 全量 `go test ./...` **编译阶段**就报 `no space left on
// ★★★★ device` ⇒ ⇒ ★★ **红的原因不指向真正的原因**（★ 是磁盘，不是门有洞）。
//
// ★★★★ **为什么「隔离」能治本**：★ Go 的构建缓存是**内容寻址**的
// ★★★★ （★ action ID = 全部输入内容 + 构建配置的哈希）⇒ ⇒ ★ **它不可能返回
// ★★★★ 陈旧的产物** ⇒ ⇒ ★ 隔离只改「产物落在哪」，**不改「产物是不是对的」**
// ★★★★ ⇒ ⇒ ★★ **零正确性代价**。
//
// # 本门做什么
//
// ★★★ **判据**：★ 仓内**每一个**用 `exec.Command` / `exec.CommandContext` 调起
// ★★★ **go 工具链**的地方 ⇒ ⇒ 它所在的函数**必须**把子进程的 `GOCACHE` 指向
// ★★★ **专用目录**。
//
// ★★ **两种「有隔离」的形态都认**：
// ① **用共享包**：★ 引用 `internal/gocache`（★ `gocache.Env` / `gocache.Value`）
//    ⇒ ⇒ 目录约定**只有一份**（★ `.build-local/.gocache-<purpose>`）；
// ② **就地设**：★ 函数体里出现 `GOCACHE=...` 的字符串字面量
//    ⇒ ⇒ 掏空门用的就是这形态（★ 它额外带「尺寸上限 + 超限整目录删除」）。
//
// ★★★ **为什么第②种也要认**：★ 掏空门那套是 **r1046 轮自己写的**（★ 带自己的
// ★★★ 门与变异臂）⇒ ⇒ ★★ 硬把它改成用共享包 ⇒ ⇒ ★ 要动 **M01 / M02** 两条变异臂
// ★★★ 的锚点 ⇒ ⇒ ★★ 而且会让判据②「问 git 自己」那条**失去独立性**
// ★★★ ⇒ ⇒ ⇒ **共享包是【新增点】的默认形态，★ 不是【存量】的强制形态。**
//
// # ★★ 本门的【覆盖边界】（★ 诚实登记，★ 别把它当全量尺）
//
// ★ ① **只覆盖【Go 文件】里的子进程编译点**（★ ⇒ ⇒ 可用 AST 精确判定）。
// ★    ★★ **shell / Python 脚本**里的编译点（★ 审计脚本、变异台）**不在本门视野内**
// ★    ★★ ⇒ ⇒ ★ 那需要【解析 shell / Python】⇒ ⇒ ★ **那是另一把尺**（★ 下一轮）。
// ★ ② **只认「第一个实参是 `go` / `goBin` / `goCmd`」的调用形态**
//    ⇒ ⇒ ★ 形如 `exec.Command(bin, "run", ...)`（★ 那 `bin` 是**已编好的测试二进制**
// ★    ★★ 不是 go 工具链）**不算** ⇒ ⇒ ★ 不会误伤。
// ★ ③ **不判断子命令是否编译**（★ `go list` / `go env` 也一并要求隔离）
//    ⇒ ⇒ ★ **宁可多要求一点**，★ 也不要「按子命令分类」带来的漏判。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// goInvocationFirstArgs 是【被本门认作「go 工具链调用」】的第一个实参的**变量名**。
//
// ★ ★★ **为什么是白名单而不是「名字里含 go」**：★ 「名字里含 go」会把
// ★ ★★ `exec.Command(gopher, ...)` 这类**与 go 工具链无关**的调用也算进来
// ★ ★★ ⇒ ⇒ ★⇒ 门就会去要求它设 GOCACHE ⇒ ⇒ ★⇒ 假红。
var goInvocationFirstArgs = map[string]bool{
	"go":    true,
	"goBin": true,
	"goCmd": true,
}

// gocacheSkipDirs 是【不扫】的目录（★ vendor 与前端产物）。
var gocacheSkipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	".build-local": true,
	"web":          true,
	"web-mobile":   true,
	"docs":         true,
	"maestro":      true,
}

// gocachePkgInfo 是【一个包】的解析结果。
type gocachePkgInfo struct {
	dir   string
	funcs map[string]*ast.FuncDecl
	// files 存【文件名 ⇒ AST】（★ ★★ 存文件名而不是 AST.File：★ 因为
	// ★★ `ast.File.Name` 是【包名】而不是【文件名】⇒ ⇒ ★ 用它拼路径会得到
	// ★★ 「sql/schema/schema」这种**看起来像路径的错东西**）。
	files []gocacheParsedFile
}

// gocacheParsedFile 是一个已解析的 Go 文件。
type gocacheParsedFile struct {
	rel  string
	pkg  string
	file *ast.File
}

// gocacheSite 是一个【用 go 工具链起子进程】的位置。
type gocacheSite struct {
	file string
	line int
	fn   string
	pkg  string
}

// isGoToolchainCall 判「这个 exec.Command 的第一个实参是不是 go 工具链」。
func isGoToolchainCall(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	switch v := call.Args[0].(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING && strings.Trim(v.Value, `"`) == "go"
	case *ast.Ident:
		return goInvocationFirstArgs[v.Name]
	}
	return false
}

// isExecCommandCall 判「这个调用是不是 exec.Command / exec.CommandContext」。
func isExecCommandCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "exec" {
		return false
	}
	return sel.Sel.Name == "Command" || sel.Sel.Name == "CommandContext"
}

// gocacheFuncEvidence 看一个函数体**本身**有没有「隔离证据」。
//
// ★ 返回 (usesShared, setsGocache)：
//   - usesShared：★ 引用了共享包 `gocache`（★ `gocache.Env` / `gocache.Value` …）
//   - setsGocache：★ 出现了 `GOCACHE=` 开头的字符串字面量（★ 就地设置）
func gocacheFuncEvidence(fn *ast.FuncDecl) (usesShared bool, setsGocache bool) {
	ast.Inspect(fn, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if id, ok := v.X.(*ast.Ident); ok && id.Name == "gocache" {
				usesShared = true
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING && strings.HasPrefix(strings.Trim(v.Value, `"`), "GOCACHE=") {
				setsGocache = true
			}
		}
		return true
	})
	return
}

// gocacheEnvHelpers 找出函数体里所有 `X.Env = <helper>(...)` 的 helper 名。
//
// ★ ★★ **为什么要看「赋给了 .Env」**：★ 因为那才是【子进程环境】被设置的地方
// ★ ★★ ⇒ ⇒ ★ 只看「函数里出现了 GOCACHE=」会误判（★ 例如那个字符串只是
// ★ ★★ 打印在日志里、或者只是个常量）。
func gocacheEnvHelpers(fn *ast.FuncDecl) []string {
	var out []string
	ast.Inspect(fn, func(n ast.Node) bool {
		as, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, rhs := range as.Rhs {
			if i >= len(as.Lhs) {
				break
			}
			sel, ok := as.Lhs[i].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Env" {
				continue
			}
			call, ok := rhs.(*ast.CallExpr)
			if !ok {
				continue
			}
			if id, ok := call.Fun.(*ast.Ident); ok {
				out = append(out, id.Name)
			}
		}
		return true
	})
	return out
}

// gocacheFuncIsolated 判「这个函数起 go 子进程时，环境是否已指向专用缓存」。
func gocacheFuncIsolated(fn *ast.FuncDecl, pkg *gocachePkgInfo, pkgName string) bool {
	usesShared, setsGocache := gocacheFuncEvidence(fn)
	if usesShared || setsGocache {
		return true
	}
	// ★ 形态③：`X.Env = helper(...)` ⇒ helper 在**同一个包**里 ⇒ helper 自己有证据。
	for _, name := range gocacheEnvHelpers(fn) {
		helper, ok := pkg.funcs[pkgName+"."+name]
		if !ok {
			continue
		}
		if u, s := gocacheFuncEvidence(helper); u || s {
			return true
		}
	}
	return false
}

// collectGoCacheSites 扫出全仓所有【用 go 工具链起子进程】的位置。
func collectGoCacheSites(root string) ([]gocacheSite, map[string]*gocachePkgInfo, error) {
	pkgs := map[string]*gocachePkgInfo{}
	var sites []gocacheSite
	// ★ 一个 fset 贯穿全仓（★ ⇒ 位置信息可跨文件比较）。
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if path != root && (gocacheSkipDirs[base] || strings.HasPrefix(base, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		// ★ 预筛：只有【提到 exec.Command】的文件才值得解析。
		if !strings.Contains(string(src), "exec.Command") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		dir := filepath.Dir(rel)

		f, perr := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if perr != nil {
			return fmt.Errorf("解析 %s 失败：%w", rel, perr)
		}
		pkg, ok := pkgs[dir]
		if !ok {
			pkg = &gocachePkgInfo{dir: dir, funcs: map[string]*ast.FuncDecl{}}
			pkgs[dir] = pkg
		}
		pkg.files = append(pkg.files, gocacheParsedFile{rel: rel, pkg: f.Name.Name, file: f})
		for _, decl := range f.Decls {
			if fd, ok := decl.(*ast.FuncDecl); ok && fd.Recv == nil && fd.Name != nil {
				// ★★ **按「包名.函数名」索引**（★ 而不是只按函数名）：★★ 同一个目录里
				// ★★ 可能同时有 `foo` 与 `foo_test` 两个包 ⇒ ⇒ ★★ 只按函数名索引
				// ★★ ⇒ ⇒ 两者会互相覆盖 ⇒ ⇒ ★★ ⇒ **解析结果依赖文件遍历顺序**
				// ★★ ⇒ ⇒ ★★ ⇒ **间歇性假绿 / 假红**。
				pkg.funcs[f.Name.Name+"."+fd.Name.Name] = fd
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	for _, pkg := range pkgs {
		for _, pf := range pkg.files {
			f := pf.file
			rel := pf.rel
			for _, decl := range f.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil {
					continue
				}
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok || !isExecCommandCall(call) || !isGoToolchainCall(call) {
						return true
					}
					sites = append(sites, gocacheSite{
						file: rel,
						line: fset.Position(call.Pos()).Line,
						fn:   fd.Name.Name,
						pkg:  pf.pkg,
					})
					return true
				})
			}
		}
	}
	sort.Slice(sites, func(i, j int) bool {
		if sites[i].file != sites[j].file {
			return sites[i].file < sites[j].file
		}
		return sites[i].line < sites[j].line
	})
	return sites, pkgs, nil
}

// TestGoToolchainSubprocessesUseDedicatedGoCache —— ★★ 主判据：
// ★ 每一个用 go 工具链起子进程的地方 ⇒ 环境都必须指向【专用 GOCACHE】。
func TestGoToolchainSubprocessesUseDedicatedGoCache(t *testing.T) {
	root := gocacheGateRepoRoot(t)

	sites, pkgs, err := collectGoCacheSites(root)
	if err != nil {
		t.Fatalf("扫描子进程编译点失败：%v", err)
	}

	// ★★ **搜索面完整性自检**：★ 一个编译点都没扫到 ⇒ ⇒ 本门是**瞎的**
	// ★★ ⇒ ⇒ 门绿了也不能当「已覆盖」的证据（★ 仓内 §419 的通式：
	// ★★ 「0 命中」有两种读法 ⇒ ★★ 扫到了没发现 vs 根本没扫到）。
	if len(sites) == 0 {
		t.Fatalf("★ 一个 go 工具链子进程点都没扫到 ⇒ ⇒ 【搜索面不完整】⇒ ⇒ " +
			"本门是瞎的 ⇒ ⇒ 别据此下「全仓都隔离了」的结论")
	}

	var offenders []string
	for _, s := range sites {
		pkg := pkgs[filepath.Dir(s.file)]
		fn := pkg.funcs[s.pkg+"."+s.fn]
		if fn == nil {
			offenders = append(offenders, fmt.Sprintf("%s:%d（★ 找不到所在函数 %s）", s.file, s.line, s.fn))
			continue
		}
		if !gocacheFuncIsolated(fn, pkg, s.pkg) {
			offenders = append(offenders, fmt.Sprintf("%s:%d（★ 函数 %s）", s.file, s.line, s.fn))
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("★ %d 处「用 go 工具链起子进程」的地方**没有**指向专用 GOCACHE（★ 共扫到 %d 处）：\n  %s\n"+
			"★ ⇒ ⇒ 这些子进程会往**用户的全局缓存**里写「随树变化而失效」的一次性产物\n"+
			"★ ⇒ ⇒ ⇒ 实测那个缓存一度到 72 GB ⇒ ⇒ 磁盘占满 ⇒ ⇒ 全量 go test ./... 编译阶段就红\n"+
			"★ ⇒ ⇒ ⇒ 修法：★ 用共享包 `gocache.Env(root, \"<purpose>\")`（★ 目录约定只有一份）\n"+
			"★ ⇒ ⇒ ★ 或者就地设 `cmd.Env = append(os.Environ(), \"GOCACHE=\"+…)`",
			len(offenders), len(sites), strings.Join(offenders, "\n  "))
	}
}

// gocacheGateRepoRoot 返回仓根（★ 问 git 自己 ⇒ ★ 独立于任何硬编码路径）。
func gocacheGateRepoRoot(t *testing.T) string {
	t.Helper()
	return hollowRepoRoot(t)
}

// ── ★★ 第二面：★【脚本家族】（★ shell / Python）★★ ──

// gocacheScriptFamily 是【本门覆盖的脚本族】。
//
// ★ ★★ **为什么只覆盖这一族**：★★ 本门的**判据是【文件级】的**（★ 「这个文件有没有
// ★ ★★ 设置 GOCACHE」）⇒ ⇒ ★★ 而文件级的判据对【部署脚本 / 压测脚本】那一族
// ★ ★★ **判不准**（★ 那些脚本的 go 调用形态五花八门：★ 有的是 `go build`、
// ★ ★★ 有的是在容器里编、★ 有的是 `go list`）⇒ ⇒ ★★ ⇒ **那一族归下一轮**
// ★ ★★ （★ 需要【逐个读懂那个脚本在干什么】才判得准 ⇒ ⇒ ★★ 不是一条正则能做的）。
//
// ★ ★★ **这一族为什么【必须】管**：★★ 它们是【**改树 ⇒ 跑 go test ⇒ 还原**】
// ★ ★★ ⇒ ⇒ ★★ 树每变一次 ⇒ ⇒ ★★ 编译产物的 action ID 就变 ⇒ ⇒ ★★ 旧的【永远
// ★ ★★ 不会再被命中】⇒ ⇒ ★★ ⇒ **每次跑都产生一批纯垃圾** ⇒ ⇒ ★★ ⇒ **A 级**
// ★ ★★ （★ 而审计脚本那一族在**固定树**上跑 ⇒ ⇒ 条目可复用 ⇒ ⇒ 是**容量**问题
// ★ ★★ 不是**垃圾**问题 ⇒ ⇒ 优先级低一档）。
var gocacheScriptFamily = []string{
	"scripts/.mutate-*.sh",
	"scripts/.mutate-*.py",
	"scripts/mut-*.sh",
	"scripts/mutate-*.py",
	"scripts/audit/mutate_*.sh",
	"scripts/audit/mutate_*.py",
}

// gocacheGoCmdRE 认【go 工具链的编译型子命令】。
//
// ★ ★★ **两种形态都要认**：★★ ① `go test …`（★ shell 与字符串里）② `["go", "test", …]`
// ★ ★★ （★ Python 的列表形态）⇒ ⇒ ★★ ⇒ **只认第一种 ⇒ ★★ 会漏掉一半**
// ★ ★★ ⇒ ⇒ ★★ （★ 实测：★ 仓里 `scripts/mutate-14336-*.py` 就是列表形态）。
var gocacheGoCmdRE = regexp.MustCompile(`\bgo\s+(build|test|vet|run)\b`)

// gocacheGoCmdListRE 认【列表形态】的 go 工具链调用。
var gocacheGoCmdListRE = regexp.MustCompile(`["']go["']\s*,\s*["'](build|test|vet|run)["']`)

// gocacheSetRE 认【设置了 GOCACHE】的各种写法。
//
// ★ ★★ **为什么认这几种形态**：★★ shell 侧是 `export GOCACHE=…`（★ 或
// ★ ★★ `GOCACHE=…` 在 env 数组里），Python 侧是 `["GOCACHE"] = …`（★ 或
// ★ ★★ `env["GOCACHE"] = …`），还有两边的**共享入口**（★ `gocache_use` /
// ★ ★★ `gocache.env(` / `gocache.dir_for(` / ★ ★★ `gocache.apply_to_environ(`）
// ★ ★★ ⇒ ⇒ ★★ ⇒ **一个都不许漏**。
//
// ★ ★★★★ **r1048 收尾轮补的一条（★ 治「两门口径不一致」）**：★★★★ 本门（★ A 级）
// ★ ★★★★ 原先**只认** `gocache.dir_for(` ⇒ ⇒ ★★★★ **不认** `gocache.apply_to_environ(`
// ★ ★★★★ ⇒ ⇒ ★★★★ 而 r1048 轮的 B 级门**两个都认** ⇒ ⇒ ★★★★ ⇒ ⇒ ★★ **同一个脚本
// ★ ★★★★ 在一道门里绿、在另一道门里红** ⇒ ⇒ ★★★★ ⇒ ⇒ ★★ **两门对「什么算隔离」
// ★ ★★★★ 必须说同一句话** ⇒ ⇒ ★★★★ ⇒ ⇒ ★★ ⇒ ⇒ **本轮实测**：★ 上游新脚本
// ★ ★★★★ `scripts/mutate-r180-query-plan-cost-gate.py` 被改成失败安全形态
// ★ ★★★★ （★ `gocache.apply_to_environ("mutate")`）⇒ ⇒ ★★★★ ⇒ ⇒ ★★ **本门当场红**
// ★ ★★★★ ⇒ ⇒ ★★★★ ⇒ ⇒ ★★ **⇒ ⇒ 两门口径统一（★ 本次改动）⇒ ⇒ ★★ ⇒ ⇒ ⇒ 绿**。
var gocacheSetRE = regexp.MustCompile(`GOCACHE\s*=|["']GOCACHE["']\s*\]|gocache_use|gocache\.env\(|gocache\.dir_for\(|gocache\.apply_to_environ\(`)

// gocacheDedicatedRE 认【指向【专用目录】】的写法。
//
// ★★★★ **为什么必须有这一条（★ 本轮被变异臂逼出来的）**：★★★★ 第一版只判
// ★★★★ 「这个文件有没有设置 GOCACHE」⇒ ⇒ ★★★★ **而那是一道【假绿】的判据**：
// ★★★★ `export GOCACHE="$HOME/.cache/go-build"`（★ **用户的全局缓存**）⇒ ⇒ ★★★★
// ★★★★ **也算「设置了 GOCACHE」** ⇒ ⇒ ★★★★ ⇒ **门照样绿** ⇒ ⇒ ★★★★ ⇒ **隔离
// ★★★★ 根本没生效，而门在报「已覆盖」** ⇒ ⇒ ★★★★ ⇒ **★ 这正是「变异臂的价值
// ★★★★ 不在『红』而在『其余照常绿』」那条纪律的现场：★ M04 臂把隔离改成
// ★★★★ 「用户的全局缓存」⇒ ⇒ ★★★★ 门**没有红** ⇒ ⇒ ★★★★ ⇒ 门有洞。
//
// ★★★★ **修法**：★★★★ 「设置了 GOCACHE」**还不够**，★ 必须指向【专用目录】⇒ ⇒
// ★★★★ **两种算专用**：★★ ① **委托给共享入口**（★ `gocache.sh` / `gocache_use`
// ★★★★ / `gocache.env(` / `gocache.dir_for(` / ★ ★★ `gocache.apply_to_environ(`）
// ★★★★ ⇒ ⇒ **按构造就是专用的**；
// ★★★★ ② **字面路径在 `.build-local/` 下**（★ ⇒ ⇒ 在 `.gitignore` 里 ⇒ ⇒
// ★★★★ `git clean -xfd` 一把回收）。
//
// ★ ★★★★ **r1048 收尾轮补的一条**：★★★★ `gocache.apply_to_environ(` 与
// ★ ★★★★ `gocache.dir_for(` **同列**⇒ ⇒ ★★★★ ⇒ **因为它比 `dir_for` 更严**
// ★ ★★★★ （★ 内部先 `ensure()` 建目录并查可写 ⇒ ⇒ 失败就不设 ⇒ ⇒ 脚本照常跑）
// ★ ★★★★ ⇒ ⇒ ★★ **它才是 python 侧该用的那个**（★ 见下面的修法提示）。
// ★ ★★★★ **r1049 审计轮移除的一条（★ `gocache\.dir_for(`）**：★★★★
// ★ ★★★★ `dir_for()` **只算路径** ⇒ ⇒ ★★ 它**不建目录**、★★ **不查可写**
// ★ ★★★★ ⇒ ⇒ ★★ 而 go 拿到一个**不可用**的 GOCACHE 会直接报
// ★ ★★★★ `failed to initialize build cache` ⇒ ⇒ ★★ ⇒ **脚本直接坏掉** ⇒ ⇒ ★★
// ★ ★★★★ ⇒ ★★ ⇒ 而本正则原先把它认作「已指向专用目录」⇒ ⇒ ★★ ⇒ **门绿着，
// ★ ★★★★ 而脚本在受限环境下会坏** ⇒ ⇒ ★★ ⇒ ★★ ⇒ 移出去 ⇒ ⇒ ★★ ⇒ 只认
// ★ ★★★★ **真正失败安全**的入口（★ `gocache_use` / `gocache.env(` /
// ★ ★★★★ `gocache.apply_to_environ(`）。
//
// ★ ★★ **注意 `gocacheSetRE` 里【还留着** `gocache\.dir_for(`**：★★ 那一只是
// ★ ★★ 「这个文件**碰过** GOCACHE 吗」⇒ ⇒ ★★ ⇒ 留着它 ⇒ ⇒ ★★ ⇒ 一个只用
// ★ ★★ `dir_for` 的文件会落到「**设了 GOCACHE，但没指向专用目录**」那条红 ⇒ ⇒
// ★ ★★ ⇒ ★★ ⇒ **红的理由更准确** ⇒ ⇒ ★★ ⇒ 而不是落到「压根没设置 GOCACHE」。
var gocacheDedicatedRE = regexp.MustCompile(`\.build-local/|gocache\.sh|gocache_use|gocache\.env\(|gocache\.apply_to_environ\(`)

// gocacheRawLines 把源码切成【未经任何剥离】的行。
//
// ★ ★★ **为什么【不许在打掩码前剥整行注释】**（★ ★★ r1049 审计轮实测踩到 ★★）：★★
// ★ ★★ Python 里三引号块**内部**的 `#` **不是注释** ⇒ ⇒ ★★ ⇒ 一段三引号字符串的
// ★ ★★ **闭合定界符**若落在某条「以 `#` 开头的行」上 ⇒ ⇒ ★★ ⇒ 那行会被「剥整行
// ★ ★★ 注释」这一步**删掉** ⇒ ⇒ ★★ ⇒ **闭合符消失** ⇒ ⇒ ★★ ⇒ 掩码的状态机从此
// ★ ★★ 错位 ⇒ ⇒ ★★ ⇒ **该文件后半段全被当成数据** ⇒ ⇒ ★★ ⇒ 它调的那些 go 命令
// ★ ★★ **门看不见** ⇒ ⇒ ★★ ⇒ **假绿**（★ 实测：`scripts/mutate-r164-freshness-gate.py`
// ★ ★★ ⇒ ⇒ 它的 `["go", "test", …]` 在门眼里**根本不存在** ⇒ ⇒ ⇒ 作用域少算 1 个）。
// ★ ★★ ⇒ ⇒ 所以顺序必须是【先打掩码（★ 掩码自己认注释）⇒ ⇒ 再取代码视图】。
func gocacheRawLines(src string) []string {
	return strings.Split(src, "\n")
}

// TestMutationHarnessScriptsUseDedicatedGoCache —— ★★ 第二面：
// ★ 凡是【改树 ⇒ 跑 go test ⇒ 还原】的脚本 ⇒ 都必须把 `GOCACHE` 指向【专用目录】。
func TestMutationHarnessScriptsUseDedicatedGoCache(t *testing.T) {
	root := gocacheGateRepoRoot(t)

	var checked, invoking []string
	var offenders []string

	for _, pattern := range gocacheScriptFamily {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatalf("展开 %s 失败：%v", pattern, err)
		}
		for _, path := range matches {
			src, rerr := os.ReadFile(path)
			if rerr != nil {
				t.Fatalf("读 %s 失败：%v", path, rerr)
			}
			rel, _ := filepath.Rel(root, path)
			checked = append(checked, rel)

			// ★ ★★ **r1049 审计轮**：★ 编译型判定改走【编译型掩码】⇒ ⇒ ★★ ⇒
			// ★ ★★ 注释与 docstring 里的 go 字样**不算**调用 ⇒ ⇒ ★★ ⇒ **两门口径
			// ★ ★★ 说同一句话**（★ 与 B 级门 `gocacheScriptHasCompile` 同实现）。
			// ★ ★★ ★★ **不许在打掩码前剥整行注释**（★ ★★ 见 `gocacheRawLines` 的
			// ★ ★★ 注释：★ ★★ 三引号的闭合符若落在注释行上 ⇒ ⇒ ★★ ⇒ 掩码错位 ⇒ ⇒
			// ★ ★★ ⇒ ★★ **那个文件后半段全被当成数据** ⇒ ⇒ ★★ ⇒ ★★ **假绿** ★★）。
			code := gocacheScriptCompileCode(rel, gocacheRawLines(string(src)))
			hasGoCmd := false
			for _, line := range code {
				if gocacheGoCmdRE.MatchString(line) || gocacheGoCmdListRE.MatchString(line) {
					hasGoCmd = true
					break
				}
			}
			if !hasGoCmd {
				continue
			}
			invoking = append(invoking, rel)

			hasSet := false
			for _, line := range code {
				if gocacheSetRE.MatchString(line) {
					hasSet = true
					break
				}
			}
			if !hasSet {
				offenders = append(offenders, rel+"（★ 压根没设置 GOCACHE）")
				continue
			}
			// ★★★ **「设置了」还不够**：★ 必须指向【专用目录】⇒ ⇒ ⇒
			// ★★★ （★ `GOCACHE=$HOME/.cache/go-build` ⇒ ⇒ 是【用户的全局缓存】
			// ★★★ ⇒ ⇒ ⇒ **隔离没生效，而门在报「已覆盖」** ⇒ ⇒ ⇒ **假绿**）。
			dedicated := false
			for _, line := range code {
				if gocacheDedicatedRE.MatchString(line) {
					dedicated = true
					break
				}
			}
			if !dedicated {
				offenders = append(offenders, rel+"（★ 设了 GOCACHE，但**没指向专用目录**）")
			}
		}
	}

	// ★★ **搜索面完整性自检**（★ 与本门第一面同款）：★★ 一个脚本都没扫到 ⇒ ⇒ 门是瞎的。
	if len(checked) == 0 {
		t.Fatalf("★ 脚本家族里一个文件都没扫到 ⇒ ⇒ 【搜索面不完整】⇒ ⇒ 本门是瞎的")
	}
	if len(invoking) == 0 {
		t.Fatalf("★ 脚本家族里一个【调 go 工具链】的脚本都没扫到 ⇒ ⇒ 【搜索面不完整】⇒ ⇒ 本门是瞎的")
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("★ %d 个【调 go 工具链】的脚本**没有**把 GOCACHE 指向专用目录"+
			"（★ 本族共扫到 %d 个脚本、其中 %d 个调 go 工具链）：\n  %s\n"+
			"★ ⇒ ⇒ 判据有两条：★ ① **必须设置** GOCACHE ② **必须指向专用目录**"+
			"（★ `.build-local/` 下 ⇒ ⇒ 在 .gitignore 里 ⇒ ⇒ `git clean -xfd` 可回收）\n"+
			"★ ⇒ ⇒ 它们【改树 ⇒ 跑 go test ⇒ 还原】⇒ ⇒ 树每变一次就产生一批"+
			"【永远不会再被命中】的缓存条目 ⇒ ⇒ ⇒ 纯垃圾\n"+
			"★ ⇒ ⇒ ⇒ 修法：★ shell 用 `export GOCACHE=\"$(\"$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh\" mutate)\"`；\n"+
			"★ ⇒ ⇒ ★ Python 用 `gocache.apply_to_environ(\"mutate\")`（★ ★★ 失败安全：★ 专用目录建不出来时**不设** GOCACHE ⇒ ⇒ 脚本照常跑；\n"+
			"★ ⇒ ⇒ ★ ★★ 旧写法 `os.environ[\"GOCACHE\"] = gocache.dir_for(gocache.repo_root(), \"mutate\")` 也算隔离 ⇒ ⇒ 但它**没有失败安全**（★ 建不出来时 go 会直接报错 ⇒ ⇒ 脚本坏掉）⇒ ⇒ ★★ 优先用前者",
			len(offenders), len(checked), len(invoking), strings.Join(offenders, "\n  "))
	}
}

// ★★★★ TestGoCacheDedicatedFormsAreRecognized ★★★★
//
// ★ ★★★ **这是【正则扩大】的自证（★ r1048 收尾轮加）★★★**：★★★
// ★ 本轮把 `gocache.apply_to_environ(` 加进 `gocacheDedicatedRE`（★ 为了跟 B 级门
// ★ 说同一句话）⇒ ⇒ ★★ ⇒ **「正则扩大」天然有【假绿】的风险**（★ 把用户全局缓存
// ★ 顺手一起认了 ⇒ ⇒ 门就看不见真泄漏）⇒ ⇒ ★★★ ⇒ ⇒ **所以必须自带反向断言**：
// ★★★ ⇒ ⇒ ★★ **正向**：★ 两种 python 形态 + shell 形态都必须判「专用」；
// ★★★ ⇒ ⇒ ★★ **反向**：★ 指向【用户全局缓存】的两种写法必须**判不专用**。
//
// ★ ★★ **为什么放在这个文件里**：★ 这条自证量的就是【本门自己的正则】⇒ ⇒
// ★ ★★ 它必须与被量的判据同源 ⇒ ⇒ ★★ 放别处就变成「另一份量具」⇒ ⇒ ★★ ⇒ ★★
// ★ ★★ 而「两份量具」可能【口径不一致】⇒ ⇒ ★★ ⇒ ⇒ **⇒ 那样这条自证就是假绿**。
func TestGoCacheDedicatedFormsAreRecognized(t *testing.T) {
	// ── 正向：★ 这些形态都必须被认成「指向专用目录」──
	dedicated := []string{
		`gocache.apply_to_environ("mutate")`,
		`env = gocache.env("mutate")`,
		`export GOCACHE="$("$(git rev-parse --show-toplevel)/scripts/lib/gocache.sh" mutate)"`,
		`export GOCACHE="$(cd "$(dirname "$0")" && pwd)/.build-local/.gocache-script"`,
	}
	for _, s := range dedicated {
		if !gocacheDedicatedRE.MatchString(s) {
			t.Fatalf("★ ★★ 这条形态本门必须认成【指向专用目录】★★ ⇒ ⇒ 但它没被认 ⇒ ⇒ 本门的正则退化了：\n  %s", s)
		}
		if !gocacheSetRE.MatchString(s) {
			t.Fatalf("★ ★★ 这条形态本门必须认成【设置了 GOCACHE】★★ ⇒ ⇒ 但它没被认 ⇒ ⇒ 本门的正则退化了：\n  %s", s)
		}
	}

	// ── 反向：★ 指向【用户全局缓存】的写法必须【判不专用】──
	notDedicated := []string{
		`export GOCACHE="$HOME/.cache/go-build"`,
		`os.environ["GOCACHE"] = os.path.expanduser("~/.cache/go-build")`,
		`os.environ["GOCACHE"] = "/tmp/go-build-cache"`,
		// ★ ★★ **r1049 审计轮加的一条（★ 最重要的一条）**：★★ `gocache.dir_for(`
		// ★ ★★ **只算路径** ⇒ ⇒ ★★ 它**不建目录**、★★ **不查可写** ⇒ ⇒ ★★ ⇒
		// ★ ★★ 而 go 拿到一个**不可用**的 GOCACHE 会直接报
		// ★ ★★ `failed to initialize build cache` ⇒ ⇒ ★★ ⇒ **脚本直接坏掉**
		// ★ ★★ ⇒ ⇒ ★★ ⇒ 所以它**不算**「指向专用目录」⇒ ⇒ ★★ ⇒ 必须判红。
		`os.environ["GOCACHE"] = gocache.dir_for(gocache.repo_root(), "mutate")`,
	}
	for _, s := range notDedicated {
		if gocacheDedicatedRE.MatchString(s) {
			t.Fatalf("★ ★★ 这条形态必须【判不专用】★★（★ 它指向用户的全局缓存 ⇒ ⇒ ⇒ 门不能认）⇒ ⇒ 但本门把它认成专用 ⇒ ⇒ 【假绿】：\n  %s", s)
		}
	}
}
