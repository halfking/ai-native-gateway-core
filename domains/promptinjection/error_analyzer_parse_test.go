package promptinjection

import (
	"encoding/json"
	"strings"
	"testing"
)

// error_analyzer_parse_test.go — LLM 分析输出容错解析的钉测（2026-11-25）。
//
// ★ 夹具对着三台生产的**真实失败形态**写（键结构逐字对应，正文换成合成内容）：
//
//	211：suggested_fix 是对象 / 输出被截断；
//	245/154：字段散落在嵌套报告体例里（analysis_report / diagnosis / 中文键）、
//	         字符串里有裸换行、单成员语法错、输出被截断。
//
// 每个夹具都断言「读出了什么」+「怎么读出来的」（ParseDiagnostics）。

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

func detailsOf(t *testing.T, r *AnalysisResult) map[string]any {
	t.Helper()
	if r == nil {
		t.Fatalf("result is nil")
	}
	return r.AnalysisDetails
}

func fixPlanOf(t *testing.T, r *AnalysisResult) *FixPlan {
	t.Helper()
	raw, ok := detailsOf(t, r)["fix_plan"]
	if !ok {
		t.Fatalf("analysis_details 里没有 fix_plan: %s", mustJSON(t, r.AnalysisDetails))
	}
	plan, ok := raw.(*FixPlan)
	if !ok {
		t.Fatalf("fix_plan 类型 = %T, want *FixPlan", raw)
	}
	return plan
}

// ── 规范形态：行为与旧解析器逐字段等价 ────────────────────────────────────

func TestParseAnalysisResponse_CanonicalIsUnchanged(t *testing.T) {
	response := `{
		"root_cause": "LLM service timeout",
		"error_category": "timeout",
		"is_false_positive": false,
		"confidence_score": 0.85,
		"suggested_fix": "Increase timeout from 30s to 60s",
		"rule_adjustment": {"timeout_sec": 60},
		"code_changes": {"file": "config.yaml", "change": "timeout: 60"},
		"severity_level": "medium",
		"impact_scope": "tenant",
		"affected_users_estimate": 10,
		"analysis_details": {"recommendation": "adjust timeout"}
	}`
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("canonical 响应解析失败: %+v", diag)
	}
	if diag.Mode != ParseModeCanonical {
		t.Errorf("mode = %q, want canonical", diag.Mode)
	}
	if r.RootCause != "LLM service timeout" || r.ErrorCategory != "timeout" || r.IsFalsePositive ||
		r.ConfidenceScore != 0.85 || r.SuggestedFix != "Increase timeout from 30s to 60s" ||
		r.SeverityLevel != "medium" || r.ImpactScope != "tenant" || r.AffectedUsersEstimate != 10 {
		t.Errorf("字段读错: %+v", r)
	}
	if r.RuleAdjustment["timeout_sec"] != int64(60) {
		t.Errorf("rule_adjustment = %v", r.RuleAdjustment)
	}
	if r.CodeChanges["file"] != "config.yaml" {
		t.Errorf("code_changes = %v", r.CodeChanges)
	}
	// 模型给的 analysis_details 原样保留（与旧行为一致）。
	if detailsOf(t, r)["recommendation"] != "adjust timeout" {
		t.Errorf("analysis_details 丢了模型给的内容: %v", r.AnalysisDetails)
	}
	// 规范形态不写 parse_diagnostics、不留 model_output。
	if _, ok := detailsOf(t, r)["parse_diagnostics"]; ok {
		t.Errorf("规范形态不应带 parse_diagnostics")
	}
	if _, ok := detailsOf(t, r)["model_output"]; ok {
		t.Errorf("规范形态不应带 model_output")
	}
}

// ── 真实形态 ①：字段散落在嵌套报告体例里 ─────────────────────────────────

// 245 ROW 9：analysis{…root_cause…} + repair_recommendations[] + 行动列表。
func TestParseAnalysisResponse_NestedAnalysisReport(t *testing.T) {
	response := "```json\n" + `{
  "analysis": {
    "error_type": "canary_detection_failed",
    "tenant_id": "default",
    "severity": "medium",
    "root_cause": "请求上下文在检测完成前被取消",
    "failure_reasons": [{"reason": "客户端断开", "probability": "高"}]
  },
  "repair_recommendations": [
    {"priority": 1, "category": "detect", "action": "识别调用方取消", "detail": "把 context.Canceled 与真实失败分开", "code_hint": "errors.Is(err, context.Canceled)"},
    {"priority": 2, "category": "policy", "action": "调整失败策略", "detail": "对调用方取消不记失败"}
  ],
  "immediate_actions": ["确认是否为客户端主动断开"],
  "long_term_actions": ["为取消类失败补充观测指标"]
}` + "\n```"

	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	if diag.Mode != ParseModeNested {
		t.Errorf("mode = %q, want nested", diag.Mode)
	}
	if r.RootCause != "请求上下文在检测完成前被取消" {
		t.Errorf("root_cause = %q", r.RootCause)
	}
	if r.SeverityLevel != "medium" {
		t.Errorf("severity = %q, want medium", r.SeverityLevel)
	}
	// 建议正文 = 三组列表按原顺序渲染。
	fix := r.SuggestedFix
	for _, want := range []string{"识别调用方取消", "调整失败策略", "确认是否为客户端主动断开", "为取消类失败补充观测指标"} {
		if !strings.Contains(fix, want) {
			t.Errorf("suggested_fix 缺「%s」:\n%s", want, fix)
		}
	}
	if strings.Index(fix, "识别调用方取消") > strings.Index(fix, "确认是否为客户端主动断开") {
		t.Errorf("建议顺序被打乱（应保持模型输出的先后）:\n%s", fix)
	}

	plan := fixPlanOf(t, r)
	if len(plan.ManualSteps) != 4 {
		t.Fatalf("manual_steps = %d, want 4: %+v", len(plan.ManualSteps), plan.ManualSteps)
	}
	first := plan.ManualSteps[0]
	if first.Step != 1 || first.Title != "识别调用方取消" || first.Risk != "medium" || first.Kind != "code" {
		t.Errorf("首步读错: %+v", first)
	}
	if !strings.Contains(first.Detail, "把 context.Canceled 与真实失败分开") {
		t.Errorf("首步细节丢了: %q", first.Detail)
	}
	if plan.Deploy == nil || !plan.Deploy.Required {
		t.Errorf("含代码步骤时应标记需要部署: %+v", plan.Deploy)
	}
	// 原始结构整体留档，管理面可展开完整报告。
	if _, ok := detailsOf(t, r)["model_output"]; !ok {
		t.Errorf("嵌套形态应留 model_output")
	}
}

// 245 ROW 7：根因在 root_cause_analysis.immediate_cause{error, explanation} 里。
func TestParseAnalysisResponse_NestedRootCauseChain(t *testing.T) {
	response := `{
  "analysis_report": {
    "error_type": "canary_detection_failed",
    "severity": "low",
    "summary": "检测查询被调用方取消，不构成检测系统故障。",
    "root_cause_analysis": {
      "immediate_cause": {"error": "context canceled", "explanation": "请求在检测完成前被取消"},
      "alternative_hypothesis": "也可能是内部超时"
    },
    "evidence_chain": ["检测层状态为 canary=false", "重试次数为零"],
    "repair_recommendations": [
      {"priority": "P1", "recommendation": "区分调用方取消与真实失败", "expected_effect": "减少无效告警"}
    ],
    "verification_steps": ["重新发起同一请求", "确认不再入队"]
  }
}`
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	// 取根因容器里的**解释性正文**（explanation），不是 16 字符的信号名，
	// 更不是同级的 alternative_hypothesis。
	if r.RootCause != "请求在检测完成前被取消" {
		t.Errorf("root_cause = %q, want immediate_cause.explanation", r.RootCause)
	}
	if r.SeverityLevel != "low" {
		t.Errorf("severity = %q", r.SeverityLevel)
	}
	if !strings.Contains(r.SuggestedFix, "区分调用方取消与真实失败") {
		t.Errorf("suggested_fix = %q", r.SuggestedFix)
	}
	plan := fixPlanOf(t, r)
	if plan.Summary != "检测查询被调用方取消，不构成检测系统故障。" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if plan.Verification == nil || len(plan.Verification.Steps) != 2 || plan.Verification.Method != "manual" {
		t.Errorf("verification 读错: %+v", plan.Verification)
	}
}

// 245 ROW 8：全中文键。
func TestParseAnalysisResponse_NestedChineseKeys(t *testing.T) {
	response := "以下为分析结果：\n```json\n" + `{
  "分析结果": {
    "错误类型": "policy_load_failed",
    "严重程度": "中",
    "根因分析": {
      "原始错误": "context canceled",
      "根因类别": "调用方取消",
      "详细说明": "加载策略时请求已被取消，属于调用方行为。",
      "可能原因列表": [{"原因": "客户端断开"}]
    },
    "影响评估": {"直接影响": "该请求未执行检测"},
    "修复建议": {
      "立即措施": ["确认客户端是否主动断开"],
      "短期修复": [{"措施": "取消类失败不计入故障统计", "细节": "按错误类型过滤"}],
      "长期优化": ["补充取消原因观测"]
    },
    "排查步骤": ["核对请求日志中的断开时机"]
  }
}` + "\n```\n"
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	if r.RootCause != "加载策略时请求已被取消，属于调用方行为。" {
		t.Errorf("root_cause = %q, want 详细说明", r.RootCause)
	}
	if r.ErrorCategory != "调用方取消" {
		t.Errorf("category = %q", r.ErrorCategory)
	}
	plan := fixPlanOf(t, r)
	if len(plan.ManualSteps) != 4 {
		t.Fatalf("manual_steps = %d, want 4: %+v", len(plan.ManualSteps), plan.ManualSteps)
	}
	if plan.ManualSteps[0].Title != "确认客户端是否主动断开" || plan.ManualSteps[1].Title != "取消类失败不计入故障统计" {
		t.Errorf("中文键步骤读错: %+v", plan.ManualSteps)
	}
}

// 245 ROW 12 / 13：置信度是定性词；根因在 root_cause_analysis.primary_cause / diagnosis.root_cause。
func TestParseAnalysisResponse_QualitativeConfidenceIsConservative(t *testing.T) {
	row12 := `{
  "analysis_metadata": {"error_type": "canary_detection_failed", "confidence": "高", "severity": "high"},
  "root_cause_analysis": {"primary_cause": "调用方取消", "technical_detail": "ctx 在查询期间被取消"},
  "fix_recommendations": [{"priority": "P0", "fix": "停止把调用方取消记为检测失败"}]
}`
	r, _ := parseAnalysisResponse(row12)
	if r == nil {
		t.Fatalf("解析失败")
	}
	if r.RootCause != "调用方取消" || r.SeverityLevel != "high" {
		t.Errorf("读错: %+v", r)
	}
	if r.ConfidenceScore != 0.75 {
		t.Errorf("定性「高」应保守映射为 0.75（低于 0.80 应用门），得 %v", r.ConfidenceScore)
	}
	if plan := fixPlanOf(t, r); plan.ManualSteps[0].Risk != "high" || plan.ManualSteps[0].Title != "停止把调用方取消记为检测失败" {
		t.Errorf("步骤读错: %+v", plan.ManualSteps)
	}

	row13 := `{
  "diagnosis": {"root_cause": "请求取消导致加载中断", "root_cause_category": "canceled"},
  "remediation": {"immediate_actions": [{"priority": "P2", "action": "核对客户端行为", "detail": "查看断开时机"}]}
}`
	r13, _ := parseAnalysisResponse(row13)
	if r13 == nil || r13.RootCause != "请求取消导致加载中断" || r13.ErrorCategory != "canceled" {
		t.Errorf("diagnosis 形态读错: %+v", r13)
	}
}

// ── 真实形态 ②：suggested_fix 是对象（211 实测）─────────────────────────

func TestParseAnalysisResponse_SuggestedFixObject(t *testing.T) {
	response := "```json\n" + `{
  "root_cause": "上游流式响应在输出起始阶段即被中断，守卫对残缺参数判非法。",
  "error_category": "upstream_interrupted",
  "is_false_positive": false,
  "confidence_score": 0.85,
  "suggested_fix": {
    "客户端侧": "可直接重试该请求。",
    "运维侧": "按请求编号核对请求日志，确认上游流是否被提前掐断。"
  },
  "rule_adjustment": null,
  "severity_level": "low",
  "impact_scope": "session",
  "affected_users_estimate": 1,
  "analysis_details": {"attribution_basis": ["参数仅两字节"], "request_context": {"truncated_prefix_bytes": 2}}
}` + "\n```"
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag) // 旧解析器在这里整条丢弃
	}
	if diag.Mode != ParseModeNested {
		t.Errorf("mode = %q, want nested（suggested_fix 是对象）", diag.Mode)
	}
	if r.RootCause == "" || r.ConfidenceScore != 0.85 || r.ErrorCategory != "upstream_interrupted" {
		t.Errorf("标量字段读错: %+v", r)
	}
	if !strings.Contains(r.SuggestedFix, "客户端侧：可直接重试该请求。") || !strings.Contains(r.SuggestedFix, "运维侧：") {
		t.Errorf("对象型建议未渲染成文本: %q", r.SuggestedFix)
	}
	if detailsOf(t, r)["attribution_basis"] == nil {
		t.Errorf("模型给的 analysis_details 丢了: %v", r.AnalysisDetails)
	}
	if _, ok := detailsOf(t, r)["suggested_fix_structured"]; !ok {
		t.Errorf("结构化建议没有留档")
	}
	plan := fixPlanOf(t, r)
	if len(plan.ManualSteps) != 2 || plan.ManualSteps[0].Title != "客户端侧" {
		t.Errorf("分受众建议应收成两条步骤: %+v", plan.ManualSteps)
	}
}

// ── 真实形态 ③：输出被截断 ───────────────────────────────────────────────

func TestParseAnalysisResponse_TruncatedOutputIsSalvaged(t *testing.T) {
	// 围栏没闭合、最后一个成员在字符串中途断掉（211 ROW 1 / 245 ROW 2·6·10·11）。
	response := "```json\n" + `{
  "root_cause": "LLM 判官调用超时，fail-closed 策略拦截了请求。",
  "error_category": "timeout",
  "is_false_positive": false,
  "confidence_score": 0.92,
  "suggested_fix": "1) 放宽判官超时预算；2) 增加熔断；3) 规则层兜底",
  "severity_level": "high",
  "impact_scope": "tenant",
  "analysis_details": {"note": "输出在中途被截断，本成员不完整",
  "patch": {"file": "a.go", "diff": "unfinished`
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("截断输出一条都没救回来: %+v", diag)
	}
	if diag.Mode != ParseModeSalvaged || !diag.Truncated {
		t.Errorf("应为 salvaged+truncated，得 %+v", diag)
	}
	if r.RootCause != "LLM 判官调用超时，fail-closed 策略拦截了请求。" || r.SuggestedFix == "" || r.SeverityLevel != "high" {
		t.Errorf("完整成员没救回来: %+v", r)
	}
	// ★ 截断的分析不完整 ⇒ 置信度封顶 0.79（低于 0.80 应用门）⇒ 只能看。
	if r.ConfidenceScore != salvagedConfidenceCap {
		t.Errorf("截断结果置信度 = %v, want 封顶 %v", r.ConfidenceScore, salvagedConfidenceCap)
	}
	// 没救出来的成员要如实点名（以路径形态：截断点在 analysis_details 里面）。
	if len(diag.LostKeys) == 0 || !strings.HasPrefix(diag.LostKeys[0], "analysis_details") {
		t.Errorf("丢失成员未如实登记: %v", diag.LostKeys)
	}
	pd, _ := detailsOf(t, r)["parse_diagnostics"].(map[string]any)
	if pd == nil || pd["truncated"] != true {
		t.Errorf("parse_diagnostics 未落档: %v", detailsOf(t, r)["parse_diagnostics"])
	}
}

// 压缩成单行、在字符串中途断掉（没有行锚点 ⇒ 顺序流式解码兜底）。
func TestParseAnalysisResponse_TruncatedMinifiedJSON(t *testing.T) {
	response := `{"root_cause":"压缩输出的根因","error_category":"timeout","confidence_score":0.8,"suggested_fix":"这条建议写到一半就`
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("单行截断输出一条都没救回来: %+v", diag)
	}
	if diag.Mode != ParseModeSalvaged || !diag.Truncated {
		t.Errorf("want salvaged+truncated, got %+v", diag)
	}
	if r.RootCause != "压缩输出的根因" || r.ErrorCategory != "timeout" {
		t.Errorf("读错: %+v", r)
	}
	if r.ConfidenceScore != salvagedConfidenceCap {
		t.Errorf("置信度应被封顶: %v", r.ConfidenceScore)
	}
}

// ── 真实形态 ④：字符串里有裸换行 / 单成员语法错 ─────────────────────────

func TestParseAnalysisResponse_RawNewlineInsideString(t *testing.T) {
	// 245 ROW 4：值里有裸换行（JSON 不合法），且末尾成员的引号不配对。
	response := "```json\n" + `{
  "根因分析": {
    "根因类别": "调用方取消",
    "详细说明": "第一行说明
第二行说明"
  },
  "修复建议": {"立即措施": ["确认客户端行为"]},
  "注意事项": "本分析仅基于错误消息本身。'
}` + "\n```"
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	if !diag.Repaired {
		t.Errorf("裸换行需要修复才合法，应标 repaired: %+v", diag)
	}
	if !strings.Contains(r.RootCause, "第一行说明") {
		t.Errorf("root_cause = %q", r.RootCause)
	}
	// 建议成员在坏点**之前** ⇒ 必须救回来。
	if !strings.Contains(r.SuggestedFix, "确认客户端行为") {
		t.Errorf("坏点之前的成员丢了: %q", r.SuggestedFix)
	}
}

func TestParseAnalysisResponse_OneBadMemberDoesNotPoisonTheRest(t *testing.T) {
	// 245 ROW 5：中间某个成员有语法错（这里让 risk_assessment 缺逗号）。
	response := "```json\n" + `{
  "analysis": {
    "error_type": "canary_detection_failed",
    "root_cause": "检测查询在执行中被调用方取消",
    "severity": "medium",
    "risk_assessment": {"fail_open_risk": true "risk_description": "少了逗号"},
    "overall_confidence": 0.88
  },
  "repair_recommendations": [{"priority": "P1", "recommendation": "取消类失败不重试"}]
}` + "\n```"
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("一个坏成员让整篇作废: %+v", diag)
	}
	// 模型给的置信度是 0.88，但这份文档有成员丢失 ⇒ 按纪律封顶 0.79（只能看）。
	if r.RootCause != "检测查询在执行中被调用方取消" || r.SeverityLevel != "medium" || r.ConfidenceScore != salvagedConfidenceCap {
		t.Errorf("坏点两侧的完整成员没全部救回: %+v", r)
	}
	if !strings.Contains(r.SuggestedFix, "取消类失败不重试") {
		t.Errorf("坏点之后的成员丢了: %q", r.SuggestedFix)
	}
	// 坏成员在嵌套层里（analysis.risk_assessment…）⇒ 以路径形态登记。
	found := false
	for _, k := range diag.LostKeys {
		if strings.Contains(k, "analysis.risk_assessment") {
			found = true
		}
	}
	if !found {
		t.Errorf("坏成员未如实登记: %v", diag.LostKeys)
	}
}

// ── 自由文本兜底与失败 ───────────────────────────────────────────────────

func TestParseAnalysisResponse_ProseFallbackIsViewOnly(t *testing.T) {
	prose := strings.Repeat("根因是判官端点在高峰期超时，建议放宽超时并补充规则层兜底。", 3)
	r, diag := parseAnalysisResponse(prose)
	if r == nil {
		t.Fatalf("长自由文本应按兜底保留，而不是丢弃: %+v", diag)
	}
	if diag.Mode != ParseModeUnstructured || r.ConfidenceScore != 0 {
		t.Errorf("自由文本兜底必须 unstructured 且置信度 0: mode=%q conf=%v", diag.Mode, r.ConfidenceScore)
	}
	if !strings.Contains(r.SuggestedFix, "建议放宽超时") {
		t.Errorf("原文没保留: %q", r.SuggestedFix)
	}
}

func TestParseAnalysisResponse_FailsOnlyWhenNothingIsRecoverable(t *testing.T) {
	cases := map[string]string{
		"empty":              "",
		"whitespace":         "   \n\t ",
		"short prose":        "This is just text without JSON",
		"malformed JSON":     "{invalid json}",
		"nothing but a tag":  `{"error_category": "test"}`,
		"unterminated fence": "```json\n{",
	}
	for name, resp := range cases {
		t.Run(name, func(t *testing.T) {
			r, diag := parseAnalysisResponse(resp)
			if r != nil {
				t.Fatalf("应判失败却产出了结果: %+v", r)
			}
			if diag.Mode != ParseModeFailed {
				t.Errorf("mode = %q, want failed", diag.Mode)
			}
		})
	}
}

// ── coercion ──────────────────────────────────────────────────────────────

func TestCoerceConfidence(t *testing.T) {
	cases := []struct {
		in    any
		want  float64
		valid bool
	}{
		{json.Number("0.9"), 0.9, true},
		{json.Number("0"), 0, true},
		{json.Number("1.7"), 1, true}, // 越界归一
		{"0.85", 0.85, true},
		{"85%", 0.85, true},
		{"85", 0.85, true}, // 百分数口径
		{"高", 0.75, true},  // 定性词保守映射（< 0.80 应用门）
		{"High", 0.75, true},
		{"中", 0.5, true},
		{"低", 0.25, true},
		{"大概吧", 0, false},
		{[]any{}, 0, false},
	}
	for _, c := range cases {
		got, ok := coerceConfidence(c.in)
		if ok != c.valid || got != c.want {
			t.Errorf("coerceConfidence(%v) = (%v, %v), want (%v, %v)", c.in, got, ok, c.want, c.valid)
		}
	}
}

func TestCoerceBoolAndInt(t *testing.T) {
	if b, ok := coerceBool("是"); !ok || !b {
		t.Errorf(`"是" ⇒ (%v, %v), want (true, true)`, b, ok)
	}
	if b, ok := coerceBool("否"); !ok || b {
		t.Errorf(`"否" ⇒ (%v, %v), want (false, true)`, b, ok)
	}
	if _, ok := coerceBool("也许"); ok {
		t.Errorf(`"也许" 不应被当成布尔`)
	}
	if n, ok := coerceInt("12"); !ok || n != 12 {
		t.Errorf(`coerceInt("12") = (%d, %v)`, n, ok)
	}
}

// ── rule_adjustment / fix_plan 的载体与镜像 ─────────────────────────────

func TestParseAnalysisResponse_RuleAdjustmentArrayIsCarried(t *testing.T) {
	// 模型把规则调整写成数组——旧解析器在这里「数组解不进 map」整条失败。
	response := `{"root_cause": "规则过宽", "confidence_score": 0.9,
	  "rule_adjustment": [{"action": "disable_rule", "rule_name": "dan_mode"}],
	  "suggested_fix": "禁用该规则"}`
	r, _ := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败")
	}
	rules, ok := r.RuleAdjustment["rules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("数组型 rule_adjustment 应包成 {rules: [...]}，得 %v", r.RuleAdjustment)
	}
	if plan := fixPlanOf(t, r); len(plan.AutoActions) != 1 {
		t.Errorf("auto_actions 未镜像: %+v", plan.AutoActions)
	}
}

func TestParseAnalysisResponse_FixPlanContract(t *testing.T) {
	response := `{
  "root_cause": "判官超时预算过小",
  "confidence_score": 0.91,
  "suggested_fix": "放宽超时并补规则兜底",
  "rule_adjustment": null,
  "fix_plan": {
    "summary": "放宽判官超时并增加规则层兜底",
    "auto_actions": [{"action": "adjust_severity", "rule_name": "r1", "severity": 3}],
    "manual_steps": [
      {"step": 1, "title": "把判官超时从 30s 调到 60s", "detail": "管理面 → PI 策略", "kind": "config", "risk": "low"},
      {"step": 2, "title": "为取消类失败增加退避", "detail": "errors.Is(err, context.Canceled)", "kind": "代码", "risk": "中"}
    ],
    "config_changes": [{"target": "llm_detection_timeout_sec", "current": "30", "proposed": "60"}],
    "code_changes": [{"file": "domains/promptinjection/detector.go", "description": "取消不计失败", "patch": "--- a\n+++ b\n"}],
    "deploy": {"required": true, "targets": ["245", "154"], "steps": ["先预发布再生产"], "rollback": "回退到上一版镜像"},
    "verification": {"method": "layer_recheck", "expect": "复测通过且同型错误不再新增"}
  }
}`
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	plan := fixPlanOf(t, r)
	if plan.Summary != "放宽判官超时并增加规则层兜底" {
		t.Errorf("summary = %q", plan.Summary)
	}
	if len(plan.AutoActions) != 1 {
		t.Errorf("auto_actions = %+v", plan.AutoActions)
	}
	if r.RuleAdjustment == nil {
		t.Errorf("rule_adjustment 为空时应用 fix_plan.auto_actions 补位")
	}
	if len(plan.ManualSteps) != 2 || plan.ManualSteps[1].Kind != "code" || plan.ManualSteps[1].Risk != "medium" {
		t.Errorf("manual_steps 读错（中文类别/风险要归一）: %+v", plan.ManualSteps)
	}
	if len(plan.ConfigChanges) != 1 || plan.ConfigChanges[0].Proposed != "60" {
		t.Errorf("config_changes 读错: %+v", plan.ConfigChanges)
	}
	if len(plan.CodeChanges) != 1 || plan.CodeChanges[0].File != "domains/promptinjection/detector.go" {
		t.Errorf("code_changes 读错: %+v", plan.CodeChanges)
	}
	if plan.Deploy == nil || !plan.Deploy.Required || len(plan.Deploy.Targets) != 2 || plan.Deploy.Rollback == "" {
		t.Errorf("deploy 读错: %+v", plan.Deploy)
	}
	if plan.Verification == nil || plan.Verification.Method != "layer_recheck" {
		t.Errorf("verification 读错: %+v", plan.Verification)
	}
	// 落库前要能被 JSON 序列化（analysis_details 是 JSONB 列）。
	if _, err := json.Marshal(r.AnalysisDetails); err != nil {
		t.Fatalf("analysis_details 不可序列化: %v", err)
	}
}

// ── 文本层的小件 ─────────────────────────────────────────────────────────

func TestNormKey(t *testing.T) {
	cases := map[string]string{
		"Root Cause":       "rootcause",
		"root_cause":       "rootcause",
		"rootCause":        "rootcause",
		"根本原因（root cause）": "根本原因",
		"建议修复（fix）":        "建议修复",
		"  Impact-Scope ":  "impactscope",
	}
	for in, want := range cases {
		if got := normKey(in); got != want {
			t.Errorf("normKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepairHelpers(t *testing.T) {
	if got := stripTrailingCommas(`{"a": 1, "b": [1, 2,],}`); got != `{"a": 1, "b": [1, 2]}` {
		t.Errorf("stripTrailingCommas = %q", got)
	}
	if got := escapeRawControlChars("x\"a\nb\"y\n  z"); got != `x"a\nb"y`+"\n  z" {
		t.Errorf("escapeRawControlChars = %q", got)
	}
	if got := cleanResponse("\uFEFF\u200B{\u200B\"a\":1}"); got != `{"a":1}` {
		t.Errorf("cleanResponse = %q", got)
	}
	if got := runeTruncate("中文字符串", 3); got != "中文字…" {
		t.Errorf("runeTruncate = %q", got)
	}
}

// TestParseAnalysisResponse_IsDeterministic：同一份响应解析两遍，落库内容逐字相同。
func TestParseAnalysisResponse_IsDeterministic(t *testing.T) {
	response := `{"analysis_report": {"summary": "s", "root_cause_analysis": {"primary_cause": "p"}, "remediation": {"immediate": ["a", "b"], "long_term": ["c"]}}}`
	var first string
	for i := 0; i < 8; i++ {
		r, _ := parseAnalysisResponse(response)
		if r == nil {
			t.Fatalf("解析失败")
		}
		got := mustJSON(t, map[string]any{"fix": r.SuggestedFix, "details": r.AnalysisDetails})
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("第 %d 次解析结果与首次不同（键序不稳定）:\n%s\n%s", i, first, got)
		}
	}
}

// TestBuildSteps_Limits：步骤限量、截断、默认归类。
func TestBuildSteps_Limits(t *testing.T) {
	raw := make([]rawStep, maxManualSteps+5)
	for i := range raw {
		raw[i] = rawStep{title: strings.Repeat("步", maxStepTitleRunes+10), detail: "补充"}
	}
	steps := buildSteps(raw)
	if len(steps) != maxManualSteps {
		t.Errorf("steps = %d, want %d", len(steps), maxManualSteps)
	}
	if n := len([]rune(steps[0].Title)); n != maxStepTitleRunes+1 {
		t.Errorf("标题未按 rune 截断: %d runes", n)
	}
	if steps[0].Kind != "ops" || steps[0].Risk != "low" {
		t.Errorf("默认归类应为 ops/low: %+v", steps[0])
	}
	if steps[maxManualSteps-1].Step != maxManualSteps {
		t.Errorf("编号不连续: %+v", steps[maxManualSteps-1])
	}
}

// 245 ROW 2：根因是**数组**（多条原因），顶层另有 executive_summary。
func TestParseAnalysisResponse_RootCauseArrayWithExecutiveSummary(t *testing.T) {
	response := "以下是分析报告：\n```json\n" + `{
  "report_metadata": {"error_type": "canary_detection_failed", "severity": "critical", "confidence": "high"},
  "executive_summary": "Canary 匹配查询因连接重置失败，且重试次数为零。",
  "root_cause_analysis": [
    {"id": "RC-1", "category": "infrastructure", "title": "后端连接重置（直接原因）", "description": "查询在 TCP 层被对端重置。"},
    {"id": "RC-2", "category": "resilience", "title": "重试机制未触发（放大因素）", "description": "连接重置属瞬时故障却未重试。"}
  ],
  "remediation_plan": {"immediate": ["确认后端进程是否重启"], "short_term": ["把连接重置标记为可重试"]}
}` + "\n```"
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	if r.RootCause != "Canary 匹配查询因连接重置失败，且重试次数为零。" {
		t.Errorf("root_cause = %q, want executive_summary", r.RootCause)
	}
	if r.SeverityLevel != "critical" || r.ConfidenceScore != 0.75 {
		t.Errorf("severity/confidence 读错: %s %v", r.SeverityLevel, r.ConfidenceScore)
	}
}

// 245 ROW 6：根因对象以 description 作正文，置信度藏在根因对象里。
func TestParseAnalysisResponse_RootCauseObjectUsesDescription(t *testing.T) {
	response := "```json\n" + `{
  "analysis_report": {
    "root_cause": {
      "primary_signal": "context canceled",
      "category": "pipeline_interruption",
      "description": "'context canceled' 表示检测管线在扫描完成前被取消。",
      "confidence": "high"
    },
    "repair_steps": [{"title": "取消不计失败", "detail": "按错误类型过滤"}]
  }
}` + "\n```"
	r, diag := parseAnalysisResponse(response)
	if r == nil {
		t.Fatalf("解析失败: %+v", diag)
	}
	if r.RootCause != "'context canceled' 表示检测管线在扫描完成前被取消。" {
		t.Errorf("root_cause = %q, want description（不是 primary_signal）", r.RootCause)
	}
	if r.ConfidenceScore != 0.75 {
		t.Errorf("根因对象里的 confidence 应被读到: %v", r.ConfidenceScore)
	}
}

// 根因容器一个指定正文键都没有 ⇒ 把全部字符串字段渲染出来（不放弃）。
func TestParseAnalysisResponse_RootCauseContainerWithoutKnownKeys(t *testing.T) {
	response := `{"diagnosis": {"root_cause": {"mechanism": "查询被取消", "where": "canary 层"}}, "suggested_fix": "重试"}`
	r, _ := parseAnalysisResponse(response)
	if r == nil || !strings.Contains(r.RootCause, "查询被取消") || !strings.Contains(r.RootCause, "canary 层") {
		t.Errorf("根因容器兜底渲染失败: %+v", r)
	}
}

// 自由文本兜底：根因取第一句实质内容，不取开场白。
func TestParseAnalysisResponse_ProseFallbackSkipsIntroLine(t *testing.T) {
	prose := "以下是对该错误的安全分析报告：\n根因是判官端点在高峰期持续超时，规则层又没有兜底，导致请求被 fail-closed 拦截。建议放宽超时预算并补充规则层降级路径，同时为该类失败增加告警。"
	r, diag := parseAnalysisResponse(prose)
	if r == nil || diag.Mode != ParseModeUnstructured {
		t.Fatalf("应走自由文本兜底: %+v %+v", r, diag)
	}
	if r.RootCause != "根因是判官端点在高峰期持续超时，规则层又没有兜底，导致请求被 fail-closed 拦截。" {
		t.Errorf("root_cause = %q", r.RootCause)
	}
}

// ── 截断修复（2026-11-26）────────────────────────────────────────────────────

// 输出在**字符串中间**被截断——额度用完时最常见的断点。此前这种回答一个成员都打捞
// 不出来（未闭合的引号让后面的一切都成了字符串的一部分）⇒ 整条被当成散文 ⇒
// 连前面完整写出的根因与建议都丢了，而且不标记截断。
func TestParseAnalysisResponse_CutInsideStringKeepsCompleteMembers(t *testing.T) {
	pretty := `{"root_cause":"上游流被掐断","error_category":"upstream_interrupted","confidence":0.8,
	"suggested_fix":"重试","fix_plan":{"summary":"重试即可","manual_steps":[{"step":1,"title":"重试","kind":"client","risk":"low"},{"step":2,"title":"核对日`
	single := `{"root_cause":"x","error_category":"upstream_interrupted","confidence":0.9,"suggested_fix":"重试一下再`

	for name, in := range map[string]string{"pretty": pretty, "single-line": single} {
		r, diag := parseAnalysisResponse(in)
		if r == nil {
			t.Fatalf("%s: 截断输出一条都没救回来: %+v", name, diag)
		}
		if diag.Mode != ParseModeSalvaged || !diag.Truncated {
			t.Errorf("%s: 应为 salvaged+truncated，得 %+v", name, diag)
		}
		// ★ 断点之前完整写出的成员一个都不能丢。
		if r.RootCause == "" || r.ErrorCategory != "upstream_interrupted" {
			t.Errorf("%s: 完整成员没保住: %+v", name, r)
		}
		if name == "pretty" && r.SuggestedFix == "" {
			t.Errorf("pretty: 断点在 fix_plan 里，前面的 suggested_fix 必须保住: %+v", r)
		}
		if name == "single-line" && r.SuggestedFix != "" {
			t.Errorf("single-line: 被截断的正是 suggested_fix 本身，不该编出半句: %q", r.SuggestedFix)
		}
		if r.ConfidenceScore != salvagedConfidenceCap {
			t.Errorf("%s: 截断结果置信度 = %v, want 封顶 %v", name, r.ConfidenceScore, salvagedConfidenceCap)
		}
		if len(diag.LostKeys) == 0 {
			t.Errorf("%s: 被丢掉的成员要如实登记: %v", name, diag.LostKeys)
		}
	}
	// 丢失成员按路径登记：断点在 fix_plan.manual_steps 里那个成员的 title 上。
	if _, diag := parseAnalysisResponse(pretty); diag.LostKeys[0] != "fix_plan.manual_steps.title" {
		t.Errorf("丢失成员应按路径登记，得 %v", diag.LostKeys)
	}
	if _, diag := parseAnalysisResponse(single); diag.LostKeys[0] != "suggested_fix" {
		t.Errorf("丢失成员应按路径登记，得 %v", diag.LostKeys)
	}
}

func TestParseAnalysisResponse_CutInsideNumberLiteralOrFirstNestedMember(t *testing.T) {
	cases := map[string]struct{ in, lost string }{
		"number":  {`{"root_cause":"x","error_category":"upstream_interrupted","confidence":0.`, "confidence"},
		"literal": {`{"root_cause":"x","error_category":"upstream_interrupted","is_false_positive":tru`, "is_false_positive"},
		"nested":  {`{"root_cause":"x","fix_plan":{"summary":"重`, "fix_plan.summary"},
	}
	for name, c := range cases {
		r, diag := parseAnalysisResponse(c.in)
		if r == nil || r.RootCause != "x" {
			t.Fatalf("%s: 应读出根因，得 %+v / %+v", name, r, diag)
		}
		if !diag.Truncated || len(diag.LostKeys) != 1 || diag.LostKeys[0] != c.lost {
			t.Errorf("%s: 应标记截断并登记 %s，得 %+v", name, c.lost, diag)
		}
	}
}

func TestRepairTruncatedJSON_OnlyTouchesUnclosedText(t *testing.T) {
	// 已经闭合的文档：不是截断，不动。
	if _, _, ok := repairTruncatedJSON(`{"a":1,"b":[1,2]}`); ok {
		t.Errorf("闭合的文档不该被当成截断")
	}
	// 括号错配：不是截断，不修（交给逐成员打捞）。
	if _, _, ok := repairTruncatedJSON(`{"a":1]`); ok {
		t.Errorf("括号错配不该按截断修")
	}
	// 成员之后断掉（带尾逗号）：补齐后必须能严格解码，且没有成员丢失。
	closed, lost, ok := repairTruncatedJSON(`{"a":1,"b":"x",`)
	if !ok || len(lost) != 0 {
		t.Fatalf("成员之后截断: ok=%v lost=%v", ok, lost)
	}
	if _, err := decodeStrictOrdered(closed); err != nil {
		t.Errorf("补齐后应能严格解码: %q (%v)", closed, err)
	}
	// 字符串里出现的括号与逗号不算结构符。
	closed, lost, ok = repairTruncatedJSON(`{"a":"a{b,}c","d":{"e":"f","g":"h[","i":"cut`)
	if !ok || len(lost) != 1 || lost[0] != "d.i" {
		t.Fatalf("字符串里的括号不该影响路径: ok=%v lost=%v", ok, lost)
	}
	if _, err := decodeStrictOrdered(closed); err != nil {
		t.Errorf("补齐后应能严格解码: %q (%v)", closed, err)
	}
}
