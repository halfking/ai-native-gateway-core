// Package routemount finds admin route-registration functions that are never called.
//
// # The shape it looks for
//
// This repo registers admin HTTP endpoints through small helper functions:
//
//	func (h *Handler) RegisterModelStatusRoutes(mux *http.ServeMux, adminWrap func(http.HandlerFunc) http.HandlerFunc) {
//	    mux.HandleFunc("/api/admin/model-status", adminWrap(h.handleModelStatus))
//	}
//
// The helper exists, the handler exists, the tests for the handler exist — and
// the endpoint still answers 404, because **nothing ever calls the helper**.
//
// # Why it matters (measured, not hypothetical)
//
// 2026-10-21, llm-gateway-go R72: a full-page / full-endpoint sweep against
// the local deployment (127.0.0.1:8782, build 2.5.8-d3b57b45-20261020-2792)
// found six admin endpoints answering 404. Two of them turned out to be
// silently unmounted registrations:
//
//   - /api/admin/model-status — the call site
//     `adminHandler.RegisterModelStatusRoutes(mux, wrapAdmin)` was introduced
//     by 0174ec0aa and then **deleted by a merge** (d2cbaf88b's main.go hunk
//     contains the line `- adminHandler.RegisterModelStatusRoutes(mux, wrapAdmin)`
//     and no matching addition). The function survived in admin/model_status.go,
//     the call site did not.
//
//   - /api/admin/feishubot/* — `registerFeishuRoutes` was removed wholesale by
//     b6f8a69cb (a build fix) and later re-added *without its call site* by
//     825d741ee (another build fix).
//
// The failure mode is invisible to the existing test suite: no test asserts
// "this endpoint exists", so nothing goes red. The endpoint simply 404s in
// production while its unit tests keep passing.
//
// # The rule this package enforces
//
// Any function that registers an HTTP route must be called from **non-test**
// code somewhere in the repository. A function that is only called from a
// _test.go file is reported too: a registration that only happens under `go
// test` is not mounted in production.
//
// The scanner is deliberately syntactic. It never reasons about the database,
// the runtime, or whether the handler would succeed — it only asks the one
// question whose answer is a fact about the source tree: *does anything call
// this?*
package routemount

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one route-registering function with no non-test call site.
type Finding struct {
	// Func is the bare function or method name, e.g. "RegisterModelStatusRoutes".
	Func string
	// Pkg is the package that defines it, e.g. "admin".
	Pkg string
	// Pos is the "file:line" of the definition.
	Pos string
	// Routes are the literal route paths the function registers.
	Routes []string
}

// DisabledMarker is the in-source marker that turns a finding into an
// explicit, reviewable decision.
//
// A registration function with no call site is a defect *unless* the source
// says out loud that the feature is deliberately not mounted. The marker is
// required to carry a reason, so the decision is written down next to the
// code instead of living in somebody's memory.
//
// Format (in a doc comment or a comment inside the function):
//
//	routemount: disabled — <why, and what restoring it would take>
const DisabledMarker = "routemount: disabled"

func (f Finding) String() string {
	return fmt.Sprintf("%s.%s (%s) registers %d route(s) but has no non-test call site: %s",
		f.Pkg, f.Func, f.Pos, len(f.Routes), strings.Join(f.Routes, ", "))
}

// skipDirs are directories that never contain gateway route registrations.
var skipDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"testdata":     true,
	".next":        true,
	"coverage":     true,
}

// Scan walks the Go sources under root and returns the route-registering
// functions that are never called from non-test code.
//
// The returned slice is sorted by Func so failures are deterministic.
//
// Functions that carry the DisabledMarker are not returned: they are an
// explicit decision, and they are listed separately by ScanWithDisabled so
// every run still shows them.
func Scan(root string) ([]Finding, error) {
	findings, _, err := ScanWithDisabled(root)
	return findings, err
}

// ScanWithDisabled is Scan plus the list of deliberately unmounted
// registration functions (those carrying the DisabledMarker).
func ScanWithDisabled(root string) ([]Finding, []Finding, error) {
	fset := token.NewFileSet()

	type candidate struct {
		pkg      string
		name     string
		pos      string
		routes   []string
		disabled bool
		reason   string
	}
	candidates := map[string]candidate{}
	// callee name -> called from non-test code?
	calledInProd := map[string]bool{}
	calledInTest := map[string]bool{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if path != root && (skipDirs[base] || strings.HasPrefix(base, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			// A file that does not parse is not this scanner's business;
			// skipping it keeps the gate from becoming a compile-error detector.
			return nil
		}
		isTest := strings.HasSuffix(path, "_test.go")
		rel, _ := filepath.Rel(root, path)

		// 1) collect call sites
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call.Fun)
			if name == "" {
				return true
			}
			if isTest {
				calledInTest[name] = true
			} else {
				calledInProd[name] = true
			}
			return true
		})

		// 2) collect route-registering functions.
		//    ★ 只收**非测试文件**里的函数：测试里的 httptest 夹具也用
		//      mux.HandleFunc 注册路由，但它们**不是生产接线**，把它们算进来
		//      会把门变成噪声源。
		if isTest {
			return nil
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			// main / init 由 Go 运行时调用，不存在「调用点」可言。
			if fd.Name.Name == "main" || fd.Name.Name == "init" {
				continue
			}
			routes := registeredRoutes(fd)
			if len(routes) == 0 {
				continue
			}
			key := fd.Name.Name
			if _, exists := candidates[key]; !exists {
				pos := fset.Position(fd.Pos())
				disabled, reason := disabledReason(fd)
				candidates[key] = candidate{
					pkg:      file.Name.Name,
					name:     fd.Name.Name,
					pos:      fmt.Sprintf("%s:%d", rel, pos.Line),
					routes:   routes,
					disabled: disabled,
					reason:   reason,
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}

	var findings []Finding
	var disabledList []Finding
	for key, c := range candidates {
		if calledInProd[key] {
			continue
		}
		f := Finding{
			Func:   c.name,
			Pkg:    c.pkg,
			Pos:    c.pos,
			Routes: c.routes,
		}
		if c.disabled {
			disabledList = append(disabledList, f)
			continue
		}
		// Only a test calls it ⇒ in production it is never mounted either.
		findings = append(findings, f)
	}
	sortFindings(findings)
	sortFindings(disabledList)
	return findings, disabledList, nil
}

func sortFindings(fs []Finding) {
	sort.Slice(fs, func(i, j int) bool {
		if fs[i].Pkg != fs[j].Pkg {
			return fs[i].Pkg < fs[j].Pkg
		}
		return fs[i].Func < fs[j].Func
	})
}

// disabledReason reports whether a function carries the DisabledMarker and
// returns the reason written next to it.
//
// A marker without a reason is treated as **absent**: "deliberately not
// mounted" is only a decision when the reason is written down.
func disabledReason(fd *ast.FuncDecl) (bool, string) {
	texts := []string{}
	if fd.Doc != nil {
		texts = append(texts, fd.Doc.Text())
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if cg, ok := n.(*ast.CommentGroup); ok {
			texts = append(texts, cg.Text())
		}
		return true
	})
	for _, t := range texts {
		idx := strings.Index(t, DisabledMarker)
		if idx < 0 {
			continue
		}
		reason := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(t[idx+len(DisabledMarker):]), "-—:："))
		if len(reason) < 8 {
			continue
		}
		return true, reason
	}
	return false, ""
}

// calleeName returns the bare name of the called function, or "" when the
// callee is not a plain identifier or selector (closures, function literals).
func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// registeredRoutes returns the literal route paths a function registers.
//
// Only literal string arguments are collected: a route built from a variable
// cannot be resolved statically, and guessing would produce false findings.
func registeredRoutes(fd *ast.FuncDecl) []string {
	var routes []string
	seen := map[string]bool{}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}
		if len(call.Args) == 0 {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		path := strings.Trim(lit.Value, `"`)
		if !strings.HasPrefix(path, "/") {
			return true
		}
		if !seen[path] {
			seen[path] = true
			routes = append(routes, path)
		}
		return true
	})
	sort.Strings(routes)
	return routes
}

// ModuleRoot returns the repository root for a package located at pkgDir.
func ModuleRoot(pkgDir string) (string, error) {
	dir, err := filepath.Abs(pkgDir)
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod found above %s", pkgDir)
		}
		dir = parent
	}
}
