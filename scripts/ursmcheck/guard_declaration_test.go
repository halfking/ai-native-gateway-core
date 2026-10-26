package ursm_test

// guard_declaration_test.go —— R92 轮（破坏性腿 · 守卫线 · 续六）：
// **闸门名单不变量**（G1~G6）。
//
// ★★★ 为什么要有这道门（R92 轮实测出来的形态转变）──────────────────────
//
//	前几轮把「守卫口径收口」表述成「让 N 个包逐个声明自己的闸门名单」
//	⇒⇒ 那是一份**手工清单**：不可证伪、随代码漂移、忘了没有任何东西会响。
//
//	R92 轮把它换成**可验证的不变量**。实测（R92 轮探针，全仓扫描）：
//	满足「导出 ∧ 包级 ∧ 非测试文件 ∧ 能中止 ∧ 与目标有关 ∧ **被别的包调用**」
//	的函数**全仓只有 3 个**：
//
//	  · internal/testdb.Create                     —— 已声明 ✓
//	  · internal/testguard.RequireIsolatedTarget   —— 已声明 ✓
//	  · internal/testguard.RequireDatabaseMatchesDSN —— **刻意不声明**（核对落点，见 G5）
//
//	⇒⇒ ⇒ **守卫口径其实早已收口完成**，只是从前没有一道门来保证它不倒退。
//	⇒⇒ ⇒ 本门的价值不在「今天红不红」，而在**将来新增跨包闸门时不能漏声明**。
//
// ── 六条不变量 ────────────────────────────────────────────────────────────
//
//	G1 不悬空：名单里的每个名字必须是本包**真实存在**的函数。
//	           （名单写了不存在的东西 ⇒ 跨包闸门解析会静默失效。）
//	G2 必导出：名单里的名字必须**导出** ⇒ 跨包才调得到。
//	           （未导出 ⇒ 声明它是死条目 ⇒ 让人以为有守卫。）
//	G3 能中止：闸门必须能**终止**这条腿（`t.Fatal*` / `t.Skip*`，
//	           含同包 helper 里的）⇒ 否则它只是「读了一下目标」。
//	G4 与目标有关：闸门自身**或同包传递可达**处必须有目标库判定证据。
//	           ★ **必须按同包可达算**：`RequireIsolatedTarget` 自身**没有**任何
//	           目标库判定（判定在同包 `IsIsolated` 里）⇒ 只看自身会把它误判成
//	           「不是闸门」⇒⇒ 「闸门把判定委托给同包 helper」是**通用形态**。
//	G5 禁核对落点：闸门不能只是「核对连接落在哪」——
//	           机械判别 = 体内有 `==`/`!=` 等值比较且**一侧是 `.Database` 选择器、
//	           另一侧是普通标识符**（一侧是字面量则不算）。
//	           ⇒ `RequireDatabaseMatchesDSN`（`got != want.Database`）= 核对落点
//	           ⇒ `IsIsolated`（`t.Database == ""`）= 判定（另一侧是字面量）⇒ 不误伤。
//	           ★ 闸门**只对被声明的名字生效** ⇒ 不会误伤 `mustLandInScratch`。
//	G6 完备性：凡「导出 ∧ 包级 ∧ 非测试文件 ∧ 能中止 ∧ 与目标有关 ∧ 被跨包调用」
//	           的函数，其所在包**必须**声明名单**并列出它**。
//	           ⇒★ 这一条把「手工清单」变成**自动不变量**：新增闸门忘声明 ⇒ 门立刻红。

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// r92GuardFn 一个函数的闸门候选属性（纯 AST 口径）。
type r92GuardFn struct {
	// exported：导出（首字母大写）⇒ 跨包可调用。
	exported bool
	// pkgLevel：包级函数（不是方法）⇒ 可作为闸门被声明。
	pkgLevel bool
	// nonTestFile：定义在**非** `_test.go` 文件里 ⇒ 跨包测试可 import。
	nonTestFile bool
	// canAbort：体内（或同包可达）能**中止**这条腿。
	canAbort bool
	// hasTarget：体内（或同包可达）有目标库判定证据。
	hasTarget bool
	// pureCheck：体内是「核对落点」形态（`.Database` vs 普通标识符的等值比较）。
	pureCheck bool
}

// r92AbortCall 体内是否有 `t.Fatal*` / `t.Skip*` 这类**中止**调用。
func r92AbortCall(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		name := sel.Sel.Name
		if strings.HasPrefix(name, "Fatal") || strings.HasPrefix(name, "Skip") {
			found = true
			return false
		}
		return true
	})
	return found
}

// r92IsPureCheck 体内是否出现「`.Database` 选择器 vs 普通标识符」的等值比较。
//
// ★ 一侧是**字面量**时不算（`t.Database == ""` 是判定，不是核对落点）。
func r92IsPureCheck(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		if bin.Op != token.EQL && bin.Op != token.NEQ {
			return true
		}
		isDBSel := func(e ast.Expr) bool {
			sel, ok := e.(*ast.SelectorExpr)
			return ok && sel.Sel.Name == "Database"
		}
		isPlainIdent := func(e ast.Expr) bool {
			switch v := e.(type) {
			case *ast.Ident:
				return true
			case *ast.SelectorExpr:
				return v.Sel.Name != "Database"
			}
			return false
		}
		if (isDBSel(bin.X) && isPlainIdent(bin.Y)) || (isDBSel(bin.Y) && isPlainIdent(bin.X)) {
			found = true
			return false
		}
		return true
	})
	return found
}

// r92ScanGuardFns 扫一个根目录，建「包目录 → 函数名 → 属性」索引。
func r92ScanGuardFns(t *testing.T, root string) map[string]map[string]r92GuardFn {
	t.Helper()
	out := map[string]map[string]r92GuardFn{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", ".idea", "dist", "build":
				return filepath.SkipDir
			}
			if d.Name() == "ursmcheck" && filepath.Base(filepath.Dir(path)) == "scripts" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, raw, parser.ParseComments)
		if perr != nil {
			return nil
		}
		dir := filepath.Dir(path)
		assigned := r87AssignedNodes(f)
		nonTest := !strings.HasSuffix(d.Name(), "_test.go")
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			m := out[dir]
			if m == nil {
				m = map[string]r92GuardFn{}
				out[dir] = m
			}
			cur := m[fd.Name.Name]
			cur.exported = cur.exported || ast.IsExported(fd.Name.Name)
			cur.pkgLevel = cur.pkgLevel || fd.Recv == nil
			cur.nonTestFile = cur.nonTestFile || nonTest
			cur.canAbort = cur.canAbort || r92AbortCall(fd.Body)
			cur.hasTarget = cur.hasTarget || len(r87CollectHits(fset, fd.Body, assigned)) > 0
			cur.pureCheck = cur.pureCheck || r92IsPureCheck(fd.Body)
			m[fd.Name.Name] = cur
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// r92GuardViolations 跑 G1~G6，返回违规描述（空 = 全部通过）。
//
// ★ `exported` 是事实源导出名集合（`r87BuildPkgFacts` 需要它做 DSN 名索引）；
//
//	对 G1~G6 不关键 ⇒ 夹具可以传空。
func r92GuardViolations(t *testing.T, root, modulePath string, exported map[string]bool) []string {
	t.Helper()
	pkgs, gidx := r87BuildPkgFacts(t, root, modulePath, exported)
	fns := r92ScanGuardFns(t, root)

	var violations []string
	rel := func(dir string) string {
		r, err := filepath.Rel(root, dir)
		if err != nil {
			return dir
		}
		return r
	}

	// ── G1~G5：对**已声明**的名单逐条检查 ──
	for dir, facts := range pkgs {
		if len(facts.declaredGuards) == 0 {
			continue
		}
		names := make([]string, 0, len(facts.declaredGuards))
		for n := range facts.declaredGuards {
			names = append(names, n)
		}
		sort.Strings(names)
		// 同包可达集（G3/G4 用）。
		reach := map[string]map[string]bool{}
		for name := range facts.pkgFuncs {
			reach[name] = r89ReachNames(name, facts.pkgFuncs)
		}
		for _, name := range names {
			fn, ok := fns[dir][name]
			if !ok {
				violations = append(violations, fmt.Sprintf(
					"G1 悬空：%s 的 GuardFunctions 声明了 %q，但本包里找不到这个函数 ⇒ 跨包闸门解析会静默失效",
					rel(dir), name))
				continue
			}
			if !fn.exported {
				violations = append(violations, fmt.Sprintf(
					"G2 未导出：%s 的 GuardFunctions 声明了 %q，但它未导出 ⇒ 跨包调不到 ⇒ 死条目",
					rel(dir), name))
			}
			// G3 能中止（自身或同包可达）。
			canAbort := fn.canAbort
			if !canAbort {
				for g := range reach[name] {
					if gf, ok := fns[dir][g]; ok && gf.canAbort {
						canAbort = true
						break
					}
				}
			}
			if !canAbort {
				violations = append(violations, fmt.Sprintf(
					"G3 不能中止：%s 的闸门 %q 体内（及同包可达处）没有 t.Fatal*/t.Skip* ⇒ 它只是「读了一下目标」，不能终止这条腿",
					rel(dir), name))
			}
			// G4 与目标有关（自身或同包可达）。
			hasTarget := fn.hasTarget
			if !hasTarget {
				for g := range reach[name] {
					if gf, ok := fns[dir][g]; ok && gf.hasTarget {
						hasTarget = true
						break
					}
				}
			}
			if !hasTarget {
				violations = append(violations, fmt.Sprintf(
					"G4 与目标无关：%s 的闸门 %q 体内（及同包可达处）没有目标库判定证据 ⇒ 它不决定「这条腿能不能跑」",
					rel(dir), name))
			}
			// G5 禁核对落点（只对**被声明的名字**生效）。
			if fn.pureCheck {
				violations = append(violations, fmt.Sprintf(
					"G5 只是核对落点：%s 把 %q 声明成闸门，但它只核对「连接落在哪」（`.Database` vs 标识符的等值比较）⇒ "+
						"DSN 指产品库时它照样通过 ⇒ 声明它 = 漏放",
					rel(dir), name))
			}
		}
	}

	// ── G6 完备性：凡「跨包闸门候选」必须被本包声明 ──
	type g6row struct {
		dir  string
		name string
	}
	var cands []g6row
	for dir, fnsDir := range fns {
		pkg := pkgs[dir]
		if pkg == nil {
			continue
		}
		reach := map[string]map[string]bool{}
		for name := range pkg.pkgFuncs {
			reach[name] = r89ReachNames(name, pkg.pkgFuncs)
		}
		for name, fn := range fnsDir {
			if !fn.exported || !fn.pkgLevel || !fn.nonTestFile || !fn.canAbort {
				continue
			}
			if fn.pureCheck {
				// 核对落点形态不是闸门 ⇒ 不要求声明（G5 的同一判据）。
				continue
			}
			hasTarget := fn.hasTarget
			if !hasTarget {
				for g := range reach[name] {
					if gf, ok := fnsDir[g]; ok && gf.hasTarget {
						hasTarget = true
						break
					}
				}
			}
			if !hasTarget {
				continue
			}
			node := gidx[r91Key(dir, name)]
			if node == nil || len(node.predsCross) == 0 {
				continue
			}
			cands = append(cands, g6row{dir: dir, name: name})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		if rel(cands[i].dir) != rel(cands[j].dir) {
			return rel(cands[i].dir) < rel(cands[j].dir)
		}
		return cands[i].name < cands[j].name
	})
	for _, c := range cands {
		pkg := pkgs[c.dir]
		if !pkg.declaredGuards[c.name] {
			violations = append(violations, fmt.Sprintf(
				"G6 漏声明：%s 的 %q 具备闸门语义（导出 ∧ 包级 ∧ 非测试文件 ∧ 能中止 ∧ 与目标有关）且**被别的包调用** ⇒ "+
					"它必须出现在本包的 GuardFunctions 名单里，否则跨包调用它的那条腿会被判成「无守卫」（漏放）",
				rel(c.dir), c.name))
		}
	}
	return violations
}

// TestGuardDeclarationInvariants 门本体：仓内必须零违规。
func TestGuardDeclarationInvariants(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}
	exported := r71ParseDatasourceExports(t, r86DSPath)
	modulePath := r87ModulePath(t, root)
	violations := r92GuardViolations(t, root, modulePath, exported)
	for _, v := range violations {
		t.Errorf("闸门名单不变量被破：%s", v)
	}
	if len(violations) == 0 {
		t.Logf("闸门名单不变量 G1~G6 全部通过（事实源导出 %d 个名字）", len(exported))
	}
}
