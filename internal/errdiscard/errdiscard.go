// Package errdiscard 检测「上抛打到了丢弃错误的调用方」这一缺陷形状（R68）。
//
// 背景：R66 给全仓 rows 循环加了迭代终检（return / warn / continue 三选一），
// 守卫 `internal/rowsguard` 只判「`X.Err()` 在不在循环旁边」——它**结构上
// 无力**发现分型是否选对。R67 的独立复审据此抓到 4 处真缺陷，全部是同一形状：
//
//	f(...) 改成 return nil, err   而   x, _ := f(...)
//
// 上抛的收益为零（调用方本来就不用 err），代价却是把「静默跳过一行」变成
// 「静默截断整体」，而且对调用方**完全不可见**。R68 用同一形状又找到 3 处
// 活着的同类（route_incidents / session_trend / usage_enhanced）。
//
// 本包把这一形状做成机械可查的门：**同一文件内**，若被调用函数在迭代终检
// 路径上会 `return ..., err`，而调用点用 `, _ :=` 丢弃该错误，即报出。
//
// 能力边界（必须写清楚，否则这个门会被误信）：
//   - 只看**同一个 Go 文件内**的调用——throwing 表按文件构建，不含包内
//     其他文件的同名方法；跨文件同包与跨包的 `, _ :=` 都不在覆盖内。
//     补全需要 go/packages 的完整类型信息，成本与误报率都高得多（12h
//     审计 P2-3 登记：旧文档自称「同包」，实为同文件）。
//   - 只认 `x.f(...)` 的 selector 调用形态；包级函数直呼 `f(...)` 不报。
//   - 报出的是**候选**，不是缺陷判决。每一条都需要人判断「调用方丢弃之后
//     结果被怎么用」：若调用方立刻走有文档的回退路径（例：work_types 的
//     canonical-only 回退），那是有意决策，本门会误报。
//   - 「是否在 rows.Err() 上返回」用 AST 判 if 条件，不靠文本窗口。
package errdiscard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding 是一条候选。
type Finding struct {
	File    string
	Line    int
	Callee  string
	Snippet string
}

// mustRel keeps a repo-relative path, falling back to the path itself when the
// walk hands us something outside root.
func mustRel(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

func (f Finding) String() string {
	return fmt.Sprintf("%s:%d calls %s discarding its error: %s", f.File, f.Line, f.Callee, f.Snippet)
}

// CheckDir 扫描一个目录下所有包的「同文件内 `, _ := f(...)`，且 f 会在
// rows.Err() 路径上 return err」。
//
// scanned 返回本次实际解析的非测试 .go 文件数。它存在的唯一目的是让**自检门**
// 能证明「门确实扫到了东西」——本门开发中出现过三次「0 findings 且全绿」，
// 其中一次正是目录遍历把整棵树跳过了，而当时的门对 0 条是沉默通过的。
func CheckDir(root string) ([]Finding, error) {
	f, _, err := CheckDirCounted(root)
	return f, err
}

// CheckDirCounted 是 CheckDir 的带计数版本。
func CheckDirCounted(root string) ([]Finding, int, error) {
	var out []Finding
	scanned := 0
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// 根目录本身必须放行。判据是「目录**名**」在排除表里，而不是
			// `strings.HasPrefix(name, ".")`——后者会把传入的根（常见是
			// `../..`，其 Name() 就是 ".."）整棵树 SkipDir 掉，门于是
			// **静默扫过 0 个文件并返回 0 条**。第一版就是这么写的，
			// 且它在正控（把已知缺陷改回去）下也不报，逼我逐层拆到
			// Walk 才知道。
			if path != root {
				n := info.Name()
				if n == "." || n == ".." {
					return nil
				}
				if strings.HasPrefix(n, ".") || n == "node_modules" || n == "vendor" ||
					n == "web" || n == "docs" || n == "testdata" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		scanned++
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, src, 0)
		if perr != nil {
			return nil
		}
		// Forward slashes, to match knownCandidates in the test. filepath.Rel
		// hands back OS-native separators, so on Windows the ratchet matched
		// nothing in either direction: every known candidate looked "fixed"
		// and every finding looked "new" — in the same run.
		rel := filepath.ToSlash(mustRel(root, path))
		lines := strings.Split(string(src), "\n")

		// 1) 本文件内哪些函数会在迭代终检上返回 error（throwing 表按
		// 文件构建：包内其他文件的同名方法认不到，见包文档能力边界）
		throwing := map[string]bool{}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fnReturnsOnErrCheck(fn) {
				throwing[fn.Name.Name] = true
			}
		}
		if len(throwing) == 0 {
			return nil
		}

		// 2) 谁用 `, _ :=` 调了它们
		for _, d := range f.Decls {
			ast.Inspect(d, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || len(as.Lhs) < 2 || len(as.Rhs) == 0 {
					return true
				}
				call, ok := as.Rhs[0].(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				recv, ok := sel.X.(*ast.Ident)
				if !ok || !throwing[sel.Sel.Name] {
					return true
				}
				// 「丢弃」出现在**左边**：`x, _ := f()` 是 1 个 RHS / 2 个 LHS，
				// `_` 在 LHS[1]。第一版去扫 RHS 里的 `_`（那是 `_, err = f()`
				// 这种多返回值解构的形态），于是本仓库主流的 `a, _ := f(...)`
				// 一个都认不出来，门静默返回 0 条。
				// 两种形态都要认：LHS 上的 `_` 与 RHS 上的 `_`。
				discards := false
				for _, lhs := range as.Lhs {
					if id, ok := lhs.(*ast.Ident); ok && id.Name == "_" {
						discards = true
						break
					}
				}
				if !discards {
					for _, rhs := range as.Rhs {
						if id, ok := rhs.(*ast.Ident); ok && id.Name == "_" {
							discards = true
							break
						}
					}
				}
				if !discards {
					return true
				}
				pos := fset.Position(as.Pos())
				snippet := ""
				if pos.Line-1 < len(lines) {
					snippet = strings.TrimSpace(lines[pos.Line-1])
				}
				out = append(out, Finding{
					File: rel, Line: pos.Line,
					Callee: recv.Name + "." + sel.Sel.Name, Snippet: snippet,
				})
				return true
			})
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, scanned, err
}

// fnReturnsOnErrCheck 判断函数里是否有「因迭代终检而 return err」的分支。
func fnReturnsOnErrCheck(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn, func(n ast.Node) bool {
		if found {
			return false
		}
		ifs, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		// 终检有两种写法，位置不同：
		//   A) `if err := rows.Err(); err != nil { return ... }` —— 调用在
		//      IfStmt.**Init**（AssignStmt），Cond 只是 `err != nil`；
		//   B) `if rows.Err() != nil { return ... }` —— 调用在 Cond。
		// 第一版只查 Cond，于是 A 形态（仓库里的主流写法）一个都认不出来，
		// 门静默返回 0 条 —— 又一次「判据锚错形状 ⇒ 恒绿」。
		if !stmtMentionsErrCheck(ifs.Init) && !mentionsErrCheck(ifs.Cond) {
			return true
		}
		if returnCarriesErr(ifs.Body) {
			found = true
		}
		return true
	})
	return found
}

// stmtMentionsErrCheck 用于 IfStmt.Init（它是 Stmt 不是 Expr）。
func stmtMentionsErrCheck(s ast.Stmt) bool {
	if s == nil {
		return false
	}
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Err" {
			found = true
		}
		return true
	})
	return found
}

// mentionsErrCheck 判一个表达式里是否出现 `X.Err()` / `dbrows.Err(X)`。
func mentionsErrCheck(e ast.Expr) bool {
	if e == nil {
		return false
	}
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "Err" {
			found = true
		}
		return true
	})
	return found
}

// returnCarriesErr 判断块内是否有 `return ..., err` 形态的返回。
func returnCarriesErr(b *ast.BlockStmt) bool {
	found := false
	ast.Inspect(b, func(n ast.Node) bool {
		r, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, res := range r.Results {
			if isErrIdent(res) {
				found = true
			}
		}
		return true
	})
	return found
}

func isErrIdent(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "err"
	case *ast.UnaryExpr:
		return isErrIdent(v.X)
	case *ast.CallExpr:
		// fmt.Errorf("...: %w", err) 之类
		for _, a := range v.Args {
			if isErrIdent(a) {
				return true
			}
		}
	}
	return false
}
