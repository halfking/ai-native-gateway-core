// Pin tests for the load-bearing premises of pending decision 82 (audit round
// 235), plus the fail-closed invariant that currently keeps 82 a latent trap
// instead of a live data-corruption bug.
//
// Why (2026-10-04, audit round 235):
//
// Round 196 registered 82 — 「三层 provenance 的 identity/occurrence 映射未闭合」
// — and wrote its decision precondition explicitly: 「先问『谁会是第一个消费方』」.
// Round 235 executed that precondition against the code instead of inheriting it.
//
// What the code says today:
//
//   - SanitizedMessageRef.RawIndex/SanitizedIndex (client request space) and
//     AlignmentInfo.OriginalIndex (assembled outbound space) index DIFFERENT
//     arrays. Round 196 measured the resulting offset as -2 in a compressed
//     session. That measurement still holds.
//   - BUT the two index spaces are never joined. There is no production caller
//     that reads either index as a lookup key.
//   - Two candidate consumers are ALREADY WRITTEN and BOTH unwired:
//     threetier.BuildAlignments (forward) and alignment_reverse.go's
//     ResolveCompressedToOriginal (reverse). So 82's blocker is not "should we
//     invent a consumer" — it is "pick one of the two, or delete both".
//   - The only code touching both index spaces at once is
//     recovery_coordinator.go's validatePersistedProvenance, and it is
//     fail-closed: any inconsistency returns false, which downgrades the V2
//     incremental recovery to "refused", never to a silent wrong read.
//
// These tests pin the premises, not a verdict. They do NOT assert that 82 is
// closed; they make the ledger's wording stop matching reality the moment
// somebody acts on it.
package compression

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 82's decision precondition: the two candidate consumers stay unwired.
// ---------------------------------------------------------------------------

// candidateConsumers are the two functions that already implement the
// "read the provenance indices as keys" semantics 82 is waiting for.
//
//   - threetier.BuildAlignments: forward (compressed index -> tier ranges).
//   - ResolveCompressedToOriginal: reverse (compressed index -> original).
//
// Both are in the same package as this test, so a same-package call is just as
// much a "wiring" as an out-of-package one.
var candidateConsumers = []string{
	"BuildAlignments",
	"ResolveCompressedToOriginal",
}

// knownWiredSymbol is the scan's own positive control.
//
// If the scanner cannot find this one being called from threetier/register.go,
// the scanner is broken and every "zero callers" verdict below is worthless.
// A guard that cannot fail is worse than no guard (playbook §172).
const (
	knownWiredSymbol     = "DetectMisalignment"
	knownWiredCallSite   = "register.go"
	knownWiredDefinition = "align.go"
)

func TestCandidateProvenanceConsumersRemainUnwired(t *testing.T) {
	root := repoRootForTest(t)

	calls, err := scanCallsInRepo(t, root, candidateConsumers)
	if err != nil {
		t.Fatalf("scan repo: %v", err)
	}

	// --- positive control: prove the scanner actually resolves call sites ----
	ctrl, err := scanCallsInRepo(t, root, []string{knownWiredSymbol})
	if err != nil {
		t.Fatalf("scan repo (control): %v", err)
	}
	if !containsSubstring(ctrl[knownWiredSymbol], knownWiredCallSite) {
		t.Fatalf("扫描器阳性对照失败：找不到 %s 在 %s 里的调用点（实际扫到 %v）。\n"+
			"扫描器坏了，下面所有「零调用方」结论都不成立。",
			knownWiredSymbol, knownWiredCallSite, ctrl[knownWiredSymbol])
	}
	// Stronger half of the control: the file that DEFINES the symbol must NOT
	// appear as a call site. If it did, the scanner would be counting
	// declarations as calls and every zero-caller verdict would be worthless.
	//
	// (This half caught a wrong expectation of mine on its first run: I had
	// asserted align.go was a call site, when align.go is where
	// DetectMisalignment is DEFINED and register.go is where it is called.)
	if containsSubstring(ctrl[knownWiredSymbol], knownWiredDefinition) {
		t.Fatalf("扫描器把定义点当成了调用点：%s 出现在 %s 里，但它只是定义文件。\n"+
			"扫描分不清声明与调用，下面所有「零调用方」结论都不成立（实际扫到 %v）。",
			knownWiredSymbol, knownWiredDefinition, ctrl[knownWiredSymbol])
	}
	t.Logf("阳性对照：%s 的调用点扫到 %d 处（含 %s，且定义文件 %s 正确地未出现）",
		knownWiredSymbol, len(ctrl[knownWiredSymbol]), knownWiredCallSite, knownWiredDefinition)

	// --- the actual premise -------------------------------------------------
	var offenders []string
	for _, sym := range candidateConsumers {
		for _, site := range calls[sym] {
			if isTestCallSite(site) {
				continue
			}
			offenders = append(offenders, site+" 调用了 "+sym)
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("待裁决 82 的前提已被打破：候选消费方被接线了。\n  %s\n\n"+
			"这意味着「第一个消费方」已经出现，82 的坐标系错位（客户端请求空间 vs "+
			"组装后出向空间，实测 offset -2）从「陷阱」变成「真实故障面」。\n"+
			"请在台账里改写 82：记录新消费方的落点、它 join 用的是哪一侧索引，"+
			"以及是否已按 196 号的 F1 做过错位验证。",
			strings.Join(offenders, "\n  "))
		return
	}
	t.Logf("82 前提仍成立：%d 个候选消费方在生产代码中零调用（扫描器阳性对照已通过）",
		len(candidateConsumers))
}

// ---------------------------------------------------------------------------
// The fail-closed invariant that keeps 82 latent.
//
// validatePersistedProvenance is the ONLY place that looks at both index
// spaces. It must reject — never silently accept — any of the shapes that 82
// says are ambiguous. Pin both directions: a well-formed meta must be
// ACCEPTED (otherwise every negative case below would pass vacuously), and
// each malformed shape must be REJECTED.
// ---------------------------------------------------------------------------

func goodProvenanceMeta(sourceMsgCount int) map[string]any {
	return map[string]any{
		"pre_sanitize_offset_range": []int{1, 3},
		"cut_marker": map[string]interface{}{
			"version":                   1,
			"created_at":                float64(time.Now().Unix()),
			"source_msg_count":          sourceMsgCount,
			"system_msg_count":          1,
			"cut_index":                 2,
			"strategy":                  "mechanical_trim",
			"pre_sanitize_offset_range": []int{1, 3},
		},
		"sanitize_message_refs": []map[string]interface{}{
			{"raw_index": 0, "sanitized_index": 0},
		},
		"alignment_map": []map[string]interface{}{
			{"original_index": 0, "compressed_index": 0},
		},
	}
}

func markerFromMeta(t *testing.T, meta map[string]any) CutMarker {
	t.Helper()
	nested, ok := meta["cut_marker"].(map[string]interface{})
	if !ok {
		t.Fatalf("fixture lost cut_marker")
	}
	marker, ok := cutMarkerFromMetadata(nested)
	if !ok {
		t.Fatalf("fixture cut_marker did not parse")
	}
	return marker
}

func TestValidatePersistedProvenanceAcceptsWellFormedMeta(t *testing.T) {
	// Anti-vacuity control. If this ever fails, every rejection case in
	// TestValidatePersistedProvenanceRejectsAmbiguousIndices is meaningless:
	// a function that always returns false would pass all of them.
	meta := goodProvenanceMeta(4)
	if !validatePersistedProvenance(meta, markerFromMeta(t, meta)) {
		t.Fatal("形态良好的 provenance 元数据必须被接受 —— " +
			"它被拒的话，下面所有「必须拒绝」的用例都失去鉴别力")
	}
}

func TestValidatePersistedProvenanceRejectsAmbiguousIndices(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m map[string]any)
		explain string
	}{
		{
			name: "refs 的 raw_index 不是位置序（196 号 F1 的形态）",
			mutate: func(m map[string]any) {
				m["sanitize_message_refs"] = []map[string]interface{}{
					{"raw_index": 0, "sanitized_index": 0},
					{"raw_index": 2, "sanitized_index": 2},
				}
			},
			explain: "refs 在客户端请求空间，位置序是它自身的不变量；破坏它必须 fail-closed",
		},
		{
			name: "refs 条数超过 SourceMsgCount",
			mutate: func(m map[string]any) {
				refs := []map[string]interface{}{}
				for i := 0; i < 5; i++ {
					refs = append(refs, map[string]interface{}{
						"raw_index": i, "sanitized_index": i,
					})
				}
				m["sanitize_message_refs"] = refs
			},
			explain: "跨空间的长度上界；客户端空间不应大于出向空间",
		},
		{
			name: "refs 出现负索引",
			mutate: func(m map[string]any) {
				m["sanitize_message_refs"] = []map[string]interface{}{
					{"raw_index": -1, "sanitized_index": 0},
				}
			},
			explain: "负索引不是合法的消息位置",
		},
		{
			name: "alignment 的 original_index 非递增",
			mutate: func(m map[string]any) {
				m["alignment_map"] = []map[string]interface{}{
					{"original_index": 1, "compressed_index": 0},
					{"original_index": 0, "compressed_index": 1},
				}
			},
			explain: "alignment 在出向空间，单调递增是它自身的不变量",
		},
		{
			name: "alignment 的 original_index 越出 SourceMsgCount",
			mutate: func(m map[string]any) {
				m["alignment_map"] = []map[string]interface{}{
					{"original_index": 9, "compressed_index": 0},
				}
			},
			explain: "越界索引说明两侧坐标系被混用，正是 82 描述的形态",
		},
		{
			name: "alignment 缺 original_index 键",
			mutate: func(m map[string]any) {
				m["alignment_map"] = []map[string]interface{}{
					{"compressed_index": 0},
				}
			},
			explain: "缺键不得被当作 0 静默通过",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta := goodProvenanceMeta(4)
			tc.mutate(meta)
			if validatePersistedProvenance(meta, markerFromMeta(t, meta)) {
				t.Fatalf("必须拒绝：%s", tc.explain)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// isTestCallSite reports whether a "rel/path.go:LINE" site string is in a test
// file.
//
// It splits off the line suffix FIRST. Testing strings.HasSuffix(site,
// "_test.go") on the composite "path_test.go:107" is silently always false —
// appending the line number moved the property off the end of the string, and
// the filter then lets every test call site through as if it were production
// wiring. (That bug shipped in the first run of this very test.)
func isTestCallSite(site string) bool {
	if i := strings.LastIndex(site, ":"); i >= 0 {
		site = site[:i]
	}
	return strings.HasSuffix(site, "_test.go")
}

func containsSubstring(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

// scanCallsInRepo walks every .go file under root and returns, per symbol, the
// "relpath:line" of every call expression naming that symbol.
//
// It is AST-based on purpose: the two candidate names are substrings of longer
// names in places (BuildAlignments is not, but ResolveCompressedToOriginal is
// unique only by luck), and a text scan cannot tell a call from a declaration.
func scanCallsInRepo(t *testing.T, root string, symbols []string) (map[string][]string, error) {
	t.Helper()
	want := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		want[s] = true
	}
	out := make(map[string][]string, len(symbols))

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			base := info.Name()
			if base == ".git" || base == "node_modules" || base == "vendor" ||
				base == "dist" || base == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// Unparsable file: skip it, but say so, so a zero-caller verdict
			// is never silently built on a file we failed to read.
			t.Logf("WARN 跳过无法解析的文件：%s（%v）", relTo(root, path), perr)
			return nil
		}
		rel := relTo(root, path)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok || !want[ident.Name] {
				return true
			}
			pos := fset.Position(call.Lparen)
			out[ident.Name] = append(out[ident.Name], rel+":"+itoa(pos.Line))
			return true
		})
		return nil
	})
	return out, err
}

func relTo(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(r)
	}
	return path
}

func repoRootForTest(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("从 %s 向上找不到 go.mod", thisFile)
		}
		dir = parent
	}
}
