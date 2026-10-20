// Package stopguard finds Stop() methods that can block forever.
//
// # The shape it looks for
//
// Background workers in this repo follow one shape: Start() launches a
// goroutine that eventually closes a `done` channel, and Stop() cancels the
// worker and waits on that channel:
//
//	func (w *Worker) Stop() {
//	    if w.cancel != nil { w.cancel() }
//	    <-w.done
//	}
//
// That shape is only safe when Start() actually ran. When a worker is
// *constructed* but never started — e.g. a legacy worker skipped by a
// feature flag — `done` is never closed and Stop() blocks forever.
//
// # Why it matters (measured, not hypothetical)
//
// 2026-10-21, llm-gateway-go R71: the gateway's graceful shutdown hung for
// 97 minutes on exactly this shape. Evidence chain (SIGQUIT stack dumps of
// two zombie gateway processes on the same binary family):
//
//	goroutine 1 [sync.WaitGroup.Wait, 97 minutes]:
//	    db.(*DB).Close()            db/db.go:4996
//	    main.main.func3()           cmd/gateway/main.go:579   (deferred db.Close)
//	    main.main()                 cmd/gateway/main.go:8383
//
//	goroutine 5631 [chan receive, 97 minutes]:
//	    bg.(*PassiveProbeListener).Stop()   bg/passive_probe_listener.go:74
//	    main.main.func72()                  cmd/gateway/main.go:8264
//
// The shutdown sequence is one goroutine, so that single blocking Stop()
// prevented every *later* Stop() from running — including
// `autoRouteListener.Stop()`, which owns the long-lived LISTEN connection.
// That connection was therefore never returned to the pool, so the deferred
// `db.Close()` blocked forever inside `pgxpool.Close()` and main never
// returned: the process stayed alive as a zombie holding every Redis
// connection it had opened. The trigger was the default probe mode:
// cmd/gateway/main.go constructs the legacy passive-probe listener and then
// skips `Start()` when LLM_GATEWAY_USE_NEW_PROBE_MODE is true (the default
// since 2026-07-14).
//
// # The rule this package enforces
//
// A terminal channel wait inside a Stop() method is safe only when it is
// **unreachable unless the worker actually started**. The scanner proves
// that syntactically:
//
//   - the wait sits inside a region that is only entered when the worker
//     has started (e.g. `if w.started { <-w.done }`), or
//   - an earlier `if` in the same block returns early on exactly the
//     "never started" branch (e.g. `if !w.started.Load() { return }`,
//     `if w.cancel == nil { return }`,
//     `if w.cancel != nil { w.cancel() } else { return }`), or
//   - the wait is one alternative of a `select` (never a terminal wait).
//
// Anything else is reported. The scanner is deliberately syntactic: it
// understands only the liveness signals this repo uses (`started`, `cancel`,
// `done`, `stop`) and never reasons about the database or the runtime.
package stopguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Finding is one unguarded terminal wait inside a Stop() method.
type Finding struct {
	// File is the path relative to the scanned root.
	File string
	// Line is the 1-based line of the wait statement.
	Line int
	// Type is the receiver's type name.
	Type string
	// Wait is the field name being waited on.
	Wait string
	// Reason explains, in one line, why the wait is considered unguarded.
	Reason string
}

func (f Finding) String() string {
	return f.File + ":" + itoa(f.Line) + ": " + f.Type + ".Stop() waits on " + f.Wait + " — " + f.Reason
}

// livenessFields are the receiver field names this repo uses to express
// "has this worker started?".
var livenessFields = map[string]bool{
	"started":    true,
	"cancel":     true,
	"done":       true,
	"stop":       true,
	"stopCh":     true,
	"workerDone": true,
	"stopped":    true,
}

// ScanDir walks dir and reports every unguarded terminal wait inside a
// Stop() method of a pointer receiver. Test files and vendor are skipped.
func ScanDir(dir string) ([]Finding, error) {
	var out []Finding
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := filepath.Base(path)
			if base == "vendor" || base == ".git" || base == "node_modules" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			out = append(out, scanStopMethod(fset, rel, fd)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

// scanStopMethod returns the findings for one Stop() method (empty for any
// other method).
func scanStopMethod(fset *token.FileSet, rel string, fd *ast.FuncDecl) []Finding {
	if fd.Name == nil || fd.Name.Name != "Stop" {
		return nil
	}
	if fd.Recv == nil || len(fd.Recv.List) != 1 {
		return nil
	}
	star, ok := fd.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return nil
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return nil
	}
	sc := &stopScanner{fset: fset, rel: rel, typeName: ident.Name}
	sc.collectAliases(fd.Body)
	sc.walkBlock(fd.Body, false)
	return sc.findings
}

type stopScanner struct {
	fset     *token.FileSet
	rel      string
	typeName string
	findings []Finding
	// aliases maps a local variable that captured a liveness field
	// (`started := w.started`) onto the field it captured.
	aliases map[string]string
}

// collectAliases records local variables that snapshot a liveness field.
//
// The hardened workers in this repo commonly write:
//
//	started := w.started
//	...
//	if started { <-w.done }
//
// The local name must be understood as the field it copied, otherwise the
// guard looks like an unrelated condition.
func (s *stopScanner) collectAliases(blk *ast.BlockStmt) {
	s.aliases = map[string]string{}
	ast.Inspect(blk, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		if name := s.livenessName(assign.Rhs[0]); name != "" {
			s.aliases[lhs.Name] = name
		}
		return true
	})
}

// walkBlock walks a statement list. startedOK means "everything reachable
// from here implies the worker has started".
func (s *stopScanner) walkBlock(blk *ast.BlockStmt, startedOK bool) {
	if blk == nil {
		return
	}
	// guardOK tracks whether an earlier statement in this same block
	// returns early on the "worker never started" branch.
	guardOK := false
	for _, stmt := range blk.List {
		if ifStmt, ok := stmt.(*ast.IfStmt); ok {
			if s.ifProvidesStartGuard(ifStmt) {
				guardOK = true
			}
		}
		s.walkStmt(stmt, startedOK || guardOK)
	}
}

func (s *stopScanner) walkStmt(stmt ast.Stmt, startedOK bool) {
	switch st := stmt.(type) {
	case *ast.BlockStmt:
		s.walkBlock(st, startedOK)
	case *ast.IfStmt:
		// The body is entered when cond is TRUE; the else branch when it
		// is FALSE. Each side only implies "started" if the condition does.
		s.walkBlock(st.Body, startedOK || s.trueImpliesStarted(st.Cond))
		if st.Else != nil {
			switch e := st.Else.(type) {
			case *ast.BlockStmt:
				s.walkBlock(e, startedOK || s.falseImpliesStarted(st.Cond))
			case *ast.IfStmt:
				s.walkStmt(e, startedOK)
			}
		}
	case *ast.SelectStmt:
		// Inside a select, a receive is one alternative among several, so
		// it is never a terminal wait.
		for _, comm := range st.Body.List {
			cc, ok := comm.(*ast.CommClause)
			if !ok {
				continue
			}
			for _, cstmt := range cc.Body {
				s.walkStmt(cstmt, true)
			}
		}
	case *ast.ForStmt:
		s.walkBlock(st.Body, startedOK)
	case *ast.RangeStmt:
		s.walkBlock(st.Body, startedOK)
	case *ast.SwitchStmt:
		s.walkBlock(st.Body, startedOK)
	case *ast.TypeSwitchStmt:
		s.walkBlock(st.Body, startedOK)
	case *ast.LabeledStmt:
		s.walkStmt(st.Stmt, startedOK)
	case *ast.ExprStmt:
		s.checkWait(st, startedOK)
	case *ast.GoStmt:
		if call, ok := st.Call.Fun.(*ast.FuncLit); ok {
			s.walkBlock(call.Body, startedOK)
		}
	case *ast.DeferStmt:
		if call, ok := st.Call.Fun.(*ast.FuncLit); ok {
			s.walkBlock(call.Body, startedOK)
		}
	}
}

// checkWait inspects one expression statement for a terminal channel wait.
func (s *stopScanner) checkWait(st *ast.ExprStmt, startedOK bool) {
	arrow, ok := st.X.(*ast.UnaryExpr)
	if !ok || arrow.Op != token.ARROW {
		return
	}
	sel, ok := arrow.X.(*ast.SelectorExpr)
	if !ok {
		return
	}
	name := sel.Sel.Name
	if !isDoneish(name) {
		return
	}
	if startedOK {
		return
	}
	pos := s.fset.Position(st.Pos())
	s.findings = append(s.findings, Finding{
		File:   s.rel,
		Line:   pos.Line,
		Type:   s.typeName,
		Wait:   name,
		Reason: "no started/cancel guard: a worker that was constructed but never started leaves this wait blocking forever",
	})
}

func isDoneish(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), "done")
}

// ifProvidesStartGuard reports whether the if statement returns early on
// exactly the branch where the worker has NOT started.
//
// Accepted shapes:
//
//	if !w.started.Load() { return }            // cond true  ⇒ not started
//	if w.cancel == nil { return }              // cond true  ⇒ not started
//	if w.cancel != nil { w.cancel() } else { return }
//
// The signal must be about startedness — a `return` gated on an unrelated
// condition does not make the wait safe.
func (s *stopScanner) ifProvidesStartGuard(ifStmt *ast.IfStmt) bool {
	if endsWithReturn(ifStmt.Body) && s.falseImpliesStarted(ifStmt.Cond) {
		return true
	}
	if ifStmt.Else != nil {
		if blk, ok := ifStmt.Else.(*ast.BlockStmt); ok && endsWithReturn(blk) && s.trueImpliesStarted(ifStmt.Cond) {
			return true
		}
	}
	return false
}

// trueImpliesStarted reports: when the condition is TRUE, the worker has
// started.
func (s *stopScanner) trueImpliesStarted(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			return s.falseImpliesStarted(x.X)
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.NEQ:
			// `w.cancel != nil` ⇒ a cancel function exists ⇒ started.
			return isNilComparison(x) && s.livenessName(x) != "started"
		case token.EQL:
			// `w.started == true` ⇒ started.
			if id, ok := x.Y.(*ast.Ident); ok && id.Name == "true" {
				return s.livenessName(x) == "started"
			}
		case token.LAND:
			return s.trueImpliesStarted(x.X) || s.trueImpliesStarted(x.Y)
		}
	}
	if name := s.livenessName(e); name == "started" {
		return true
	}
	return false
}

// falseImpliesStarted reports: when the condition is FALSE, the worker has
// started.
func (s *stopScanner) falseImpliesStarted(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.UnaryExpr:
		if x.Op == token.NOT {
			return s.trueImpliesStarted(x.X)
		}
	case *ast.BinaryExpr:
		switch x.Op {
		case token.EQL:
			// `w.cancel == nil` is false ⇒ cancel exists ⇒ started.
			return isNilComparison(x) && s.livenessName(x) != "started"
		case token.LOR:
			// false ⇒ both sides false; one side is enough.
			return s.falseImpliesStarted(x.X) || s.falseImpliesStarted(x.Y)
		case token.LAND:
			// false ⇒ at least one side false; both must be enough.
			return s.falseImpliesStarted(x.X) && s.falseImpliesStarted(x.Y)
		}
	}
	return false
}

// livenessName returns the liveness field an expression refers to, if any.
//
// Recognised shapes:
//
//	w.started            (bool field)
//	w.started.Load()     (atomic.Bool)
//	w.cancel != nil      (nil comparison)
func (s *stopScanner) livenessName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.SelectorExpr:
		if livenessFields[x.Sel.Name] {
			return x.Sel.Name
		}
	case *ast.Ident:
		if name, ok := s.aliases[x.Name]; ok {
			return name
		}
		// `started` is the conventional name for a local liveness flag
		// (`started := false; ... ; if started { <-w.done }`), so it is
		// read as the liveness field it stands for.
		if x.Name == "started" {
			return "started"
		}
	case *ast.CallExpr:
		// w.started.Load() and w.started() are both liveness queries.
		if sel, ok := x.Fun.(*ast.SelectorExpr); ok {
			if sel.Sel.Name == "Load" {
				if inner, ok := sel.X.(*ast.SelectorExpr); ok && livenessFields[inner.Sel.Name] {
					return inner.Sel.Name
				}
			}
			if livenessFields[sel.Sel.Name] {
				return sel.Sel.Name
			}
		}
	case *ast.BinaryExpr:
		// nil comparison: the liveness field may sit on either side.
		if l := s.livenessName(x.X); l != "" {
			return l
		}
		return s.livenessName(x.Y)
	}
	return ""
}

// isNilComparison reports whether the binary expression compares against
// the nil literal.
func isNilComparison(x *ast.BinaryExpr) bool {
	return isNilIdent(x.X) || isNilIdent(x.Y)
}

func isNilIdent(e ast.Expr) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == "nil"
}

// endsWithReturn reports whether the block's last statement is an
// unconditional return.
func endsWithReturn(blk *ast.BlockStmt) bool {
	if blk == nil || len(blk.List) == 0 {
		return false
	}
	_, ok := blk.List[len(blk.List)-1].(*ast.ReturnStmt)
	return ok
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
