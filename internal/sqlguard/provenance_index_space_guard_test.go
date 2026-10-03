// Guard for the two provenance index coordinate spaces (audit round 239).
//
// Background — the seam this pins:
//
// The gateway persists a three-layer provenance record whose indices live in
// TWO DIFFERENT ARRAYS, and nothing in the type system says so:
//
//	AlignmentInfo.OriginalIndex / CompressedIndex / CompressedInto
//	    index the ASSEMBLED OUTBOUND body
//	    (domains/hooks/compression/session_compressor.go:591,632 take
//	     `before := outboundBody`; diff.go:142-144 assembles
//	     `merged = lastMsgs ++ deltaTail`, so the array holds cached history
//	     the client did not resend).
//
//	SanitizedMessageRef.RawIndex / SanitizedIndex
//	    index the CLIENT REQUEST body's message array
//	    (security/sanitize/input_protocols.go:188 assigns both from one loop
//	     counter; sanitisation rewrites bytes in place and never reorders or
//	     re-lengths that array, so the two are always the same position).
//
// The offset between the two spaces depends on session state and CANNOT be
// reconstructed from the stored refs. The hashes are not a way around it
// either: refs carry MessageFingerprint (64-hex sha256 over json.Compact, key
// order preserved) while AlignmentInfo.Hash is msgHash (32-hex, sha256 over
// Unmarshal+Marshal, Go sorts object keys) — two different functions, so the
// same message has two permanently unequal strings.
//
// As of HEAD the two arrays are nonetheless reachable SIDE BY SIDE:
//
//   - they are written into one JSONB payload by
//     domains/streaming/request_log_pipeline.go:1526 (alignment_map) and :1536
//     (sanitize_message_refs), both from buildOutboundProvenance;
//   - handler.go:4017 passes a non-nil AlignmentMap, so on the chat lane BOTH
//     land in the same record; responses.go:272 and messages.go:262 pass nil;
//   - they are decoded back into sibling fields of the SAME struct twice:
//     domains/hooks/compression/session_compressor.go:1477-1478 (typed
//     []AlignmentInfo next to []SanitizedMessageRef) and
//     domains/session/v2/cache_v2.go:797,803 (loose map form).
//
// A join written against either struct would silently answer "where did
// original message N go?" from the wrong row, and no existing check would
// notice. That is audit finding 82 (P2), still open and awaiting a product
// decision — see 待裁决 82.
//
// This guard does NOT decide that question. It pins the fact that decides how
// urgent it is: no production code performs the cross-space join today. If
// someone writes one, this turns red at the exact line, which is the moment a
// human must be told the decision can no longer be deferred.
package sqlguard

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The two index families. Kept as one list each so a future field addition is
// a one-line change here, and so the test can print the full key set it
// actually recognises (a key it does not know about is a blind spot, and a
// silent blind spot is worse than no guard).
var (
	// outbound/alignment coordinate space
	alignIndexKeys = []string{
		"original_index", "compressed_index", "compressed_into",
		"OriginalIndex", "CompressedIndex", "CompressedInto",
	}
	// client-request/sanitisation coordinate space
	refIndexKeys = []string{
		"raw_index", "sanitized_index",
		"RawIndex", "SanitizedIndex",
	}
)

type indexSpaceViolation struct {
	file string
	line int
	form string
	src  string
}

func (v indexSpaceViolation) String() string {
	return v.file + ":" + strconv.Itoa(v.line) + " " + v.form + ": " + v.src
}

func keyIn(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// indexKeyOf reports which coordinate space an expression's terminal key
// belongs to. It recognises the two spellings a join can take in this repo:
//
//	ref.RawIndex == align.OriginalIndex   (typed struct field)
//	ref["raw_index"] == rec["original_index"]   (the JSONB / map[string]any
//	                                           form, which is how the data
//	                                           actually reaches the read path)
//
// It deliberately returns "" for anything else — a join laundered through a
// local variable or a helper call is not detected. That limitation is stated
// in the test output rather than papered over.
func indexKeyOf(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		if keyIn(alignIndexKeys, e.Sel.Name) {
			return "align"
		}
		if keyIn(refIndexKeys, e.Sel.Name) {
			return "ref"
		}
	case *ast.IndexExpr:
		lit, ok := e.Index.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return ""
		}
		v, err := strconv.Unquote(lit.Value)
		if err != nil {
			return ""
		}
		if keyIn(alignIndexKeys, v) {
			return "align"
		}
		if keyIn(refIndexKeys, v) {
			return "ref"
		}
	}
	return ""
}

func isComparisonOp(op token.Token) bool {
	switch op {
	case token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	}
	return false
}

// findIndexSpaceJoins returns every comparison in one parsed file that puts an
// index from one coordinate space against an index from the other.
func findIndexSpaceJoins(fset *token.FileSet, file *ast.File) []indexSpaceViolation {
	var out []indexSpaceViolation
	ast.Inspect(file, func(n ast.Node) bool {
		be, ok := n.(*ast.BinaryExpr)
		if !ok || !isComparisonOp(be.Op) {
			return true
		}
		l, r := indexKeyOf(be.X), indexKeyOf(be.Y)
		if l == "" || r == "" || l == r {
			return true
		}
		pos := fset.Position(be.Pos())
		src := ""
		if pos.Filename != "" {
			if b, err := os.ReadFile(pos.Filename); err == nil {
				src = lineOf(b, pos.Line)
			}
		}
		out = append(out, indexSpaceViolation{
			file: pos.Filename,
			line: pos.Line,
			form: l + " vs " + r,
			src:  strings.TrimSpace(src),
		})
		return true
	})
	return out
}

func lineOf(b []byte, n int) string {
	start := 0
	for i := 1; i < n; i++ {
		idx := bytes.IndexByte(b[start:], '\n')
		if idx < 0 {
			return ""
		}
		start += idx + 1
	}
	end := bytes.IndexByte(b[start:], '\n')
	if end < 0 {
		end = len(b)
	}
	return string(b[start : start+end])
}

// mentionsAnyIndexKey is the cheap pre-filter: parsing all 2 263 non-test Go
// files in this repo would blow the guard budget, and a comparison between the
// two spaces cannot exist without one of the ten keys appearing literally.
func mentionsAnyIndexKey(b []byte) bool {
	for _, k := range append(append([]string{}, alignIndexKeys...), refIndexKeys...) {
		if bytes.Contains(b, []byte(k)) {
			return true
		}
	}
	return false
}

// TestIndexSpaceJoinDetectorHasTeeth proves the detector can both fire and
// stay quiet, on synthetic source, BEFORE it is pointed at the repository.
//
// This is the negative control for the guard below. Without it, "zero
// violations" would be indistinguishable from "a selector that matches
// nothing" — the same class of bug as a census whose comparison silently
// degenerates into empty-set equality.
func TestIndexSpaceJoinDetectorHasTeeth(t *testing.T) {
	const src = `package synthetic

type Ref struct{ RawIndex, SanitizedIndex int }
type Aln struct{ OriginalIndex, CompressedIndex, CompressedInto int }

// MUST FIRE: typed struct fields across the two spaces.
func typedJoin(refs []Ref, align []Aln) bool {
	for _, r := range refs {
		for _, a := range align {
			if r.RawIndex == a.OriginalIndex {
				return true
			}
		}
	}
	return false
}

// MUST FIRE: the JSONB / map[string]any spelling, which is how the data
// actually reaches validatePersistedProvenance's neighbourhood.
func mapJoin(ref map[string]any, rec map[string]any) bool {
	return ref["raw_index"] == rec["original_index"]
}

// MUST NOT FIRE: both sides in the same space (self-join).
func sameSpaceJoin(a, b Aln) bool { return a.OriginalIndex == b.CompressedIndex }

// MUST NOT FIRE: unrelated comparison that mentions no index key at all.
func unrelated(x, y int) bool { return x == y }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "synthetic.go", src, 0)
	if err != nil {
		t.Fatalf("parse synthetic source: %v", err)
	}
	got := findIndexSpaceJoins(fset, file)

	// Count only what the synthetic file can produce; the source-line lookup
	// is best-effort here because the file is not on disk.
	fired := 0
	for _, v := range got {
		if v.form == "ref vs align" {
			fired++
		}
	}
	if fired != 2 {
		t.Fatalf("检测器必须恰好命中 2 处跨空间比较（typedJoin + mapJoin），实到 %d 处：%v\n"+
			"  若这里是 0，仓库扫描的「零违规」毫无意义；若 >2，说明它会误报同空间比较。",
			fired, got)
	}
	if len(got) != fired {
		t.Errorf("检测器报告了非 ref-vs-align 的违规（判据选错空间）：%v", got)
	}
	t.Logf("合成源对照：2 处跨空间比较全部命中，同空间比较与无关比较均未误报")
}

// TestNoProductionCodeJoinsProvenanceIndexSpaces is the actual guard: no
// production code may compare an AlignmentInfo index against a
// SanitizedMessageRef index.
func TestNoProductionCodeJoinsProvenanceIndexSpaces(t *testing.T) {
	root := repoRootForTest(t)

	var (
		violations  []indexSpaceViolation
		parsed      int
		coLocated   []string
		skippedTest int
	)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, "_test.go") {
			skippedTest++
			return nil
		}
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if !mentionsAnyIndexKey(raw) {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, raw, 0)
		if perr != nil {
			// A file that does not parse is not this guard's business, but
			// silently skipping it would hide coverage loss.
			t.Logf("跳过无法解析的文件（不影响本轮结论）：%s: %v", rel, perr)
			return nil
		}
		parsed++
		hasAlign, hasRef := false, false
		for _, k := range alignIndexKeys {
			if bytes.Contains(raw, []byte(k)) {
				hasAlign = true
			}
		}
		for _, k := range refIndexKeys {
			if bytes.Contains(raw, []byte(k)) {
				hasRef = true
			}
		}
		if hasAlign && hasRef {
			coLocated = append(coLocated, rel)
		}
		for _, v := range findIndexSpaceJoins(fset, f) {
			v.file = rel
			violations = append(violations, v)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}

	sort.Strings(coLocated)

	// --- anti-vacuity -------------------------------------------------------
	// "Zero violations" is only meaningful if the scan actually looked at the
	// files where such a join could be written. Both halves are asserted: the
	// scan must have parsed a real population, and the co-location census
	// (files naming keys from BOTH spaces) must be non-empty. The second one is
	// the load-bearing one — those files are exactly the places a future
	// wrong join would be typed, and this test prints them.
	if parsed == 0 {
		t.Fatalf("预筛后解析到 0 个文件，扫描覆盖为零 ⇒ 「零违规」是空跑。请检查 mentionsAnyIndexKey。")
	}
	if len(coLocated) == 0 {
		t.Fatalf("同时提到两侧索引键的文件为 0 个，与 239 号亲验的共置事实不符（session_compressor.go:1477-1478 等）。")
	}
	t.Logf("普查：非测试 Go 文件 %d 个被解析（含索引键），%d 个测试文件按设计跳过。\n"+
		"  同时提到两侧【索引键】的文件（未来误 join 可能被写下的位置）共 %d 个：\n    %s\n"+
		"  注意这与「两侧【数组】被解到同一个 struct」不是同一件事，见 TestProvenanceArraysRemainCoLocated。",
		parsed, skippedTest, len(coLocated), strings.Join(coLocated, "\n    "))
	t.Logf("本判据认识的索引键：align=%v / ref=%v；"+
		"未覆盖形态：经局部变量或辅助函数中转的比较、按切片位置配对而不点名索引键的写法。",
		alignIndexKeys, refIndexKeys)

	if len(violations) > 0 {
		for _, v := range violations {
			t.Errorf("🔴 生产代码把两个坐标系的下标放在同一次比较里：%s\n  源码：%s\n\n"+
				"后果：\n"+
				"  SanitizedMessageRef 的下标索引客户端请求数组，AlignmentInfo 的下标索引出向组装数组，"+
				"两者相差一个由会话状态决定、且无法从已存下的 refs 重建的偏移；"+
				"哈希也不是替代路（MessageFingerprint 64-hex vs msgHash 32-hex，是两个函数）。\n"+
				"  这样 join 出来的「原始消息 N 去哪了」会静默指向另一行。\n"+
				"  这正是待裁决 82 一直挂着的那个 join —— 写它之前必须先裁决坐标系，不要在这里顺手写。",
				v, v.src)
		}
	}
}

// TestProvenanceArraysRemainCoLocated pins the fact that decides how urgent
// 待裁决 82 is: the two arrays — not their index keys, the arrays themselves —
// are decoded into SIBLING FIELDS OF THE SAME STRUCT, and on the chat lane are
// written into the same JSONB payload.
//
// READ THIS BEFORE "FIXING" IT. This test asserts that the trap is still armed.
// It is not saying the co-location is correct. If it ever goes red, that is
// almost certainly someone separating the two arrays, which is a GOOD change —
// but it invalidates the decision material written for 待裁决 82, so the report
// has to be rewritten rather than the test relaxed.
//
// The two sites, both verified by reading on 2026-10-04:
//
//	session_compressor.go:1477-1478 — decodeMetaRecords into state.AlignmentMap
//	    ([]AlignmentInfo) and state.SanitizeMessageRefs ([]SanitizedMessageRef),
//	    two index spaces, both plain `int`, one struct.
//	cache_v2.go:797,803 — the same pair as []map[string]interface{} with
//	    `json:"alignment_map"` / `json:"sanitize_message_refs"` tags.
func TestProvenanceArraysRemainCoLocated(t *testing.T) {
	root := repoRootForTest(t)

	// --- site 1: two decodeMetaRecords calls in one function body -----------
	fset := token.NewFileSet()
	const scRel = "domains/hooks/compression/session_compressor.go"
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(scRel)))
	if err != nil {
		t.Fatalf("read %s: %v", scRel, err)
	}
	f, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(scRel)), raw, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", scRel, err)
	}

	type fnPair struct {
		fn              string
		alignLine       int
		sanitizedLine   int
		anyDecodeTarget bool
	}
	var pairs []fnPair
	ast.Inspect(f, func(n ast.Node) bool {
		var body *ast.BlockStmt
		var name string
		switch d := n.(type) {
		case *ast.FuncDecl:
			body, name = d.Body, d.Name.Name
		case *ast.FuncLit:
			body, name = d.Body, "<literal>"
		default:
			return true
		}
		if body == nil {
			return true
		}
		p := fnPair{fn: name}
		ast.Inspect(body, func(m ast.Node) bool {
			call, ok := m.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "decodeMetaRecords" || len(call.Args) < 2 {
				return true
			}
			p.anyDecodeTarget = true
			idx, ok := call.Args[0].(*ast.IndexExpr)
			if !ok {
				return true
			}
			lit, ok := idx.Index.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			key, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			line := fset.Position(call.Pos()).Line
			switch key {
			case "alignment_map":
				p.alignLine = line
			case "sanitize_message_refs":
				p.sanitizedLine = line
			}
			return true
		})
		if p.alignLine > 0 && p.sanitizedLine > 0 {
			pairs = append(pairs, p)
		}
		return true
	})

	if len(pairs) != 1 {
		t.Fatalf("期望恰好 1 处函数把 alignment_map 与 sanitize_message_refs 解到同一函数体内，实到 %d 处：%+v\n"+
			"  这说明共置形态变了：若是被拆开（好改动），待裁决 82 的裁决材料需要重写；若是被复制到多处，风险上升。\n"+
			"  请更新本测试与 docs/全面审计v3 下的 239 号报告，不要把断言放宽成「0 也算过」。",
			len(pairs), pairs)
	}
	t.Logf("共置点 1：%s:%d 与 :%d 在同一函数 %s 内解码进同一个 state 的两个字段。",
		scRel, pairs[0].alignLine, pairs[0].sanitizedLine, pairs[0].fn)

	// --- site 2: one struct type carrying both json tags -------------------
	const cvRel = "domains/session/v2/cache_v2.go"
	raw2, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(cvRel)))
	if err != nil {
		t.Fatalf("read %s: %v", cvRel, err)
	}
	f2, err := parser.ParseFile(fset, filepath.Join(root, filepath.FromSlash(cvRel)), raw2, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", cvRel, err)
	}

	type structPair struct {
		owner        string
		alignLine    int
		sanitizedLin int
	}
	var structs []structPair

	// The two arrays are NOT in a named type: cache_v2.go:786 declares an
	// anonymous `var meta struct { ... }` inside applyCompressionMeta. So the
	// scan has to look at ast.StructType nodes anywhere, not just TypeSpecs —
	// a first version of this test looked only at named types and reported
	// zero, which would have read as "the co-location is gone" when the shape
	// had merely never been named.
	recordStruct := func(owner string, st *ast.StructType) {
		if st.Fields == nil {
			return
		}
		p := structPair{owner: owner}
		for _, fld := range st.Fields.List {
			if fld.Tag == nil {
				continue
			}
			tag, uerr := strconv.Unquote(fld.Tag.Value)
			if uerr != nil {
				continue
			}
			switch tag {
			case `json:"alignment_map"`:
				p.alignLine = fset.Position(fld.Pos()).Line
			case `json:"sanitize_message_refs"`:
				p.sanitizedLin = fset.Position(fld.Pos()).Line
			}
		}
		if p.alignLine > 0 && p.sanitizedLin > 0 {
			structs = append(structs, p)
		}
	}
	ast.Inspect(f2, func(n ast.Node) bool {
		if fd, ok := n.(*ast.FuncDecl); ok {
			if fd.Body != nil {
				ast.Inspect(fd.Body, func(m ast.Node) bool {
					if st, ok := m.(*ast.StructType); ok {
						recordStruct(fd.Name.Name, st)
					}
					return true
				})
			}
			return false
		}
		if ts, ok := n.(*ast.TypeSpec); ok {
			if st, ok := ts.Type.(*ast.StructType); ok {
				recordStruct("type "+ts.Name.Name, st)
			}
		}
		return true
	})

	if len(structs) != 1 {
		t.Fatalf("期望恰好 1 个 struct 同时带 alignment_map 与 sanitize_message_refs 两个 json tag，实到 %d 个：%+v\n"+
			"  同上：形态变了就更新报告与本测试，不要放宽断言。", len(structs), structs)
	}
	t.Logf("共置点 2：%s 的 %s 同时带两个 json tag（:%d / :%d）。",
		cvRel, structs[0].owner, structs[0].alignLine, structs[0].sanitizedLin)
}
