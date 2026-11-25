package promptinjection

// error_analyzer_compose.go — LLM 分析输出的解析总入口：解码 → 认字段 → 组装。
//
// ★ 什么时候算「解析失败」（返回 nil）：
//
//	只有**一条可用的分析内容都没救回来**时。具体是：
//	  · 没有任何 JSON 候选块能解出成员，且
//	  · 自由文本兜底也不够长（< minProseRunes 个字符 ⇒ 不是一份分析）。
//	其余情况一律产出结果——能救多少救多少，并在 parse_diagnostics 里
//	如实写明「怎么读出来的」（完整/嵌套/抢救/自由文本、是否截断、丢了哪些键）。
//
// ★ 安全口径（两条，都在这里强制）：
//
//	① 抢救出来的结果（输出被截断/有成员丢失）置信度**封顶 0.79**——
//	  低于应用修复的 0.80 门 ⇒ 只能看、不能一键应用。分析自己都不完整，
//	  就不该拿它去动检测规则。
//	② 自由文本兜底的结果置信度恒为 0（同样 view-only）。

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// ParseDiagnostics 描述一次解析是「怎么读出来的」（落在 analysis_details.parse_diagnostics）。
type ParseDiagnostics struct {
	Mode       string   `json:"mode"`                // canonical | nested | salvaged | unstructured | failed
	Candidates int      `json:"candidates"`          // 响应里的 JSON 候选块数
	Truncated  bool     `json:"truncated,omitempty"` // 输出在中途断掉
	Repaired   bool     `json:"repaired,omitempty"`  // 文本经过修复才解出
	LostKeys   []string `json:"lost_keys,omitempty"` // 没能解出来的顶层键
}

// 解析形态闭集。
const (
	// ParseModeCanonical：顶层就是约定结构（与旧解析器的成功路径逐字段等价）。
	ParseModeCanonical = "canonical"
	// ParseModeNested：JSON 完整，但字段散落在嵌套结构/别名键里。
	ParseModeNested = "nested"
	// ParseModeSalvaged：JSON 不完整或不合法，逐成员抢救出部分字段。
	ParseModeSalvaged = "salvaged"
	// ParseModeUnstructured：没有任何可用 JSON，按自由文本保留。
	ParseModeUnstructured = "unstructured"
	// ParseModeFailed：一条可用内容都没有。
	ParseModeFailed = "failed"
)

const (
	// minProseRunes：自由文本至少这么长才算「一份分析」（更短的是寒暄/拒答）。
	minProseRunes = 60
	// salvagedConfidenceCap：抢救结果的置信度封顶（< piFixConfidenceFloor 0.80）。
	salvagedConfidenceCap = 0.79

	maxRootCauseRunes    = 2000
	maxSuggestedFixRunes = 6000
)

// decodeInfo 是「顶层对象是怎么解出来的」。
type decodeInfo struct {
	salvaged  bool
	truncated bool
	repaired  bool
	lost      []string
}

// repairVariants 给出一段候选文本的修复变体（原样 → 去尾逗号 → 字符串内控制字符转义）。
//
//	顺序即优先级：能不动文本就不动。去重后返回。
func repairVariants(text string) []string {
	out := []string{text}
	noCommas := stripTrailingCommas(text)
	if noCommas != text {
		out = append(out, noCommas)
	}
	escaped := escapeRawControlChars(noCommas)
	if escaped != noCommas {
		out = append(out, escaped)
	}
	return out
}

// decodedDoc 是一份解出来的文档 + 它是怎么解出来的。
type decodedDoc struct {
	top *ordObj
	how decodeInfo
}

// decodeCandidates 把候选块依次解成文档（保持候选块的先后顺序）。
//
//	每块：先严格解码（取第一个能用的修复变体），不行再逐成员抢救。
//	两步都失败的候选块不产出文档。
func decodeCandidates(cands []string) []decodedDoc {
	var out []decodedDoc
	for _, c := range cands {
		decoded := false
		for vi, variant := range repairVariants(c) {
			if obj, err := decodeStrictOrdered(variant); err == nil {
				out = append(out, decodedDoc{obj, decodeInfo{repaired: vi > 0}})
				decoded = true
				break
			}
		}
		if decoded {
			continue
		}
		// ★ 抢救要看**每一个**修复变体、取丢得最少的那个：原样文本常常也能
		//   抢出一个对象，只是坏点所在的成员丢了（例：字符串里的裸换行让
		//   那个成员解不出）；而转义修复后的变体能把同一个成员救回来。
		//   取「第一个能出对象的」= 系统性地选了丢得最多的变体。
		best := salvageResult{}
		bestHow := decodeInfo{}
		for vi, variant := range repairVariants(c) {
			sr := salvageObject(variant, 0)
			if sr.obj == nil {
				continue
			}
			if best.obj == nil || salvageBetter(sr, best) {
				best, bestHow = sr, decodeInfo{salvaged: true, truncated: sr.cut, repaired: vi > 0, lost: sr.lost}
			}
		}
		if best.obj != nil {
			out = append(out, decodedDoc{best.obj, bestHow})
		}
	}
	return out
}

// salvageBetter 报告 a 是否比 b 抢救得更完整（丢的成员更少；平局时未截断者胜）。
func salvageBetter(a, b salvageResult) bool {
	if len(a.lost) != len(b.lost) {
		return len(a.lost) < len(b.lost)
	}
	return !a.cut && b.cut
}

// isCanonicalShape 判断严格解出的顶层对象是不是约定结构。
//
//	约定 = 顶层直接有字符串型 root_cause，且 suggested_fix（若有）是字符串。
//	其余按 nested 记（字段要靠别名表去认；suggested_fix 是对象/数组时，
//	结构化原文也要留档给管理面展示）。
func isCanonicalShape(top *ordObj) bool {
	idx := top.normIndex()
	orig, ok := idx["rootcause"]
	if !ok {
		return false
	}
	if _, isStr := top.vals[orig].(string); !isStr {
		return false
	}
	if fixOrig, ok := idx["suggestedfix"]; ok {
		if v := top.vals[fixOrig]; v != nil {
			if _, isStr := v.(string); !isStr {
				return false
			}
		}
	}
	return true
}

// parseAnalysisResponse 是 LLM 分析输出的唯一解析入口。
//
// ★ 选块按「哪一块真的读出了分析」定，不按位置猜：
//
//	候选块里既有模型写给的那份文档，也有碰巧括号配平的内层小对象、
//	散文里的花括号。外层文档有坏点要抢救时，一个内层小对象可能恰好能
//	严格解码——但它里面没有根因。⇒ 逐块收获，**第一块读出根因的胜出**；
//	一块都没读出根因时，退回自由文本兜底。
//
// 返回 (nil, diag) ⇔ 解析失败（diag.Mode == ParseModeFailed）。
func parseAnalysisResponse(response string) (*AnalysisResult, ParseDiagnostics) {
	diag := ParseDiagnostics{Mode: ParseModeFailed}
	if strings.TrimSpace(response) == "" {
		return nil, diag
	}
	cleaned := cleanResponse(response)
	cands := jsonCandidates(cleaned)
	diag.Candidates = len(cands)

	for _, doc := range decodeCandidates(cands) {
		h := harvestAnalysis(doc.top)
		// 根因正文的取用顺序：直接字段 → 根因**对象**的解释 → 摘要 → 根因清单的渲染。
		root := h.rootCause
		if root == "" {
			root = renderRootCause(h.rootCauseObjs, false)
		}
		if root == "" {
			root = h.summary
		}
		if root == "" {
			root = renderRootCause(h.rootCauseObjs, true)
		}
		if root == "" {
			continue
		}
		how := doc.how
		diag.Truncated, diag.Repaired, diag.LostKeys = how.truncated, how.repaired, how.lost
		switch {
		case how.salvaged:
			diag.Mode = ParseModeSalvaged
		case isCanonicalShape(doc.top):
			diag.Mode = ParseModeCanonical
		default:
			diag.Mode = ParseModeNested
		}
		return composeResult(h, root, &diag), diag
	}

	// 自由文本兜底：模型完全没按结构返回（或 JSON 一个成员都救不出）。
	if text := proseText(cleaned); utf8.RuneCountInString(strings.Join(strings.Fields(text), "")) >= minProseRunes {
		diag.Mode = ParseModeUnstructured
		return unstructuredResult(text), diag
	}
	return nil, diag
}

// composeResult 把收获到的字段组装成 AnalysisResult。
func composeResult(h *harvested, root string, diag *ParseDiagnostics) *AnalysisResult {
	category := h.category
	if category == "" {
		category = "unknown"
	}
	conf := 0.0
	if h.confidence != nil {
		conf = *h.confidence
	}
	if diag.Mode == ParseModeSalvaged && conf > salvagedConfidenceCap {
		conf = salvagedConfidenceCap
	}
	affected := 0
	if h.affected != nil {
		affected = *h.affected
	}
	isFP := h.isFalsePositive != nil && *h.isFalsePositive

	fixText := h.suggestedFix
	if fixText == "" {
		var lines []string
		for _, l := range h.fixLists {
			renderInto(&lines, l, 0)
		}
		fixText = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	if fixText == "" {
		fixText = h.summary
	}

	plan := buildFixPlan(h)
	ruleAdj := ruleAdjustmentCarrier(h.ruleAdjustment)
	if ruleAdj == nil && plan != nil && len(plan.AutoActions) > 0 {
		// 模型把可执行动作放在 fix_plan.auto_actions 里 ⇒ 并入 rule_adjustment
		// 载体（admin 的白名单归一化认 {rules: [...]} 这一形状）。
		ruleAdj = map[string]any{"rules": toPlainSlice(plan.AutoActions)}
	}

	details := map[string]any{}
	if d, ok := h.detailsRaw.(*ordObj); ok {
		for _, k := range d.keys {
			details[k] = toPlain(d.vals[k])
		}
	}
	if diag.Mode != ParseModeCanonical {
		// 嵌套/抢救：把模型的原始结构整体留档（管理面可展开看完整报告）。
		if raw := boundedValue(toPlain(h.top), maxModelOutputJSON); raw != nil {
			details["model_output"] = raw
		}
		if h.structuredFix != nil {
			details["suggested_fix_structured"] = boundedValue(toPlain(h.structuredFix), maxModelOutputJSON)
		}
	}
	if plan != nil {
		details["fix_plan"] = plan
	}
	if diag.Mode != ParseModeCanonical {
		pd := map[string]any{"mode": diag.Mode}
		if diag.Truncated {
			pd["truncated"] = true
		}
		if diag.Repaired {
			pd["repaired"] = true
		}
		if len(diag.LostKeys) > 0 {
			pd["lost_keys"] = diag.LostKeys
		}
		details["parse_diagnostics"] = pd
	}

	return &AnalysisResult{
		RootCause:             runeTruncate(root, maxRootCauseRunes),
		ErrorCategory:         category,
		IsFalsePositive:       isFP,
		ConfidenceScore:       conf,
		SuggestedFix:          runeTruncate(fixText, maxSuggestedFixRunes),
		RuleAdjustment:        ruleAdj,
		CodeChanges:           codeChangesCarrier(h.codeChanges),
		SeverityLevel:         normalizeSeverityLevel(h.severity),
		ImpactScope:           normalizeImpactScope(h.impact),
		AffectedUsersEstimate: affected,
		AnalysisDetails:       details,
	}
}

// unstructuredResult 是自由文本兜底的结果（view-only）。
//
//	★ 根因取「第一句实质内容」而不是第一行：模型的开场白（「以下是…分析报告：」
//
//	"Here is my analysis:"）不是根因 ⇒ 跳过以冒号结尾的引导句、Markdown 标题行。
func unstructuredResult(text string) *AnalysisResult {
	root := firstSubstantiveLine(text)
	if utf8.RuneCountInString(root) < 4 {
		root = runeTruncate(strings.Join(strings.Fields(text), " "), 200)
	}
	return &AnalysisResult{
		RootCause:       runeTruncate(root, maxRootCauseRunes),
		ErrorCategory:   "unstructured",
		IsFalsePositive: false,
		ConfidenceScore: 0,
		SuggestedFix:    runeTruncate(text, maxSuggestedFixRunes),
		SeverityLevel:   "low",
		ImpactScope:     "session",
		AnalysisDetails: map[string]any{"unstructured_response": true},
	}
}

// firstSubstantiveLine 取第一行「像结论」的文本（跳过开场白与标题行）。
func firstSubstantiveLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if utf8.RuneCountInString(line) < 8 {
			continue
		}
		if isIntroLine(line) {
			continue
		}
		return firstSentence(line, 300)
	}
	return ""
}

var introLinePrefixes = []string{"以下", "下面是", "Here is", "here is", "Below is", "below is"}

func isIntroLine(line string) bool {
	if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "**") {
		return true
	}
	for _, p := range introLinePrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// ruleAdjustmentCarrier 把 rule_adjustment 归成可落库的 map 形态。
//
//	数组（模型常见产出之一）⇒ 包成 {rules: [...]}——admin.NormalizeRuleAdjustment
//	认这一形状（形状 3）。旧解析器在这里会因「数组解不进 map」整条失败。
func ruleAdjustmentCarrier(v any) map[string]any {
	switch t := v.(type) {
	case *ordObj:
		return plainMap(t)
	case []any:
		if len(t) == 0 {
			return nil
		}
		return map[string]any{"rules": toPlainSlice(t)}
	}
	return nil
}

// codeChangesCarrier 同理（数组 ⇒ {items: [...]}）。
func codeChangesCarrier(v any) map[string]any {
	switch t := v.(type) {
	case *ordObj:
		return plainMap(t)
	case []any:
		if len(t) == 0 {
			return nil
		}
		return map[string]any{"items": toPlainSlice(t)}
	}
	return nil
}

func toPlainSlice(arr []any) []any {
	out := make([]any, len(arr))
	for i, item := range arr {
		out[i] = toPlain(item)
	}
	return out
}

// boundedValue 把值序列化后检查体量；超过上限 ⇒ 返回 nil（不落库）。
func boundedValue(v any, maxBytes int) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil || len(b) > maxBytes {
		return nil
	}
	return v
}

// plainMap 把保序对象转成标准 map。
func plainMap(o *ordObj) map[string]any {
	out, _ := toPlain(o).(map[string]any)
	return out
}
