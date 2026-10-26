// Package clockguard scans the repository's tests for clock-based assertions
// whose lower bound has **zero margin** against the very duration the test
// waits for.
//
// ★ 2026-10-21（R66 轮「全仓时钟判据普查」）的背景：
//
// A clock assertion that judges "it must NOT have been too fast" is only safe
// when the waited duration is **guaranteed by a timer or a context deadline**
// (those never fire early). It is **not** safe when the natural value is
// *derived from the same clock the assertion reads* — e.g. a follower's
// expiry watcher armed from a Redis **PTTL** read (millisecond resolution)
// versus an assertion of the form `elapsed < 200ms` where 200ms is that very
// TTL.
//
// Measured on admin/distlock (2026-10-21, 10 runs):
//
//	elapsed ∈ [200.03ms, 201.64ms]   ⇒  worst-case margin 0.03ms (0.015%)
//
// ⇒ any perturbation that makes the PTTL read land a sub-millisecond earlier
// (another Redis build, a different clock granularity, a different rounding
// direction on a slow machine) turns it into a **waiting false red**: the
// follower behaved correctly and the gate says it gave up too early.
//
// ⇒ This package makes that class **statically detectable**: if the lower
// bound of a clock assertion is the **same duration literal** the same test
// waits for, the margin is zero by construction.
//
// The full inventory (all clock assertions in the repo, banded by threshold
// and classified by shape) lives in
// docs/audit/2026-10-21-R66-全仓时钟判据普查.md.
package clockguard

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ZeroMarginLowerBound describes one clock lower bound whose threshold is the
// same duration literal the enclosing test waits for.
type ZeroMarginLowerBound struct {
	// File is the repository-relative path of the offending test file.
	File string
	// Line is the 1-based line of the comparison.
	Line int
	// Func is the enclosing test function's name.
	Func string
	// Variable is the clock variable being compared (e.g. "elapsed").
	Variable string
	// Bound is the parsed lower bound.
	Bound time.Duration
	// BoundText is the source text of the bound (e.g. "200*time.Millisecond").
	BoundText string
	// WaitKey is the key/argument the same duration literal was used for
	// inside the same test (e.g. "TTL", "time.Sleep").
	WaitKey string
}

// clockVariableNames are the identifiers treated as "a measured duration".
var clockVariableNames = map[string]bool{
	"elapsed": true, "elapsedTime": true, "dur": true, "duration": true,
	"latency": true, "took": true, "waited": true, "elapsedMs": true,
}

// waitKeyNames are the struct-field keys / call names whose duration argument
// is "the duration this test waits for".
var waitKeyNames = map[string]bool{
	"TTL": true, "Timeout": true, "Deadline": true, "Delay": true,
	"Duration": true, "Wait": true, "BaseDelay": true, "MaxDelay": true,
}

// waitCallNames are call expressions whose duration argument is a wait.
var waitCallNames = map[string]bool{
	"time.Sleep": true, "Sleep": true, "SleepWithCtx": true,
	"context.WithTimeout": true, "context.WithDeadline": true,
	"WithTimeout": true, "WithDeadline": true,
}

// FindZeroMarginLowerBounds scans every *_test.go file under root and returns
// every clock lower bound that equals a duration literal the same test waits
// for.
//
// Files under the guard's own package are skipped: they necessarily contain
// the pattern as fixtures.
func FindZeroMarginLowerBounds(root string, skipDir string) ([]ZeroMarginLowerBound, error) {
	var out []ZeroMarginLowerBound
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			if skipDir != "" && strings.Contains(path, skipDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		bounds, err := scanFile(root, path)
		if err != nil {
			return err
		}
		out = append(out, bounds...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DiscriminativeLowerBound describes one clock assertion that judges
// "it must NOT have been too fast" — i.e. `elapsed < <threshold>`.
//
// ★ 2026-10-21（R68 轮）新增：R66 轮把全仓 53 处时钟判据按「判别职责」分型后，
// 把「判别型下界」里除 distlock 外的 10 处登记为「已分析、未实测」—— 依据是
// **读实现推导**（定时器只晚不早 ⇒ 下界恒成立），不是**实测自然值**。
// 而 distlock 那处的教训恰恰是：**余量按构造接近零时，跑够多次也不会红**
// ⇒ 「包级全绿」证明不了余量 ⇒ 必须逐处量自然值。
//
// ⇒ 本函数提供那份「逐处清单」的**机器可读版本**：它是 ratchet 门的登记册
// 对账基准（新增站点未登记 ⇒ 红；登记了但站点消失 ⇒ 红）。
type DiscriminativeLowerBound struct {
	// File is the repository-relative path of the test file.
	File string
	// Line is the 1-based line of the comparison.
	Line int
	// Func is the enclosing test function's name.
	Func string
	// Variable is the clock variable being compared (e.g. "elapsed").
	Variable string
	// Bound is the parsed lower bound.
	Bound time.Duration
	// BoundText is the source text of the bound (e.g. "200*time.Millisecond").
	BoundText string
}

// FindDiscriminativeLowerBounds scans every *_test.go file under root and
// returns every clock lower bound (the "must not be too fast" shape),
// regardless of whether it coincides with a waited duration.
//
// Files under skipDir are skipped — the guard's own package necessarily
// contains the pattern as fixtures.
func FindDiscriminativeLowerBounds(root string, skipDir string) ([]DiscriminativeLowerBound, error) {
	var out []DiscriminativeLowerBound
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			if skipDir != "" && strings.Contains(path, skipDir) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		bounds, err := scanDiscriminativeFile(root, path)
		if err != nil {
			return err
		}
		out = append(out, bounds...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanDiscriminativeFile(root, path string) ([]DiscriminativeLowerBound, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}
	var out []DiscriminativeLowerBound
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		for _, b := range collectLowerBounds(fn) {
			out = append(out, DiscriminativeLowerBound{
				File:      rel,
				Line:      fset.Position(b.pos).Line,
				Func:      fn.Name.Name,
				Variable:  b.variable,
				Bound:     b.bound,
				BoundText: b.text,
			})
		}
	}
	return out, nil
}

func scanFile(root, path string) ([]ZeroMarginLowerBound, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	rel, relErr := filepath.Rel(root, path)
	if relErr != nil {
		rel = path
	}
	var out []ZeroMarginLowerBound
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		waits := collectWaitLiterals(fn)
		if len(waits) == 0 {
			continue
		}
		for _, b := range collectLowerBounds(fn) {
			for _, w := range waits {
				if w.value == b.bound {
					out = append(out, ZeroMarginLowerBound{
						File:      rel,
						Line:      fset.Position(b.pos).Line,
						Func:      fn.Name.Name,
						Variable:  b.variable,
						Bound:     b.bound,
						BoundText: b.text,
						WaitKey:   w.key,
					})
					break
				}
			}
		}
	}
	return out, nil
}

type waitLiteral struct {
	key   string
	value time.Duration
	pos   token.Pos
}

type lowerBound struct {
	variable string
	bound    time.Duration
	text     string
	pos      token.Pos
}

// collectWaitLiterals finds the duration literals a test waits for.
func collectWaitLiterals(fn *ast.FuncDecl) []waitLiteral {
	var out []waitLiteral
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			key, ok := node.Key.(*ast.Ident)
			if !ok || !waitKeyNames[key.Name] {
				return true
			}
			if d, ok := durationLiteral(node.Value); ok {
				out = append(out, waitLiteral{key: key.Name, value: d, pos: node.Pos()})
			}
		case *ast.CallExpr:
			name := callName(node.Fun)
			if name == "" || !waitCallNames[name] {
				return true
			}
			for _, a := range node.Args {
				if d, ok := durationLiteral(a); ok {
					out = append(out, waitLiteral{key: name, value: d, pos: node.Pos()})
					break
				}
			}
		}
		return true
	})
	return out
}

// collectLowerBounds finds clock comparisons of the form `elapsed < 200ms`.
func collectLowerBounds(fn *ast.FuncDecl) []lowerBound {
	var out []lowerBound
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		bin, ok := n.(*ast.BinaryExpr)
		if !ok {
			return true
		}
		if bin.Op != token.LSS && bin.Op != token.LEQ {
			return true
		}
		ident, ok := bin.X.(*ast.Ident)
		if !ok || !clockVariableNames[ident.Name] {
			return true
		}
		d, ok := durationLiteral(bin.Y)
		if !ok {
			return true
		}
		out = append(out, lowerBound{
			variable: ident.Name,
			bound:    d,
			text:     exprText(bin.Y),
			pos:      bin.Pos(),
		})
		return true
	})
	return out
}

// durationLiteral parses `200*time.Millisecond` (and the `time.Second` form).
func durationLiteral(e ast.Expr) (time.Duration, bool) {
	bin, ok := e.(*ast.BinaryExpr)
	if !ok || bin.Op != token.MUL {
		return 0, false
	}
	lit, ok := bin.X.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	n, err := strconv.ParseInt(lit.Value, 10, 64)
	if err != nil {
		return 0, false
	}
	unit, ok := timeUnit(bin.Y)
	if !ok {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

func timeUnit(e ast.Expr) (time.Duration, bool) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return 0, false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return 0, false
	}
	switch sel.Sel.Name {
	case "Millisecond":
		return time.Millisecond, true
	case "Second":
		return time.Second, true
	case "Minute":
		return time.Minute, true
	case "Microsecond":
		return time.Microsecond, true
	case "Nanosecond":
		return time.Nanosecond, true
	case "Hour":
		return time.Hour, true
	}
	return 0, false
}

func callName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		pkg, ok := f.X.(*ast.Ident)
		if !ok {
			return ""
		}
		return pkg.Name + "." + f.Sel.Name
	}
	return ""
}

// exprText renders an expression back to (approximate) source text.
func exprText(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BinaryExpr:
		return exprText(v.X) + " * " + exprText(v.Y)
	case *ast.BasicLit:
		return v.Value
	case *ast.SelectorExpr:
		return exprText(v.X) + "." + v.Sel.Name
	case *ast.Ident:
		return v.Name
	}
	return "?"
}
