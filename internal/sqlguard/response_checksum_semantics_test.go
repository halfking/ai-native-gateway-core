// Guard for the `response_checksum` / `request_checksum` columns on
// `session_turns` and `request_logs`.
//
// Why this guard exists (2026-10-03, audit round 228):
//
// Both columns are named "checksum", land in the durable turn/log tables, and
// are exposed verbatim by the admin APIs (`admin/logs.go:50-51`,
// `admin/telemetry.go:87`). A reader of those payloads is invited to treat them
// as integrity evidence for the corresponding payload.
//
// The two columns turned out to have very different standing, and the audit's
// first three conclusions about them were all wrong. What is actually true:
//
//  1. `request_checksum` IS a plain deterministic content hash.
//     `(*EventBuilder).RequestChecksum(body)` (`domains/hooks/audit/audit.go:961`)
//     computes `SHA256(body)` and IS wired, at three protocol handlers as a
//     chained builder call: `handler.go:3271`, `messages.go:571`,
//     `responses.go:547`.
//
//  2. `ComputeRequestChecksum(model, body)` (`audit.go:1176`) computes
//     `SHA256(model || body)` — concatenated with no separator — and has
//     ZERO production callers (`audit_test.go:643-646` only). It is dead code.
//     Its trap is purely lexical: its name CONTAINS `RequestChecksum`, so any
//     text search for the live calculator also matches this dead one, and
//     vice versa. Round 228 twice concluded "both calculators are unwired"
//     from such a search. The two also disagree on the algorithm, so a future
//     wiring of the dead one would silently change every stored value's meaning.
//
//  3. `response_checksum` is NOT a content hash. Two production accumulators
//     write the same field with incompatible semantics:
//
//     - `(*StreamCapture).ObserveChunk` (`domains/hooks/audit/stream.go:38`) —
//     hashes `json.Marshal(chunk)` of an `ir.StreamChunk`. That struct carries
//     `ID` (unique per chunk), `Created` (unix timestamp) and `Model` (may be
//     rewritten by the gateway), and only the pointer fields carry
//     `omitempty` (`internal/ir/stream.go:25,28,31`). So the value depends on
//     identity and time, not on content: two byte-identical responses can
//     produce different checksums. Wired on the mainstream streaming paths —
//     OpenAI stream, Anthropic stream and both protocol bridges.
//
//     - `(*StreamCapture).ObservePayload` (`audit.go:658`) — hashes the raw SSE
//     frame string, chained: `h_n = SHA256(h_{n-1} || frame_n)`. The value
//     therefore depends on how the response was framed on the wire. Wired on
//     the responses/native-responses paths.
//
//     Values produced by the two are not comparable, and neither answers "was
//     this body altered?". No read side compares either column: every
//     non-test reference is a transport (struct field, SQL parameter, API
//     response), and `domains/sessiondigest.Envelope` (`digest.go:52-58`)
//     carries no hash field at all.
//
// What this guard pins, and why each condition is currently satisfiable:
//
//   - The live request-checksum calculator stays the body-only one, and the
//     dead no-separator one stays unwired. This is the condition that keeps a
//     future rewire from silently redefining every historical value.
//
//   - The state of the `response_checksum` semantic split is recorded as a
//     fingerprint rather than a failure, because the split is a REGISTERED
//     finding (decision item 94), not a regression: both accumulators are
//     wired today. If either ever drops to zero, the guard says the split may
//     be gone and asks for a human to close item 94 — it does not turn red,
//     because closing a finding is a decision, not a build failure.
//
// Absence assertions ("nothing compares these columns") are deliberately NOT
// failure conditions; a 0-hit result is evidence, not a verdict (playbook §157).
package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// checksumAccumulator is one method that assigns the StreamCapture checksum field.
type checksumAccumulator struct {
	file        string // repo-relative
	method      string
	what        string // what it hashes
	prodCallers int    // state recorded by audit round 228
}

// checksumAccumulators enumerates every method that writes `sc.checksum`.
//
// It is written out by hand rather than discovered, because the guard's job is
// to notice a NEW accumulator. A discovering scanner would silently skip any
// accumulator whose right-hand side differs in shape.
var checksumAccumulators = []checksumAccumulator{
	{file: "domains/hooks/audit/stream.go", method: "ObserveChunk",
		what: "json.Marshal(ir.StreamChunk) —— 含 ID/Created/Model", prodCallers: 12},
	{file: "domains/hooks/audit/audit.go", method: "ObservePayload",
		what: "原始 SSE 帧字符串，链式 h_n = SHA256(h_{n-1} || frame_n)", prodCallers: 5},
	{file: "domains/hooks/audit/audit.go", method: "RecordChunk",
		what: "调用方给的 payload 字节", prodCallers: 0},
}

func TestResponseChecksumSplitStaysRecorded(t *testing.T) {
	root := repoRootForTest(t)

	type row struct {
		method string
		prod   int
		where  []string
	}
	var rows []row
	for _, acc := range checksumAccumulators {
		refs := productionCallSites(root, acc.method)
		var where []string
		prod := 0
		for _, r := range refs {
			if strings.HasSuffix(r.file, "_test.go") {
				continue
			}
			prod++
			where = append(where, r.file+":"+strconv.Itoa(r.line))
		}
		sort.Strings(where)
		rows = append(rows, row{acc.method, prod, where})
	}

	for _, r := range rows {
		t.Logf("%s：生产调用方 %d 个 —— %s", r.method, r.prod, r.where)
	}

	wired := 0
	for _, r := range rows {
		if r.prod > 0 {
			wired++
		}
	}
	switch classifyChecksumProducers(wired) {
	case producersSplit:
		t.Logf("确认待裁决 94 仍然成立：%d 个语义不同的累加器同时在写 response_checksum，"+
			"该列的值依赖协议路径，跨协议不可比。", wired)
	case producersUnified:
		t.Logf("response_checksum 现在只有 1 个生产累加器 —— 语义分叉可能已被消除。" +
			"请人工确认后关闭待裁决 94，并决定该列的语义是否已符合字段名承诺。")
	default:
		t.Errorf("response_checksum 没有任何生产累加器（%d 个）—— 该列将静默停止写入，"+
			"无编译错误、无测试失败。", wired)
	}
}

// producerState classifies how many checksum accumulators are wired.
type producerState int

const (
	producersNone producerState = iota
	producersUnified
	producersSplit
)

// classifyChecksumProducers is factored out so the split/unified/none decision
// can be unit-tested directly.
//
// The "unified" branch is a self-invalidating guard: it fires when a future fix
// removes the semantic split. Reaching that branch by editing the seventeen
// production call sites is not worth the blast radius, and reaching it by
// rewriting the guard's own recorded count would test nothing (playbook §183).
// Feeding the function a count is neither — the input is the property under
// test, not the scanner's own output (playbook §171).
func classifyChecksumProducers(wired int) producerState {
	switch {
	case wired == 0:
		return producersNone
	case wired == 1:
		return producersUnified
	default:
		return producersSplit
	}
}

func TestClassifyChecksumProducersCoversEveryState(t *testing.T) {
	cases := []struct {
		wired int
		want  producerState
	}{
		{wired: 0, want: producersNone},    // all accumulators unwired: the column stops being written
		{wired: 1, want: producersUnified}, // split fixed: prompt to close decision item 94
		{wired: 2, want: producersSplit},   // today's state
		{wired: 3, want: producersSplit},   // a third accumulator appears: still a split
	}
	for _, c := range cases {
		if got := classifyChecksumProducers(c.wired); got != c.want {
			t.Errorf("classifyChecksumProducers(%d) = %v, want %v", c.wired, got, c.want)
		}
	}
}

// TestRequestChecksumCalculatorIdentity pins WHICH algorithm fills the column.
//
// The failure condition is the dead calculator gaining a production caller: its
// `SHA256(model || body)` has no separator, so the same concatenation is
// reachable from different (model, body) splits, and the two calculators
// disagree on what the column means.
func TestRequestChecksumCalculatorIdentity(t *testing.T) {
	root := repoRootForTest(t)

	dead := productionCallSites(root, "ComputeRequestChecksum")
	deadProd := 0
	for _, r := range dead {
		if !strings.HasSuffix(r.file, "_test.go") {
			deadProd++
			t.Logf("ComputeRequestChecksum 的生产引用：%s:%d", r.file, r.line)
		}
	}
	if deadProd > 0 {
		t.Errorf("ComputeRequestChecksum（SHA256(model||body)，无分隔符）出现了 %d 个生产调用方。"+
			"它与在用的 EventBuilder.RequestChecksum（SHA256(body)）不是同一个算法，"+
			"且函数名包含后者，文本检索无法区分二者；接线前必须登记用哪一个以及读侧是否比对。",
			deadProd)
	}

	live := productionCallSites(root, "RequestChecksum")
	liveProd := 0
	for _, r := range live {
		if !strings.HasSuffix(r.file, "_test.go") {
			liveProd++
		}
	}
	if liveProd == 0 {
		t.Errorf("在用的 request_checksum 计算器 EventBuilder.RequestChecksum 没有生产调用方了 —— " +
			"该列将静默停止写入（无编译错误、无测试失败）")
	}
}

// TestChecksumColumnsAreNotUsedAsIntegrityEvidence prints only; it never fails.
//
// "No read side compares these columns" is a negative conclusion: a 0-hit scan
// may mean the search words were wrong or the predicate was vacuous. It is
// evidence for the report, not a build condition (playbook §157).
func TestChecksumColumnsAreNotUsedAsIntegrityEvidence(t *testing.T) {
	root := repoRootForTest(t)

	total := 0
	for _, name := range []string{"RequestChecksum", "ResponseChecksum"} {
		for _, r := range productionCallSites(root, name) {
			if strings.HasSuffix(r.file, "_test.go") {
				continue
			}
			total++
			t.Logf("生产引用（仅登记，非校验结论）：%s:%d", r.file, r.line)
		}
	}
	t.Logf("request/response checksum 的生产引用共 %d 处。人工核查：全部是搬运"+
		"（struct 字段 / SQL 参数 / API 响应），无一处拿已存值与重算值比较；"+
		"且 sessiondigest.Envelope（digest.go:52-58）不含任何 hash 字段。", total)
}

// callSite is one reference to an identifier with its file and line.
type callSite struct {
	file string
	line int
}

// trackedNames are every identifier this file reasons about. The repo index is
// built once for all of them: a naive per-name walk parsed 9,836 .go files
// eight times and pushed this package from 29.2s to 75.6s, which is the
// "guard got slower" signal (the package budget is -timeout=120s).
var trackedNames = []string{
	"ObserveChunk", "ObservePayload", "RecordChunk",
	"RequestChecksum", "ResponseChecksum", "ComputeRequestChecksum",
}

var (
	indexOnce sync.Once
	repoRefs  map[string][]callSite
)

// buildRepoIndex walks the repo exactly once and records, for every tracked
// name, both its declaration and every reference to it.
func buildRepoIndex(root string) map[string][]callSite {
	indexOnce.Do(func() {
		repoRefs = make(map[string][]callSite, len(trackedNames))
		tracked := make(map[string]bool, len(trackedNames))
		for _, n := range trackedNames {
			tracked[n] = true
		}
		// Declarations first-pass here too: record them with a sentinel so the
		// reference pass can drop the declaration line of the same name.
		declFile := map[string]string{}
		declLine := map[string]int{}

		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				switch filepath.Base(path) {
				case "vendor", "node_modules", ".git":
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			rel, rerr := filepath.Rel(root, path)
			if rerr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)

			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return nil
			}
			for _, d := range f.Decls {
				fd, ok := d.(*ast.FuncDecl)
				if !ok || !tracked[fd.Name.Name] {
					continue
				}
				if _, seen := declFile[fd.Name.Name]; !seen {
					declFile[fd.Name.Name] = rel
					declLine[fd.Name.Name] = fset.Position(fd.Pos()).Line
				}
			}
			ast.Inspect(f, func(n ast.Node) bool {
				switch node := n.(type) {
				case *ast.SelectorExpr:
					name := node.Sel.Name
					if !tracked[name] {
						return true
					}
					line := fset.Position(node.Sel.Pos()).Line
					if declFile[name] == rel && declLine[name] == line {
						return true
					}
					repoRefs[name] = append(repoRefs[name], callSite{file: rel, line: line})
				case *ast.CallExpr:
					// Bare package-level call: ComputeRequestChecksum(...)
					id, ok := node.Fun.(*ast.Ident)
					if !ok || !tracked[id.Name] {
						return true
					}
					line := fset.Position(id.Pos()).Line
					if declFile[id.Name] == rel && declLine[id.Name] == line {
						return true
					}
					repoRefs[id.Name] = append(repoRefs[id.Name], callSite{file: rel, line: line})
				}
				return true
			})
			return nil
		})
	})
	return repoRefs
}

// productionCallSites returns every reference to `name`, covering BOTH
// syntactic forms it can take:
//
//	(a) method/field selector   capture.ObservePayload(...)  -> *ast.SelectorExpr
//	(b) bare package-level call ComputeRequestChecksum(...)  -> *ast.Ident under *ast.CallExpr
//
// Form (b) matters: this guard exists partly because `ComputeRequestChecksum`
// and `RequestChecksum` are different things whose names overlap, and a scanner
// recognising only (a) reports "0 call sites" for a package-level function
// without ever having looked at it.
//
// A first version of this helper matched `sel.X` (the RECEIVER's identifier)
// against the method name, which matches nothing at all and reported every
// method as unwired. Compare the SELECTED name, never the receiver.
func productionCallSites(root, name string) []callSite {
	idx := buildRepoIndex(root)
	out := make([]callSite, len(idx[name]))
	copy(out, idx[name])
	return out
}
