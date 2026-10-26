package ursm_test

// sqlfmt_sessionfamily_gate_test.go — 2026-10-22（§145 轮）。
//
// # 生产事故（252，2026-10-21 17:34 部署 → 17:36 首错）
//
// discovery 的 stale 下架 UPDATE 把 SessionFamilyTurnsSourceSQL() 的投影拼进了
// fmt.Sprintf 的**格式串**。投影里有字面 `%`（`LIKE 'sys:%'`）⇒ Go fmt 把它
// 当成未知动词 `%'`：
//
//	LIKE 'sys:%!'(int=24)            ← %' 吃掉了下一个实参 graceHours
//	interval '%!d(MISSING) hours'    ← %d 无参可吃
//
// ⇒ 每条 UPDATE 必然 `syntax error at or near "("`（生产日志 10-21 100 条 +
// 10-22 半天 111 条），stale 下架功能自部署起全灭，而 Go 侧只记 Debug 日志。
//
// # 门（数据流版）
//
// ★ 变异 M1 的教训（2026-10-22）：修复把 SQL 组装搬进了纯函数
// staleExpiryOffersSQL 之后，若有人把**它的返回值**再包进 Sprintf，损坏照样
// 发生——「直接 helper 进格式串」的门对这种**包装形态**恒绿（盲区）。
// ⇒ 门必须做数据流传播：凡是函数体（含返回表达式）直接或间接含
// SessionFamily* helper 调用的，该函数进入「污染集」；Sprintf 的第一个实参
// （格式串）里出现污染集内任何函数的调用 ⇒ 红。
//
// 自证：
//	自证 1（阳性对照·直接形态）：夹具把 helper 直接拼进格式串 ⇒ 必须报出。
//	自证 2（阳性对照·包装形态）：夹具 A 函数返回 helper 拼接、B 处把 A()
//	                           拼进格式串 ⇒ 必须报出（M1 盲区的可执行证据）。
//	自证 3（扫了个寂寞）：仓内必须存在 helper 调用点，否则门在对空气扫描。

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var sessionFamilySQLHelpers = map[string]bool{
	"SessionFamilyTurnsSourceSQL":     true,
	"SessionFamilyTurnsHotSourceSQL":  true,
	"SessionFamilyTurnsForSessionSQL": true,
	"SessionFamilyBodiesSourceSQL":    true,
}

type goFile struct {
	path string
	// pkgDir: 包目录。污染集按包作用域，防止跨包同名函数误染（vendor 撞名实证）。
	pkgDir string
	fset   *token.FileSet
	file   *ast.File
	// helperCalls: 该文件里 helper 调用表达式（按指针身份匹配用）
	helperCalls map[ast.Expr]bool
	// helperCount: helper 调用个数
	helperCount int
}

// collectGoFiles walks root and parses every non-test .go file.
func collectGoFiles(root string) ([]*goFile, error) {
	var files []*goFile
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name == "node_modules" || name == ".git" || name == "web" || name == "web-mobile" || name == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		gf := &goFile{path: path, pkgDir: filepath.Dir(path), fset: fset, file: f, helperCalls: map[ast.Expr]bool{}}
		ast.Inspect(f, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			isHelper := false
			if id, ok := ce.Fun.(*ast.Ident); ok && sessionFamilySQLHelpers[id.Name] {
				isHelper = true
			}
			if se, ok := ce.Fun.(*ast.SelectorExpr); ok && sessionFamilySQLHelpers[se.Sel.Name] {
				isHelper = true
			}
			if isHelper {
				gf.helperCalls[ce] = true
				gf.helperCount++
			}
			return true
		})
		files = append(files, gf)
		return nil
	})
	return files, err
}

// exprCallsAny reports whether node contains a call to any name in interesting.
func exprCallsAny(node ast.Node, interesting map[string]bool, gf *goFile) bool {
	e := node
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if ce, ok := n.(*ast.CallExpr); ok {
			if gf.helperCalls[ce] {
				found = true
				return false
			}
			switch fun := ce.Fun.(type) {
			case *ast.Ident:
				if interesting[fun.Name] {
					found = true
				}
			case *ast.SelectorExpr:
				if interesting[fun.Sel.Name] {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// sqlTaintingFuncs returns, per package directory, the set of function names
// whose body directly or transitively embeds a SessionFamily helper call.
// ★ Scoping is per package dir: name-keyed taint must not leak across packages
// (a same-named function elsewhere would be a false positive — vendor 撞名实证
// 2026-10-22), and cross-package resolution would need import analysis.
func sqlTaintingFuncs(files []*goFile) map[string]map[string]bool {
	tainted := map[string]map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, gf := range files {
			pkg := tainted[gf.pkgDir]
			if pkg == nil {
				pkg = map[string]bool{}
				tainted[gf.pkgDir] = pkg
			}
			for _, decl := range gf.file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok || fd.Body == nil || pkg[fd.Name.Name] {
					continue
				}
				if exprCallsAny(fd.Body, pkg, gf) {
					pkg[fd.Name.Name] = true
					changed = true
				}
			}
		}
	}
	return tainted
}

// scanSprintfSessionFamily reports "file:line" for every fmt.Sprintf whose
// format-string argument (first arg) embeds a helper call, directly or via a
// SQL-tainting function.
func scanSprintfSessionFamily(root string) ([]string, error) {
	files, err := collectGoFiles(root)
	if err != nil {
		return nil, err
	}
	tainted := sqlTaintingFuncs(files)
	var hits []string
	for _, gf := range files {
		pkgTaint := tainted[gf.pkgDir]
		ast.Inspect(gf.file, func(n ast.Node) bool {
			ce, ok := n.(*ast.CallExpr)
			if !ok || len(ce.Args) == 0 {
				return true
			}
			isFmt := false
			if id, ok := ce.Fun.(*ast.Ident); ok && id.Name == "Sprintf" {
				isFmt = true
			}
			if se, ok := ce.Fun.(*ast.SelectorExpr); ok && se.Sel.Name == "Sprintf" {
				isFmt = true
			}
			if !isFmt {
				return true
			}
			if exprCallsAny(ce.Args[0], pkgTaint, gf) {
				pos := gf.fset.Position(ce.Pos())
				hits = append(hits, pos.Filename+":"+itoa2(pos.Line))
			}
			return true
		})
	}
	return hits, nil
}

func TestNoSessionFamilyHelperInsideFormatString(t *testing.T) {
	hits, err := scanSprintfSessionFamily("../..")
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	for _, h := range hits {
		t.Errorf("SQL（直接或经由组装函数）流进 fmt.Sprintf 格式串——投影里的字面 %% 会把语句损坏: %s", h)
	}

	// 自证 3：扫了个寂寞检查。本仓 helper 调用点 >40（admin/bg/domains 多处），
	// 若一个文件都没扫到，说明根路径错了、门在对空气扫描。
	files, err := collectGoFiles("../..")
	if err != nil {
		t.Fatalf("walk2: %v", err)
	}
	totalHelpers := 0
	for _, gf := range files {
		totalHelpers += gf.helperCount
	}
	if totalHelpers == 0 {
		t.Error("仓内扫不到任何 SessionFamily helper 调用点——根路径/规则错了，门在对空气扫描")
	}

	// 自证 1：阳性对照·直接形态。
	dir1 := t.TempDir()
	fixture1 := `package fixture

import (
	"fmt"

	db "github.com/kaixuan/llm-gateway-go/db"
)

func bad() {
	_ = fmt.Sprintf("SELECT 1 FROM "+db.SessionFamilyTurnsSourceSQL()+" LIMIT %d", 5)
}
`
	if werr := os.WriteFile(filepath.Join(dir1, "fixture.go"), []byte(fixture1), 0o644); werr != nil {
		t.Fatalf("write fixture1: %v", werr)
	}
	hits1, ferr := scanSprintfSessionFamily(dir1)
	if ferr != nil {
		t.Fatalf("fixture1 walk: %v", ferr)
	}
	if len(hits1) == 0 {
		t.Error("阳性对照（直接形态）未命中——扫描器对「helper 进格式串」已无牙（恒绿门）")
	}

	// 自证 2：阳性对照·包装形态（变异 M1 盲区的可执行证据）。
	// compose() 返回 helper 拼接 ⇒ 污染集应含 compose；
	// bad2() 把 compose() 拼进格式串 ⇒ 必须报出。
	dir2 := t.TempDir()
	fixture2 := `package fixture2

import (
	"fmt"

	db "github.com/kaixuan/llm-gateway-go/db"
)

func compose(n int) string {
	return "SELECT 1 FROM " + db.SessionFamilyTurnsSourceSQL() + " LIMIT " + itoa(n)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	d := n % 10
	return itoa(n/10) + string(rune('0'+d))
}

func bad2(n int) {
	_ = fmt.Sprintf(compose(n)+" OFFSET %d", 10)
}
`
	if werr := os.WriteFile(filepath.Join(dir2, "fixture.go"), []byte(fixture2), 0o644); werr != nil {
		t.Fatalf("write fixture2: %v", werr)
	}
	hits2, ferr := scanSprintfSessionFamily(dir2)
	if ferr != nil {
		t.Fatalf("fixture2 walk: %v", ferr)
	}
	if len(hits2) == 0 {
		t.Error("阳性对照（包装形态）未命中——「组装函数返回值进格式串」仍是盲区（M1 同族）")
	}
}

func itoa2(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}
