// Guard for the family of parallel enumerations of "model-visible response
// item carriers" (audit round 238).
//
// Why this exists (and why it is a guard, not a defect claim):
//
// Round 195 found that the OpenAI Responses item-type enumeration lives in
// THREE parallel copies and that a commit which added two carriers to two of
// them silently left the third behind — so a model writing a phone number into
// `tool_search_call.arguments` or `mcp_approval_request.arguments` walked
// straight past both output gates. 195 fixed that one instance.
//
// The structural cause was never closed: nothing stopped the next carrier from
// drifting again. 195's own pin test covers two hardcoded type names, which
// cannot notice an 18th carrier added to one copy and forgotten in the others.
//
// As of HEAD the three copies ARE aligned (measured by this test's own census
// output). This guard's job is to keep them that way and to say so loudly the
// moment they stop being aligned.
//
// The three copies:
//
//	domains/hooks/outputcompliance/protocol_text.go  — output compliance lane,
//	    two switches (non-stream addOutputItem + stream output_item frame).
//	    This is the OUTPUT DETECTION surface.
//	security/sanitize/native_restore.go              — response-side restoration.
//	security/sanitize/input_protocols.go             — request-side sanitisation.
//
// The IR layer (internal/ir/response_protocols.go) is deliberately NOT in this
// set: it intentionally keeps only message/function_call/reasoning and records
// every other item type into UnknownBlockTypes for unsupported-response
// attribution (audit R20, 2026-09-13, internal/ir/response.go:160-181).
// Round 238 verified that attribution is not decoration: it is consumed by
// executor_anthropic.go:219. Excluding the IR layer here is a documented
// decision, not an oversight.
package sqlguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
)

// requestOnlyCarriers are item types that can only appear in a REQUEST body, so
// their presence in input_protocols.go and absence from the response-side copies
// is correct rather than drift. Listed for documentation only: the subset
// assertion never requires equality, so it does not need this list.
//
//   - function_call_output / custom_tool_call_output: the client sends tool
//     results back inside the request's input array.
var requestOnlyCarriers = []string{"function_call_output", "custom_tool_call_output"}

func containsCarrier(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// carrierSet is one item-type enumeration extracted from a source file.
type carrierSet struct {
	file   string
	line   int
	labels []string
}

func (c carrierSet) key() []string {
	out := append([]string(nil), c.labels...)
	sort.Strings(out)
	return out
}

func (c carrierSet) String() string { return c.file + ":" + strconv.Itoa(c.line) }

// extractItemSwitches parses one Go file and returns every switch statement
// whose case labels look like a RESPONSES ITEM-type enumeration.
//
// The selector is structural, not positional: an item-type switch is one that
// mentions "reasoning" AND at least one of "function_call"/"custom_tool_call".
// Content-block switches in the same files (text / thinking / tool_use / …)
// are excluded by that signature, so the test does not depend on line numbers
// and will not silently start matching the wrong switch after an edit.
func extractItemSwitches(t *testing.T, root, rel string) []carrierSet {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}

	var out []carrierSet
	ast.Inspect(file, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		var labels []string
		for _, stmt := range sw.Body.List {
			cc, ok := stmt.(*ast.CaseClause)
			if !ok {
				continue
			}
			for _, expr := range cc.List {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				v, err := strconv.Unquote(lit.Value)
				if err != nil {
					continue
				}
				labels = append(labels, v)
			}
		}
		has := func(s string) bool {
			for _, l := range labels {
				if l == s {
					return true
				}
			}
			return false
		}
		if has("reasoning") && (has("function_call") || has("custom_tool_call")) {
			out = append(out, carrierSet{file: rel, line: fset.Position(sw.Pos()).Line, labels: labels})
		}
		return true
	})
	return out
}

func equalSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func diff(a, b []string) (onlyA, onlyB []string) {
	inB := map[string]bool{}
	for _, x := range b {
		inB[x] = true
	}
	inA := map[string]bool{}
	for _, x := range a {
		inA[x] = true
		if !inB[x] {
			onlyA = append(onlyA, x)
		}
	}
	for _, x := range b {
		if !inA[x] {
			onlyB = append(onlyB, x)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return
}

// TestResponsesItemCarrierEnumerationsStayAligned is the census AND the guard.
func TestResponsesItemCarrierEnumerationsStayAligned(t *testing.T) {
	root := repoRootForTest(t)

	compliance := extractItemSwitches(t, root, "domains/hooks/outputcompliance/protocol_text.go")
	restore := extractItemSwitches(t, root, "security/sanitize/native_restore.go")
	sanitize := extractItemSwitches(t, root, "security/sanitize/input_protocols.go")

	// --- anti-vacuity: the selector must actually find the switches ---------
	// Without this, a selector that silently stops matching would make every
	// comparison below trivially equal.
	if len(compliance) != 2 {
		t.Fatalf("输出合规 lane 应有 2 处 item 型 switch（非流 + 流式 output_item 帧），实到 %d 处。\n"+
			"  实到：%v\n"+
			"若代码真的改了结构，请更新本守卫的选择器；不要把断言放宽成「0 也算过」。",
			len(compliance), compliance)
	}
	if len(restore) != 1 {
		t.Fatalf("native_restore.go 应有 1 处 item 型 switch，实到 %d 处：%v", len(restore), restore)
	}
	if len(sanitize) != 1 {
		t.Fatalf("input_protocols.go 应有 1 处 item 型 switch，实到 %d 处：%v", len(sanitize), sanitize)
	}

	nonStream, stream := compliance[0], compliance[1]
	rest := restore[0]
	sani := sanitize[0]

	t.Logf("普查（item 型 switch 的 case 标签数）：\n"+
		"  合规 lane 非流 %s = %d\n  合规 lane 流式 %s = %d\n"+
		"  出向 restore   %s = %d\n  入向 sanitize   %s = %d",
		nonStream, len(nonStream.labels), stream, len(stream.labels),
		rest, len(rest.labels), sani, len(sani.labels))

	// --- 1. the compliance lane's two switches must agree with each other ----
	if onlyA, onlyB := diff(nonStream.key(), stream.key()); len(onlyA)+len(onlyB) > 0 {
		t.Errorf("🔴 输出合规 lane 的非流与流式两份枚举已漂移：\n"+
			"  仅非流有：%v\n  仅流式有：%v\n\n"+
			"后果：同一种载体在流式与非流式下走不同的输出检测面 ⇒ 195 号那类"+
			"「模型写进工具载体直过两道输出闸」会只在其中一条 lane 上复现。",
			onlyA, onlyB)
	}

	// --- 2. compliance lane vs response-side restore ------------------------
	if onlyA, onlyB := diff(stream.key(), rest.key()); len(onlyA)+len(onlyB) > 0 {
		t.Errorf("🔴 输出检测面与还原面已漂移：\n"+
			"  仅合规 lane 有：%v\n  仅 restore 有：%v\n\n"+
			"若某个载体只在合规侧被识别：还原侧不会替换其中的占位符，"+
			"而合规侧看到的是占位符文本（不是敏感值）不会拦 ⇒ 占位符原样回客户端。",
			onlyA, onlyB)
	}

	// --- 3. request-side sanitize must cover everything restore covers -------
	//
	// Deliberately a SUBSET assertion, not equality. The switch in
	// input_protocols.go is a combined one: it also carries content-block
	// labels (text / input_text / …) and the two request-only carriers
	// (function_call_output / custom_tool_call_output, which the client sends
	// back in a request and which therefore cannot appear in a response).
	// Demanding set equality here would be asking the wrong question and would
	// have failed on correct code — so the invariant is the meaningful
	// one-directional form: anything restore handles must also be sanitised.
	got := map[string]bool{}
	for _, l := range sani.labels {
		got[l] = true
	}
	var uncovered []string
	for _, l := range rest.labels {
		if !got[l] {
			uncovered = append(uncovered, l)
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		t.Errorf("🔴 入向 sanitize 未覆盖出向 restore 已处理的载体：%v（%s）\n\n"+
			"后果：模型把敏感值写进这类载体时，请求侧不脱敏 ⇒ 原文直接发给供应商；"+
			"响应侧却在还原 ⇒ 该载体 lane 出现占位符却无人替换。",
			uncovered, rest)
	}
	var extra []string
	for _, l := range sani.labels {
		if !containsCarrier(rest.labels, l) {
			extra = append(extra, l)
		}
	}
	sort.Strings(extra)
	t.Logf("入向 sanitize 额外覆盖（请求独有载体 + content-block 型别，属预期）：%v", extra)
}

// TestComplianceLaneCoversThe195Carriers pins the two carriers 195 registered,
// by NAME, in addition to the set-equality guard above.
//
// The set-equality guard would stay green if all three copies drifted TOGETHER
// into the same wrong state. This test anchors the two specific carriers whose
// omission caused 195, so a "delete it everywhere" regression is caught too.
func TestComplianceLaneCoversThe195Carriers(t *testing.T) {
	root := repoRootForTest(t)
	sets := extractItemSwitches(t, root, "domains/hooks/outputcompliance/protocol_text.go")
	if len(sets) != 2 {
		t.Fatalf("预期 2 处 item 型 switch，实到 %d", len(sets))
	}
	want := []string{"tool_search_call", "mcp_approval_request"}
	for _, s := range sets {
		have := map[string]bool{}
		for _, l := range s.labels {
			have[l] = true
		}
		var missing []string
		for _, w := range want {
			if !have[w] {
				missing = append(missing, w)
			}
		}
		if len(missing) > 0 {
			t.Errorf("🔴 195 号登记的两个载体在 %s 缺席：%v\n"+
				"模型把手机号/凭据写进这些载体的 arguments 时会直过两道输出闸。",
				s, missing)
		}
	}
	t.Logf("两个 switch 均覆盖 195 号载体：%v", want)
}
