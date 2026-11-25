package promptinjection

// error_analyzer_harvest.go — 从已解码的 JSON 里**认出**分析字段 + 类型宽 coercion
// + 结构化修正方案（fix_plan）。
//
// ★ 为什么需要「认」而不是「反序列化」：
//
//	分析提示词只约定了字段名，没有锁定**层级**。生产实测（245/154，2026-11-25）
//	模型按自己的报告体例返回——同一份分析的字段散落在：
//	  · analysis_report.root_cause_analysis.primary_cause
//	  · diagnosis.root_cause / remediation.immediate_actions[]
//	  · 中文键 分析结果.根因分析.详细说明 / 修复建议.立即措施[]
//	旧解析器只认「顶层 + 英文规范名 + 精确类型」⇒ 上面这些全部丢弃。
//
//	⇒ 本文件做三件事：
//	  ① 广度优先遍历（浅层优先），按**别名表**认字段（中英文、常见同义键）；
//	  ② 逐字段 coercion（suggested_fix 是对象/数组 ⇒ 渲染成文本；置信度是
//	     "85%"/"高" ⇒ 归一；布尔是 "是"/"否" ⇒ 归一……）；
//	  ③ 把散落的修复建议收成结构化 fix_plan（人工步骤 / 配置 / 代码 /
//	     部署 / 验证）——模型的输出无论什么体例，管理面都按同一种结构展示。
//
// ★ 安全口径不变：rule_adjustment 的白名单归一化仍在 admin 层（ApplyFix）。
//
//	这里只负责「把建议读出来」，不负责「让建议可执行」——可执行与否由
//	admin.NormalizeRuleAdjustment + 置信度门 + 类型闸决定。

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ── 键归一 ─────────────────────────────────────────────────────────────────

// normKey 把键归一成可比形态：小写、去空白与分隔符、去括注。
//
//	"Root Cause" / "root_cause" / "rootCause" / "根本原因（root cause）" 都要
//	落到同一个归一形态，别名表才能一张表覆盖大小写/分隔符/括注的变体。
func normKey(key string) string {
	key = stripParenthetical(key)
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(key)) {
		if isKeySeparator(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// stripParenthetical 去掉括注内容：「建议修复（fix）」→「建议修复」。
func stripParenthetical(key string) string {
	if !strings.ContainsAny(key, "(（") {
		return key
	}
	var b strings.Builder
	depth := 0
	for _, r := range key {
		switch r {
		case '(', '（':
			depth++
		case ')', '）':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

func isKeySeparator(r rune) bool {
	switch r {
	case '_', '-', ':', '：', '/', '.', '@', '#':
		return true
	}
	return unicode.IsSpace(r)
}

// aliasSet 是一张别名表：归一形态 → 规范写法。
//
//	★ 为什么值要存规范写法：「layer_recheck」归一后下划线没了，闭集判据命中
//	之后还得**原样写回去**——只存归一形态就会把 layer_recheck 写成 layerrecheck。
type aliasSet map[string]string

func newAlias(items ...string) aliasSet {
	set := make(aliasSet, len(items))
	for _, it := range items {
		if nk := normKey(it); nk != "" {
			if _, dup := set[nk]; !dup {
				set[nk] = it
			}
		}
	}
	return set
}

func (s aliasSet) has(normalized string) bool {
	_, ok := s[normalized]
	return ok
}

// 别名表。★ 取的键名对着 245/154/211 的真实响应形态（见文件头）。
var (
	rootCauseKeys = newAlias(
		"root_cause", "rootCause", "root_cause_analysis", "primary_cause", "immediate_cause",
		"underlying_cause", "fundamental_cause", "main_cause", "根本原因", "根因", "根因分析",
		"主要原因", "问题根因", "问题原因", "核心问题",
	)
	// rootCauseTextKeys 是「根因容器」里最适合作根因正文的键（先出现者胜）。
	//
	//	★ 顺序 = 「解释性正文」在前、「短标签」在后：245 实测形态里根因对象是
	//	  {primary_signal: "context canceled", category: …, description: "<长解释>"}
	//	  ——要的是 description，不是那个 16 字符的信号名。
	rootCauseTextKeys = []string{
		"primary_cause", "immediate_cause", "root_cause", "interpretation",
		"technical_detail", "detailed_explanation", "explanation", "description",
		"assessment", "详细说明", "根因说明", "问题说明", "说明", "描述",
		"error", "primary_signal",
	}
	categoryKeys = newAlias(
		"error_category", "category", "classification", "root_cause_category",
		"错误分类", "错误类别", "问题分类", "问题类别", "分类", "根因类别",
	)
	falsePositiveKeys = newAlias(
		"is_false_positive", "false_positive", "isFalsePositive", "误报", "是否误报", "是否为误报",
	)
	confidenceKeys = newAlias(
		"confidence_score", "confidence", "confidence_level", "overall_confidence",
		"analysis_conclusion_confidence", "置信度", "可信度",
	)
	severityKeys   = newAlias("severity_level", "severity", "严重程度", "严重性")
	impactKeys     = newAlias("impact_scope", "impact", "影响范围")
	affectedKeys   = newAlias("affected_users_estimate", "affected_users", "受影响用户数", "影响用户数")
	ruleAdjustKeys = newAlias("rule_adjustment", "rule_adjustments", "rule_change", "rule_changes", "规则调整", "规则修改")
	codeChangeKeys = newAlias("code_changes", "code_change", "patch", "patches", "代码修改", "代码变更")
	fixPlanKeys    = newAlias("fix_plan", "remediation_plan", "action_plan", "plan", "修复计划", "处置计划", "行动计划")
	detailsKeys    = newAlias("analysis_details", "details", "分析详情", "详细分析")
	summaryKeys    = newAlias(
		"summary", "executive_summary", "conclusion", "overview", "结论", "总结", "概述", "小结",
		"总体结论", "分析结论", "问题概述", "摘要", "执行摘要",
	)
	suggestedFixKeys = newAlias(
		"suggested_fix", "suggested_fixes", "fix_suggestion", "fix_suggestions", "recommended_fix",
		"remediation", "recommendation", "修复建议", "建议修复", "处置建议", "修复方案", "处理方案", "建议措施",
	)
)

// 步骤族的键按**词根**判（列不全是常态）。
var (
	fixKeyTokens     = []string{"fix", "repair", "remediation", "recommendation", "action", "step", "measure", "suggestion", "mitigation", "措施", "建议", "步骤", "修复", "处置", "处理", "行动", "优化", "排查"}
	verifyTokens     = []string{"verif", "validat", "验证", "验收", "复测", "复核"}
	excludeTokens    = []string{"evidence", "factor", "reason", "trigger", "hypothesis", "risk", "finding", "indicator", "cause", "analysis", "证据", "原因", "因素", "风险", "发现", "假设", "分析"}
	stepTitleKeys    = []string{"action", "recommendation", "fix", "title", "step", "measure", "suggestion", "item", "task", "content", "建议", "措施", "动作", "修复", "内容", "处置"}
	stepDetailKeys   = []string{"detail", "details", "description", "expected_effect", "expected", "effect", "说明", "细节", "详情", "预期效果"}
	stepPriorityKeys = []string{"priority", "urgency", "level", "优先级", "紧急度", "级别"}
	stepKindKeys     = []string{"kind", "type", "category", "类别", "类型"}
	stepRiskKeys     = []string{"risk", "风险"}
)

func isVerifyListKey(nk string) bool {
	return containsAnyToken(nk, verifyTokens)
}

func isFixListKey(nk string) bool {
	if isVerifyListKey(nk) {
		return false
	}
	return containsAnyToken(nk, fixKeyTokens) && !containsAnyToken(nk, excludeTokens)
}

func containsAnyToken(s string, tokens []string) bool {
	for _, t := range tokens {
		if t != "" && strings.Contains(s, t) {
			return true
		}
	}
	return false
}

// ── 遍历与收获 ─────────────────────────────────────────────────────────────

// maxHarvestDepth 限制遍历深度（防模型输出极深嵌套时遍历不收敛）。
const maxHarvestDepth = 4

type harvested struct {
	rootCause    string
	category     string
	suggestedFix string
	severity     string
	impact       string
	summary      string

	isFalsePositive *bool
	confidence      *float64
	affected        *int

	ruleAdjustment any
	codeChanges    any
	fixPlanRaw     any
	detailsRaw     any
	structuredFix  any

	rootCauseObjs []any
	fixLists      []any
	verifyLists   []any

	primary *ordObj // root_cause 所在的对象
	top     *ordObj // 整份输出的顶层对象
}

func isContainer(v any) bool {
	switch v.(type) {
	case *ordObj, []any:
		return true
	}
	return false
}

// scalarText 把标量值转成文本（容器 ⇒ 空串，由调用方另行处理）。
func scalarText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

type harvestItem struct {
	v     any
	depth int
}

// harvestAnalysis 广度优先（浅层优先）遍历，按别名表认字段。
//
//	★ 为什么浅层优先：同一份输出里可能有多处同名键（例如 report.severity 与
//
//	findings[].severity）。浅层的是「整份分析的属性」，深层的是「某条子项的
//	属性」⇒ 先到先得 = 浅层优先。
func harvestAnalysis(top *ordObj) *harvested {
	h := &harvested{top: top}
	queue := []harvestItem{{top, 0}}
	seen := map[*ordObj]bool{}
	for qi := 0; qi < len(queue); qi++ {
		it := queue[qi]
		obj, ok := it.v.(*ordObj)
		if !ok || seen[obj] || it.depth > maxHarvestDepth {
			continue
		}
		seen[obj] = true
		for _, k := range obj.keys {
			h.take(normKey(k), obj.vals[k], obj, it.depth, &queue)
		}
	}
	return h
}

// take 处理一个 (归一键, 值)。
func (h *harvested) take(nk string, val any, owner *ordObj, depth int, queue *[]harvestItem) {
	if val == nil {
		return
	}
	enqueue := func() {
		*queue = append(*queue, harvestItem{val, depth + 1})
	}
	setScalar := func(dst *string) {
		if *dst == "" {
			*dst = scalarText(val)
		}
	}

	switch {
	case rootCauseKeys.has(nk):
		if isContainer(val) {
			h.rootCauseObjs = append(h.rootCauseObjs, val)
			enqueue()
		} else if h.rootCause == "" {
			if s := scalarText(val); s != "" {
				h.rootCause = s
				h.primary = owner
			}
		}

	case categoryKeys.has(nk):
		setScalar(&h.category)

	case falsePositiveKeys.has(nk):
		if h.isFalsePositive == nil {
			if b, ok := coerceBool(val); ok {
				h.isFalsePositive = &b
			}
		}

	case confidenceKeys.has(nk):
		if h.confidence == nil {
			if f, ok := coerceConfidence(val); ok {
				h.confidence = &f
			}
		}
		if isContainer(val) {
			enqueue() // 例：confidence: {mechanism, exact_source} ⇒ 进去找数值
		}

	case severityKeys.has(nk):
		setScalar(&h.severity)

	case impactKeys.has(nk):
		setScalar(&h.impact)

	case affectedKeys.has(nk):
		if h.affected == nil {
			if n, ok := coerceInt(val); ok {
				h.affected = &n
			}
		}

	case suggestedFixKeys.has(nk):
		if isContainer(val) {
			h.fixLists = append(h.fixLists, val)
			if h.structuredFix == nil {
				h.structuredFix = val
			}
		} else {
			setScalar(&h.suggestedFix)
		}

	case ruleAdjustKeys.has(nk):
		if h.ruleAdjustment == nil {
			h.ruleAdjustment = val
		}

	case codeChangeKeys.has(nk):
		if h.codeChanges == nil {
			h.codeChanges = val
		}

	case fixPlanKeys.has(nk):
		if _, ok := val.(*ordObj); ok && h.fixPlanRaw == nil {
			h.fixPlanRaw = val
		}

	case detailsKeys.has(nk):
		if isContainer(val) && h.detailsRaw == nil {
			h.detailsRaw = val
		}

	case summaryKeys.has(nk):
		if !isContainer(val) {
			setScalar(&h.summary)
		}

	default:
		if !isContainer(val) {
			return
		}
		if isVerifyListKey(nk) {
			h.verifyLists = append(h.verifyLists, val)
		} else if isFixListKey(nk) {
			h.fixLists = append(h.fixLists, val)
		}
		// 对象继续下钻（例：analysis_metadata.confidence / diagnosis.root_cause）；
		// 数组只在「步骤族」里才有意义，不进标量搜索（避免把子项的
		// severity 误当成整份分析的 severity）。
		if _, isObj := val.(*ordObj); isObj {
			enqueue()
		}
	}
}

// ── coercion ───────────────────────────────────────────────────────────────

var (
	boolTrueWords  = map[string]struct{}{"true": {}, "yes": {}, "y": {}, "是": {}, "误报": {}, "1": {}}
	boolFalseWords = map[string]struct{}{"false": {}, "no": {}, "n": {}, "否": {}, "不是误报": {}, "非误报": {}, "0": {}}
)

func coerceBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		if _, ok := boolTrueWords[s]; ok {
			return true, true
		}
		if _, ok := boolFalseWords[s]; ok {
			return false, true
		}
	case json.Number:
		f, err := t.Float64()
		return f != 0, err == nil
	}
	return false, false
}

// coerceConfidence 把置信度归一到 [0,1]。
//
//	★ 定性词**保守**映射：「高」只给 0.75——低于应用修复的 0.80 门 ⇒ 只能看、
//
//	不能一键应用。模型没给数字，就不替它拍板「高到可以自动执行」。
func coerceConfidence(v any) (float64, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return normalizeConfidenceScore(f), err == nil
	case float64:
		return normalizeConfidenceScore(t), true
	case string:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "%"))
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			if f > 1 && f <= 100 {
				f /= 100
			}
			return normalizeConfidenceScore(f), true
		}
		switch strings.ToLower(s) {
		case "高", "较高", "high", "very high":
			return 0.75, true
		case "中", "中等", "medium", "moderate":
			return 0.5, true
		case "低", "较低", "low", "very low":
			return 0.25, true
		}
	}
	return 0, false
}

func coerceInt(v any) (int, bool) {
	switch t := v.(type) {
	case json.Number:
		f, err := t.Float64()
		return int(f), err == nil
	case float64:
		return int(t), true
	case string:
		s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "%"))
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int(f), true
		}
	}
	return 0, false
}

// ── 文本渲染 ───────────────────────────────────────────────────────────────

// renderLoose 把容器值渲染成可读文本（键值对保序）。
func renderLoose(v any) string {
	var lines []string
	renderInto(&lines, v, 0)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func renderInto(lines *[]string, v any, depth int) {
	if depth > 6 {
		return
	}
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			*lines = append(*lines, s)
		}
	case json.Number, bool:
		*lines = append(*lines, scalarText(t))
	case *ordObj:
		for _, k := range t.keys {
			val := t.vals[k]
			if isContainer(val) {
				*lines = append(*lines, strings.Repeat("  ", depth)+k+"：")
				renderInto(lines, val, depth+1)
				continue
			}
			if s := scalarText(val); s != "" {
				*lines = append(*lines, strings.Repeat("  ", depth)+k+"："+s)
			}
		}
	case []any:
		for _, item := range t {
			if s, ok := item.(string); ok {
				if s = strings.TrimSpace(s); s != "" {
					*lines = append(*lines, strings.Repeat("  ", depth)+"• "+s)
				}
				continue
			}
			var sub []string
			renderInto(&sub, item, 0)
			if len(sub) > 0 {
				*lines = append(*lines, strings.Repeat("  ", depth)+"• "+strings.Join(sub, "；"))
			}
		}
	}
}

// renderRootCause 从「根因容器」里取正文。
//
//	后入队的是更具体的内层容器（例：root_cause_analysis → immediate_cause）
//	⇒ 从后往前找第一个能出正文的。
//
//	arraysToo=false 时只看**对象**容器：对象是「一个根因的解释」；数组是「多条
//	原因的清单」——清单的渲染结果交给调用方在摘要之后再用（摘要是一句话的
//	结论，更适合做根因正文；清单留在 model_output 里给管理面展开）。
func renderRootCause(objs []any, arraysToo bool) string {
	for i := len(objs) - 1; i >= 0; i-- {
		if !arraysToo {
			if _, isObj := objs[i].(*ordObj); !isObj {
				continue
			}
		}
		if s := rootCauseTextOf(objs[i]); s != "" {
			return s
		}
	}
	return ""
}

// rootCauseTextOf 从一个根因容器（对象 / 对象数组）里取正文。
//
//	① 对象：先看指定正文键；一个都没有 ⇒ 把全部直接字符串字段按「键：值」渲染
//	   （根因容器里已经是「关于根因」的内容，全量渲染也不会跑题）。
//	② 数组（多条原因；245 实测：[{id, category, title, description, possible_causes}]）：
//	   取前若干条的「标题：说明」各成一行。
func rootCauseTextOf(v any) string {
	switch t := v.(type) {
	case *ordObj:
		idx := t.normIndex()
		for _, tk := range rootCauseTextKeys {
			orig, ok := idx[normKey(tk)]
			if !ok {
				continue
			}
			if s := scalarText(t.vals[orig]); s != "" {
				return s
			}
		}
		var lines []string
		for _, k := range t.keys {
			if s := scalarText(t.vals[k]); s != "" {
				lines = append(lines, k+"："+s)
			}
		}
		return strings.Join(lines, "；")
	case []any:
		var lines []string
		for _, item := range t {
			obj, ok := item.(*ordObj)
			if !ok {
				continue
			}
			title, _ := fieldByAlias(obj, "title", "name", "标题", "cause", "原因")
			desc, _ := fieldByAlias(obj, "description", "detail", "说明", "描述", "解释")
			switch {
			case title != "" && desc != "":
				lines = append(lines, title+"："+desc)
			case title != "":
				lines = append(lines, title)
			case desc != "":
				lines = append(lines, desc)
			}
			if len(lines) >= 3 {
				break
			}
		}
		return strings.Join(lines, "\n")
	}
	return ""
}

// ── 修正方案 ───────────────────────────────────────────────────────────────

// FixPlan 是分析结论里「怎么修」的结构化部分（落在 analysis_details.fix_plan）。
//
//	★ 两类受众、一条边界：
//
//	  · auto_actions —— 可由系统执行的白名单动作（与 rule_adjustment 同源；
//	    是否真能执行由 admin.ApplyFix 的白名单 + 置信度门 + 类型闸决定）；
//	  · 其余全部是**给人看的**：人工步骤 / 配置变更 / 代码变更 / 部署与回滚。
//	    系统不会去改代码、改配置或部署——那些由人按方案执行。
type FixPlan struct {
	Summary       string         `json:"summary,omitempty"`
	AutoActions   []any          `json:"auto_actions,omitempty"`
	ManualSteps   []FixStep      `json:"manual_steps,omitempty"`
	ConfigChanges []ConfigChange `json:"config_changes,omitempty"`
	CodeChanges   []CodeChange   `json:"code_changes,omitempty"`
	Deploy        *DeployPlan    `json:"deploy,omitempty"`
	Verification  *VerifyPlan    `json:"verification,omitempty"`
}

// FixStep 是一条人工步骤。
type FixStep struct {
	Step   int    `json:"step"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	Kind   string `json:"kind"` // config | code | ops | upstream | client
	Risk   string `json:"risk"` // low | medium | high
}

// ConfigChange 是一处配置变更建议。
type ConfigChange struct {
	Target   string `json:"target"`
	Current  string `json:"current,omitempty"`
	Proposed string `json:"proposed"`
}

// CodeChange 是一处代码变更建议（供人落地；系统不自动执行）。
type CodeChange struct {
	File        string `json:"file,omitempty"`
	Description string `json:"description"`
	Patch       string `json:"patch,omitempty"`
}

// DeployPlan 是部署建议。
type DeployPlan struct {
	Required bool     `json:"required"`
	Targets  []string `json:"targets,omitempty"`
	Steps    []string `json:"steps,omitempty"`
	Rollback string   `json:"rollback,omitempty"`
}

// VerifyPlan 是验证建议（复测用什么手段、期望看到什么）。
type VerifyPlan struct {
	Method string   `json:"method,omitempty"` // rules_replay | layer_recheck | recurrence | request_replay | manual
	Expect string   `json:"expect,omitempty"`
	Steps  []string `json:"steps,omitempty"`
}

const (
	maxManualSteps     = 12
	maxStepTitleRunes  = 200
	maxStepDetailRunes = 800
	maxSummaryRunes    = 300
	maxPlanTargets     = 8
	maxPlanDeploySteps = 10
	maxPlanVerifySteps = 8
	maxPlanTextRunes   = 1200
	maxModelOutputJSON = 64 << 10
)

var (
	verifyMethods = newAlias("rules_replay", "layer_recheck", "recurrence", "request_replay", "manual")
	stepKindSet   = newAlias("config", "code", "ops", "upstream", "client")
	stepRiskSet   = newAlias("low", "medium", "high")
)

// rawStep 是收步骤时的中间形态。
type rawStep struct {
	title    string
	detail   string
	priority string
	kind     string
	risk     string
}

// collectSteps 把一份「步骤候选」（数组 / 分组对象）摊平成步骤列表。
func collectSteps(v any, out *[]rawStep) {
	switch t := v.(type) {
	case string:
		if s := strings.TrimSpace(t); s != "" {
			*out = append(*out, rawStep{title: s})
		}
	case *ordObj:
		if step, ok := stepFromObject(t); ok {
			*out = append(*out, step)
			return
		}
		// 分组对象（例：{immediate: [...], short_term: [...]}）或「受众 → 说明」
		// 对象（例：{客户端侧: "...", 运维侧: "..."}）：逐成员摊平。
		for _, k := range t.keys {
			val := t.vals[k]
			if isContainer(val) {
				collectSteps(val, out)
			} else if s := scalarText(val); s != "" {
				*out = append(*out, rawStep{title: k, detail: s})
			}
		}
	case []any:
		for _, item := range t {
			collectSteps(item, out)
		}
	}
}

// stepFromObject 把一个步骤对象读成中间形态。
//
//	★ 标题键先出现者胜；细节键进 detail；其余字符串字段以「键：值」并进 detail。
func stepFromObject(o *ordObj) (rawStep, bool) {
	idx := o.normIndex()
	pick := func(keys []string) (string, string) {
		for _, k := range keys {
			if orig, ok := idx[normKey(k)]; ok {
				if s := scalarText(o.vals[orig]); s != "" {
					return s, orig
				}
			}
		}
		return "", ""
	}
	title, titleOrig := pick(stepTitleKeys)
	if title == "" {
		return rawStep{}, false
	}
	step := rawStep{title: title}
	step.detail, _ = pick(stepDetailKeys)
	step.priority, _ = pick(stepPriorityKeys)
	step.kind, _ = pick(stepKindKeys)
	step.risk, _ = pick(stepRiskKeys)
	_, detailOrig := pick(stepDetailKeys)
	_, prioOrig := pick(stepPriorityKeys)
	_, kindOrig := pick(stepKindKeys)
	_, riskOrig := pick(stepRiskKeys)
	skip := map[string]bool{titleOrig: true, detailOrig: true, prioOrig: true, kindOrig: true, riskOrig: true}
	var extras []string
	for _, k := range o.keys {
		if skip[k] || isContainer(o.vals[k]) {
			continue
		}
		if s := scalarText(o.vals[k]); s != "" {
			extras = append(extras, k+"："+s)
		}
	}
	if len(extras) > 0 {
		if step.detail != "" {
			step.detail += "；"
		}
		step.detail += strings.Join(extras, "；")
	}
	return step, true
}

// buildSteps 把原始步骤收成 FixStep（编号、归类、截断、限量）。
func buildSteps(raw []rawStep) []FixStep {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxManualSteps {
		raw = raw[:maxManualSteps]
	}
	steps := make([]FixStep, 0, len(raw))
	for i, r := range raw {
		text := r.title + " " + r.detail
		kind := normalizeStepWord(r.kind, stepKindSet, "kind", "")
		if kind == "" {
			kind = classifyStep(text)
		}
		risk := normalizeStepWord(r.risk, stepRiskSet, "risk", "")
		if risk == "" {
			risk = riskOf(r.priority)
		}
		steps = append(steps, FixStep{
			Step:   i + 1,
			Title:  runeTruncate(strings.TrimSpace(r.title), maxStepTitleRunes),
			Detail: runeTruncate(strings.TrimSpace(r.detail), maxStepDetailRunes),
			Kind:   kind,
			Risk:   risk,
		})
	}
	return steps
}

// normalizeStepWord 把模型给的类别/风险词归一到闭集（不在集内 ⇒ fallback）。
//
//	domain: "kind" | "risk" | "method"——决定同义词往哪个闭集归。
func normalizeStepWord(word string, set aliasSet, domain, fallback string) string {
	nk := normKey(word)
	if canon, ok := set[nk]; ok {
		return canon
	}
	switch domain {
	case "kind":
		switch {
		case containsAnyToken(nk, []string{"配置", "config", "setting"}):
			return "config"
		case containsAnyToken(nk, []string{"代码", "code", "开发"}):
			return "code"
		case containsAnyToken(nk, []string{"上游", "upstream", "provider", "供应商"}):
			return "upstream"
		case containsAnyToken(nk, []string{"客户端", "client", "用户"}):
			return "client"
		}
	case "risk":
		switch {
		case containsAnyToken(nk, []string{"高", "high", "critical", "严重"}):
			return "high"
		case containsAnyToken(nk, []string{"中", "medium", "moderate"}):
			return "medium"
		case containsAnyToken(nk, []string{"低", "low", "minor"}):
			return "low"
		}
	}
	return fallback
}

// classifyStep 按措辞猜步骤类别（猜不出 ⇒ ops）。
func classifyStep(text string) string {
	lower := strings.ToLower(text)
	switch {
	case containsAnyToken(lower, []string{"代码", "函数", "补丁", "仓库", "源码", "pseudo", "patch", ".go", ".ts", ".vue", "code_hint", "code change", "实现", "重构"}):
		return "code"
	case containsAnyToken(lower, []string{"配置", "环境变量", "阈值", "超时", "开关", "参数", "settings", "config", "timeout", "限额", "上限", "ttl", "重试次数", "retry", "预算"}):
		return "config"
	case containsAnyToken(lower, []string{"上游", "供应商", "中转", "联系", "upstream", "provider", "vendor", "服务商"}):
		return "upstream"
	case containsAnyToken(lower, []string{"客户端", "重试该请求", "更换模型", "切换模型", "client", "sdk", "用户侧"}):
		return "client"
	}
	return "ops"
}

// riskOf 把优先级措辞归一成 low/medium/high（猜不出 ⇒ low）。
//
//	数字优先级按「数值越小越紧急」折算：0 ⇒ high，1 ⇒ medium，其余 ⇒ low。
func riskOf(priority string) string {
	p := strings.ToLower(strings.TrimSpace(priority))
	if n, err := strconv.Atoi(p); err == nil {
		switch {
		case n <= 0:
			return "high"
		case n == 1:
			return "medium"
		}
		return "low"
	}
	switch {
	case p == "p0" || containsAnyToken(p, []string{"critical", "high", "高", "紧急", "严重"}):
		return "high"
	case p == "p1" || containsAnyToken(p, []string{"medium", "中"}):
		return "medium"
	}
	return "low"
}

// buildFixPlan 收成结构化修正方案。
//
//	模型按新契约给了 fix_plan ⇒ 读它的显式部分；没给（或给得不全）⇒ 用
//	收获到的建议列表补齐。两条路出来的形状完全一样。
func buildFixPlan(h *harvested) *FixPlan {
	plan := &FixPlan{}
	if raw, ok := h.fixPlanRaw.(*ordObj); ok {
		readFixPlan(raw, plan)
	}
	if len(plan.ManualSteps) == 0 {
		var raw []rawStep
		for _, l := range h.fixLists {
			collectSteps(l, &raw)
		}
		plan.ManualSteps = buildSteps(raw)
	}
	if plan.Summary == "" {
		plan.Summary = firstSentence(h.suggestedFix, maxSummaryRunes)
	}
	if len(plan.AutoActions) == 0 {
		plan.AutoActions = autoActionsOf(h.ruleAdjustment)
	}
	if len(plan.CodeChanges) == 0 {
		plan.CodeChanges = codeChangesOf(h.codeChanges)
	}
	if plan.Verification == nil {
		plan.Verification = &VerifyPlan{Method: "recurrence"}
	}
	if len(plan.Verification.Steps) == 0 {
		plan.Verification.Steps = flattenStrings(h.verifyLists, maxPlanVerifySteps, maxPlanTextRunes)
		if len(plan.Verification.Steps) > 0 && plan.Verification.Method == "recurrence" {
			plan.Verification.Method = "manual"
		}
	}
	needsDeploy := len(plan.CodeChanges) > 0
	for _, s := range plan.ManualSteps {
		if s.Kind == "code" {
			needsDeploy = true
		}
	}
	if plan.Deploy == nil && needsDeploy {
		plan.Deploy = &DeployPlan{Required: true}
	}
	if plan.Deploy != nil {
		plan.Deploy.Required = plan.Deploy.Required || needsDeploy
	}
	if plan.Summary == "" && h.summary != "" {
		plan.Summary = firstSentence(h.summary, maxSummaryRunes)
	}
	if isEmptyPlan(plan) {
		return nil
	}
	return plan
}

func isEmptyPlan(p *FixPlan) bool {
	return p.Summary == "" && len(p.AutoActions) == 0 && len(p.ManualSteps) == 0 &&
		len(p.ConfigChanges) == 0 && len(p.CodeChanges) == 0 && p.Deploy == nil
}

// readFixPlan 读模型按契约给的 fix_plan 对象。
func readFixPlan(o *ordObj, plan *FixPlan) {
	idx := o.normIndex()
	pickRaw := func(keys ...string) (any, bool) {
		for _, k := range keys {
			if orig, ok := idx[normKey(k)]; ok {
				v := o.vals[orig]
				if v != nil {
					return v, true
				}
			}
		}
		return nil, false
	}
	if v, ok := pickRaw("summary", "摘要", "概述"); ok {
		plan.Summary = runeTruncate(scalarText(v), maxSummaryRunes)
	}
	if v, ok := pickRaw("auto_actions", "自动动作", "自动修正"); ok {
		if arr, ok := v.([]any); ok {
			plan.AutoActions = arr
		}
	}
	if v, ok := pickRaw("manual_steps", "steps", "人工步骤", "步骤"); ok {
		var raw []rawStep
		collectSteps(v, &raw)
		plan.ManualSteps = buildSteps(raw)
	}
	if v, ok := pickRaw("config_changes", "配置变更"); ok {
		plan.ConfigChanges = readConfigChanges(v)
	}
	if v, ok := pickRaw("code_changes", "代码变更"); ok {
		plan.CodeChanges = readCodeChanges(v)
	}
	if v, ok := pickRaw("deploy", "部署"); ok {
		plan.Deploy = readDeployPlan(v)
	}
	if v, ok := pickRaw("verification", "verify", "验证"); ok {
		plan.Verification = readVerifyPlan(v)
	}
}

func fieldByAlias(o *ordObj, keys ...string) (string, bool) {
	idx := o.normIndex()
	for _, k := range keys {
		if orig, ok := idx[normKey(k)]; ok {
			if s := scalarText(o.vals[orig]); s != "" {
				return s, true
			}
		}
	}
	return "", false
}

func readConfigChanges(v any) []ConfigChange {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]ConfigChange, 0, len(arr))
	for _, item := range arr {
		switch it := item.(type) {
		case string:
			if s := strings.TrimSpace(it); s != "" {
				out = append(out, ConfigChange{Target: runeTruncate(s, maxStepTitleRunes)})
			}
		case *ordObj:
			target, _ := fieldByAlias(it, "target", "key", "path", "name", "目标", "配置项")
			current, _ := fieldByAlias(it, "current", "old", "before", "当前值")
			proposed, _ := fieldByAlias(it, "proposed", "new", "value", "after", "建议值")
			if target == "" && proposed == "" {
				continue
			}
			out = append(out, ConfigChange{
				Target:   runeTruncate(target, maxStepTitleRunes),
				Current:  runeTruncate(current, maxStepDetailRunes),
				Proposed: runeTruncate(proposed, maxStepDetailRunes),
			})
		}
	}
	return out
}

func readCodeChanges(v any) []CodeChange {
	var out []CodeChange
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if obj, ok := item.(*ordObj); ok {
				if cc, ok := codeChangeFromObject(obj); ok {
					out = append(out, cc)
				}
			}
		}
	case *ordObj:
		for _, k := range t.keys {
			switch item := t.vals[k].(type) {
			case *ordObj:
				cc, ok := codeChangeFromObject(item)
				if !ok {
					continue
				}
				out = append(out, cc)
			default:
				if s := renderLoose(item); s != "" {
					out = append(out, CodeChange{Description: runeTruncate(k+"："+s, maxPlanTextRunes)})
				}
			}
		}
	}
	return out
}

func codeChangeFromObject(o *ordObj) (CodeChange, bool) {
	file, _ := fieldByAlias(o, "file", "path", "文件")
	desc, _ := fieldByAlias(o, "description", "change", "summary", "说明", "改动")
	patch, _ := fieldByAlias(o, "patch", "diff", "snippet", "pseudo_code", "补丁")
	if file == "" && desc == "" && patch == "" {
		return CodeChange{}, false
	}
	return CodeChange{
		File:        runeTruncate(file, maxStepTitleRunes),
		Description: runeTruncate(desc, maxPlanTextRunes),
		Patch:       runeTruncate(patch, maxModelOutputJSON),
	}, true
}

func readDeployPlan(v any) *DeployPlan {
	obj, ok := v.(*ordObj)
	if !ok {
		return nil
	}
	dp := &DeployPlan{}
	if s, ok := fieldByAlias(obj, "required", "need_deploy", "需要部署"); ok {
		dp.Required = coerceBoolWord(s)
	}
	if orig, ok := obj.normIndex()[normKey("targets")]; ok {
		dp.Targets = flattenStrings(obj.vals[orig], maxPlanTargets, maxStepTitleRunes)
	}
	if orig, ok := obj.normIndex()[normKey("steps")]; ok {
		dp.Steps = flattenStrings(obj.vals[orig], maxPlanDeploySteps, maxPlanTextRunes)
	}
	dp.Rollback, _ = fieldByAlias(obj, "rollback", "回滚")
	dp.Rollback = runeTruncate(dp.Rollback, maxPlanTextRunes)
	return dp
}

func readVerifyPlan(v any) *VerifyPlan {
	vp := &VerifyPlan{}
	switch t := v.(type) {
	case []any:
		vp.Method = "manual"
		vp.Steps = flattenStrings(t, maxPlanVerifySteps, maxPlanTextRunes)
	case string:
		vp.Method = "manual"
		vp.Expect = runeTruncate(strings.TrimSpace(t), maxPlanTextRunes)
	case *ordObj:
		if s, ok := fieldByAlias(t, "method", "方式"); ok {
			vp.Method = normalizeStepWord(s, verifyMethods, "method", "manual")
		}
		if s, ok := fieldByAlias(t, "expect", "expected", "期望", "预期"); ok {
			vp.Expect = runeTruncate(s, maxPlanTextRunes)
		}
		if orig, ok := t.normIndex()[normKey("steps")]; ok {
			vp.Steps = flattenStrings(t.vals[orig], maxPlanVerifySteps, maxPlanTextRunes)
		}
		if vp.Method == "" {
			vp.Method = "manual"
		}
	default:
		return nil
	}
	return vp
}

func coerceBoolWord(s string) bool {
	b, ok := coerceBool(s)
	return ok && b
}

// flattenStrings 把数组/分组对象里的字符串摊平（限量、截断）。
func flattenStrings(v any, maxItems, maxRunes int) []string {
	var out []string
	var walk func(x any)
	walk = func(x any) {
		switch t := x.(type) {
		case string:
			if s := strings.TrimSpace(t); s != "" && len(out) < maxItems {
				out = append(out, runeTruncate(s, maxRunes))
			}
		case *ordObj:
			for _, k := range t.keys {
				walk(t.vals[k])
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

// autoActionsOf 把 rule_adjustment 归成数组形态（供展示；执行仍走 admin 白名单）。
func autoActionsOf(v any) []any {
	switch t := v.(type) {
	case []any:
		return t
	case *ordObj:
		for _, k := range t.keys {
			if arr, ok := t.vals[k].([]any); ok {
				return arr
			}
		}
		return []any{t}
	}
	return nil
}

// codeChangesOf 把顶层 code_changes（任意形状）转成 CodeChange 列表。
func codeChangesOf(v any) []CodeChange {
	if v == nil {
		return nil
	}
	return readCodeChanges(v)
}

// firstSentence 取首句（到第一个句号/分号为止并**保留句末标点**；换行不计入）。
func firstSentence(s string, max int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexAny(s, "。\n；;"); i >= 0 {
		_, size := utf8.DecodeRuneInString(s[i:])
		if s[i] == '\n' {
			s = s[:i]
		} else {
			s = s[:i+size]
		}
	}
	return runeTruncate(strings.TrimSpace(s), max)
}
