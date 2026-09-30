// Package rowsguard 是 R66 新增的**站位级**（site-level）rows 迭代守卫。
//
// 为什么需要它：R65 的接线守卫是「文件级 contains」——它能防住整个文件
// 退回吞错形态，但**防不住多站点文件里删掉其中一处**。R65 自己的记录
// 写明：同一文件多个站点时删一处，守卫不会红；那不是守卫失效，是守卫的
// 粒度契约。R65 靠「变异验证红/绿」只证明了「删唯一引用位点会红」，而
// 单站点文件恰好都是唯一引用位点。
//
// 本守卫把判据从「文件里有没有出现某个 helper 名」换成「**每一个
// `for X.Next()` 循环**是否都有迭代终检或跳行留痕」，逐循环独立判定。
// 删掉任意一处的终检都会独立报红，且报出精确 file:line。
//
// 判据形态（注意：判的是**产物特征**，不是「某字符串在不在」）：
//   - 循环体内、或同一函数内循环之后，必须出现 `X.Err()`（迭代终检），或
//   - 该循环被显式列入豁免表并写明理由。
//
// **跳行留痕（warnRowSkip / dbrows.SkipOrFail / slog.Warn）刻意不作为
// 判据**。第一版的包注释声称「留痕即可」，实现却只认 `X.Err()`——
// 文档与实现不一致本身就是缺陷。R66 复核时明确了取舍：**留痕解决的是
// 「单行坏数据」，终检解决的是「迭代中断」，两者不是同一件事**。
// 一个只有 warnRowSkip、没有 rows.Err() 的循环，在连接断开时仍然静默
// 截断——那正是本守卫要防的缺陷。因此判据取二者中更严的那个：终检。
// 留痕由代码评审保证，不进静态门。
//
// 「豁免表必须逐条写理由」是刻意的：豁免的本质是让某些站点不再受检，
// 任何白名单改动都必须能回答「为什么这一处可以不查」。
package rowsguard

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
)

// site 表示一个被检查的 `for X.Next()` 循环。
type site struct {
	file string
	line int
	fn   string
	recv string
}

// repoRoot 定位模块根：守卫的测试工作目录是包目录，向上找 go.mod。
func repoRoot(t testingT) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("go.mod not found walking up from cwd")
	return ""
}

// testingT 是 *testing.T 的最小子集，便于本包不依赖 testing 也能被单测。
type testingT interface {
	Fatalf(format string, args ...any)
	Errorf(format string, args ...any)
}

// skipDirs 是不在守卫范围内的目录。
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "web": true,
	"docs": true, ".venv": true, "testdata": true, ".codegraph": true,
}

// nextLoopRe 定位 `for X.Next() {` 与 `for X.Next(ctx) {` 两种形态。
//
// 允许带参是有意的：go-redis 的 `Scan(...).Iterator()` 走的正是
// `Next(ctx)`，它同样有 `Err()` 终检语义。它不是 pgx.Rows，但判据
// 「<recv>.Err() 是否在窗口内」对两者都成立，故不必按类型分流。
//
// 反面教训（已钉进 selfcheck）：本守卫第一版用 `Next\(\)` 精确匹配，
// 交叉复核立刻抓到 go-redis 的 `Next(ctx)` 形态被判据漏掉——
// **判据锚得过窄时，门对真实存在的站点是瞎的，而它自己不会报错**。
var nextLoopRe = regexp.MustCompile(`for\s+(\w+)\.Next\(`)

// collectSites 走遍仓库，返回所有 `for X.Next()` 站点。
func collectSites(root string) ([]site, error) {
	var out []site
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// 语法错误由 go build 负责；守卫不重复报。
			return nil
		}
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		lines := strings.Split(string(src), "\n")
		for _, decl := range f.Decls {
			ast.Inspect(decl, func(n ast.Node) bool {
				// `for rows.Next() { ... }` 是 **ForStmt**（条件循环），
				// 不是 RangeStmt——RangeStmt 只对应 `for k := range x`。
				// 本守卫第一版错断言成 RangeStmt，于是 collectSites 恒返回
				// 0 个站点、整门恒绿且不报错：这是「假绿」的第三形态
				// （判据锚在错误的 AST 节点上，门退化成空集合断言）。
				loop, ok := n.(*ast.ForStmt)
				if !ok || loop.Cond == nil {
					return true
				}
				// Cond 形如 rows.Next()：CallExpr → Fun 是 SelectorExpr。
				call, ok := loop.Cond.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Next" || sel.X == nil {
					return true
				}
				recv := ""
				if id, ok := sel.X.(*ast.Ident); ok {
					recv = id.Name
				}
				out = append(out, site{
					file: rel,
					line: fset.Position(loop.Pos()).Line,
					fn:   enclosingFunc(f, loop.Pos()),
					recv: recv,
				})
				return true
			})
		}
		_ = lines
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].file != out[j].file {
			return out[i].file < out[j].file
		}
		return out[i].line < out[j].line
	})
	return out, err
}

// enclosingFunc 找到包裹该节点的顶层函数名，用于报出可定位的上下文。
func enclosingFunc(f *ast.File, pos token.Pos) string {
	name := "<file scope>"
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Pos() <= pos && pos < fn.End() {
			name = fn.Name.Name
		}
	}
	return name
}

// enclosingFuncEnd 返回包裹该节点的顶层函数的结束位置；不在函数内时返回
// 该位置本身（从而跨函数的 Err() 不会为循环背书）。
func enclosingFuncEnd(f *ast.File, pos token.Pos) token.Pos {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Pos() <= pos && pos < fn.End() {
			return fn.End()
		}
	}
	return pos
}

// CheckAll 逐站点判定并回报缺失清单。返回 (checked, violations)。
//
// 判据打在 **AST** 上而不是文本窗口上。第一版用「循环行起 10 行文本窗口
// 内是否出现 `<recv>.Err()`」——它对 `return devices, rows.Err()` 这种
// 惯用收尾会漏判（Scan 多行时 rows.Err() 落在窗口外），把已正确处理的
// 站点误报成违规。窗口宽度无论怎么调都是错的：短了漏报、长了把下一个
// 循环的终检算给当前循环。
//
// 现行判据：对每个 `for <recv>.Next()` 循环，在**同一文件**内查找所有
// `<recv>.Err()` 调用点；只要存在至少一个位于该循环之后（按位置比较）
// 的调用，或位于循环体内部，即视为已守卫。
//
// 这个判据的边界要说清楚：它判的是「该 receiver 的迭代错误有没有被
// 消费」，**不判语义对错**——`if err := rows.Err(); err != nil { _ = err }`
// 也能通过。它防的是「完全没人看 Err()」这一类静默截断，这正是本轮
// 要收的缺陷；语义分类（五种分型）由代码评审与变异验证承担。
func CheckAll(root string, exemptions map[string]string) (int, []string, error) {
	fset, files, err := collectAST(root)
	if err != nil {
		return 0, nil, err
	}
	var violations []string
	checked := 0
	// 按文件分组，站点与 Err() 调用点共用同一 fset，位置可直接比较。
	byFile := map[string][]*ast.ForStmt{}
	for path, f := range files {
		rel, _ := filepath.Rel(root, path)
		for _, loop := range forNextLoops(f) {
			byFile[rel] = append(byFile[rel], loop)
		}
	}
	_ = fset
	names := make([]string, 0, len(byFile))
	for rel := range byFile {
		names = append(names, rel)
	}
	sort.Strings(names)
	for _, rel := range names {
		path := filepath.Join(root, rel)
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return checked, violations, rerr
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			continue
		}
		errCalls := errCallPositions(f)
		loops := forNextLoops(f)
		for _, loop := range loops {
			recv := loopReceiver(loop)
			pos := fset.Position(loop.Pos()).Line
			key := fmt.Sprintf("%s:%d", rel, pos)
			if _, ok := exemptions[key]; ok {
				continue
			}
			checked++
			if hasTerminalCheck(loop, errCalls[recv], enclosingFuncEnd(f, loop.Pos())) {
				continue
			}
			violations = append(violations, fmt.Sprintf(
				"%s (%s, recv=%s): no %s.Err() terminal check anywhere in the function after the loop",
				key, enclosingFunc(f, loop.Pos()), recv, recv))
		}
	}
	return checked, violations, nil
}

// forNextLoops 返回文件中所有 `for X.Next()` 条件循环。
func forNextLoops(f *ast.File) []*ast.ForStmt {
	var out []*ast.ForStmt
	for _, decl := range f.Decls {
		out = append(out, forNextLoopsIn(decl)...)
	}
	return out
}

// forNextLoopsIn 在单个声明（通常是 *ast.FuncDecl）内查找 Next() 循环。
func forNextLoopsIn(decl ast.Decl) []*ast.ForStmt {
	var out []*ast.ForStmt
	{
		ast.Inspect(decl, func(n ast.Node) bool {
			loop, ok := n.(*ast.ForStmt)
			if !ok || loop.Cond == nil {
				return true
			}
			if recv := loopReceiver(loop); recv != "" {
				out = append(out, loop)
			}
			return true
		})
	}
	return out
}

// loopReceiver 返回 `for <recv>.Next()` 里的 recv 名；非该形态返回 ""。
func loopReceiver(loop *ast.ForStmt) string {
	call, ok := loop.Cond.(*ast.CallExpr)
	if !ok {
		return ""
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Next" {
		return ""
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return id.Name
}

// errCallPositions 收集每个 receiver 的迭代终检调用位置。
//
// 两种形态都要认：
//   - `rows.Err()` —— 直接终检；
//   - `dbrows.Err(rows)` —— 走 internal/dbrows 共享 helper（该 helper 内部
//     就是 `rows.Err()` 加 nil 保护）。
//
// 只认第一种是过严的：它会把已经正确处理的站点判成违规，从而把后续作者
// 推回「别用共享 helper、直接裸写 rows.Err()」——**门不该奖励绕过它的
// 正确写法**。这两种形态的语义等价，都接受。
func errCallPositions(f *ast.File) map[string][]token.Pos {
	out := map[string][]token.Pos{}
	for _, decl := range f.Decls {
		ast.Inspect(decl, func(n ast.Node) bool {
			// 形态一：recv.Err()
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Err" {
				if id, ok := sel.X.(*ast.Ident); ok {
					out[id.Name] = append(out[id.Name], sel.Pos())
				}
				return true
			}
			// 形态二：dbrows.Err(rows)
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Err" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "dbrows" || len(call.Args) != 1 {
				return true
			}
			if arg, ok := call.Args[0].(*ast.Ident); ok {
				out[arg.Name] = append(out[arg.Name], call.Pos())
			}
			return true
		})
	}
	return out
}

// hasTerminalCheck 判断该循环的迭代错误是否真被消费。
//
// 判据边界必须说清楚（这正是本守卫第二版的修正点）：
// 第一版写的是 `e > loop.End() && e < loop.Body.End()+100000`——那个
// `+100000` 让「循环之后」实际退化成「**本文件里该行之后的任何位置**」。
// 于是同文件里另一个函数中的 `rows.Err()`（变量名恰好也叫 rows）可以
// 为一个真正没被守卫的循环背书，**门会假绿**。这与本仓既有教训同族：
// 判据的边界比判据本身更容易出错。
//
// 现行判据把搜索范围收敛到**包裹该循环的函数**内：
//   - 循环体内部出现 recv.Err()，或
//   - 同一函数体内、循环结束之后出现 recv.Err()。
//
// 跨函数不再互相背书。
//
// 注意它仍只判「有没有被消费」，不判语义对错——
// `if err := rows.Err(); err != nil { _ = err }` 也能通过。语义分型
// 由代码评审与变异验证承担，不由静态门承担。
func hasTerminalCheck(loop *ast.ForStmt, errs []token.Pos, fnEnd token.Pos) bool {
	for _, e := range errs {
		// 循环体内部
		if e > loop.Body.Pos() && e < loop.Body.End() {
			return true
		}
		// 同一函数内、循环之后
		if e > loop.End() && e < fnEnd {
			return true
		}
	}
	return false
}

// collectAST 解析仓库全部非测试 Go 文件。
func collectAST(root string) (*token.FileSet, map[string]*ast.File, error) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		files[path] = f
		return nil
	})
	return fset, files, err
}

// readLines 读源文件行切片，供自检门做文本交叉复核。
func readLines(t testingT, root, rel string) []string {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return strings.Split(string(b), "\n")
}
