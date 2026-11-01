package sanitize

import (
	"context"
	"testing"

	compliancehook "github.com/kaixuan/llm-gateway-go/domains/hooks/outputcompliance"
)

// 2026-11-01 12h 审计轮：「工具参数不是合法 JSON」这条阻断此前返回
// `ComplianceResult{Blocked: true}` **不带 Issues** ⇒ ⇒ 派生规则名的
// policyRuleFromIssues 拿不到任何 RuleID ⇒ ⇒ 落到审核面的 rule_id 是
// **空串** ⇒ ⇒ 审核单只有一句「未知原因的拦截」，既说不出因为什么、
// ⇒ ⇒ 也拿不到载荷摘要 ⇒ ⇒ 归因面在这条路径上等于没生效。
//
// ★ 变异对照 ★：把 Issues 去掉 ⇒ 本用例红（Issues 为空 / RuleID 不匹配）。

// TestOutputSensitiveInvalidToolJSONCarriesRuleID 钉住：非法工具参数
// 阻断时必须带规则名。
func TestOutputSensitiveInvalidToolJSONCarriesRuleID(t *testing.T) {
	s, err := NewSanitizer(NewPatternDetector())
	if err != nil {
		t.Fatal(err)
	}
	c := &outputSensitiveChecker{sanitizer: s, action: OutputMask}

	res, err := c.CheckField(context.Background(), "tenant-1", toolJSONLabelPrefix+"@write", "{not-json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("nil result")
	}
	if !res.Blocked {
		t.Fatal("非法 JSON 的工具参数必须阻断（fail-closed），却放行了")
	}
	if len(res.Issues) == 0 {
		t.Fatal("阻断必须带 Issue（否则 rule_id 为空 ⇒ 审核单是空白单）")
	}
	if got := res.Issues[0].RuleID; got != compliancehook.GuardToolJSONInvalidRuleID {
		t.Fatalf("rule_id = %q，期望 %q", got, compliancehook.GuardToolJSONInvalidRuleID)
	}
	if res.Issues[0].Description == "" {
		t.Fatal("Issue 必须带说明（审核员要看得出「不是内容规则命中」）")
	}
}

// TestOutputSensitiveInvalidToolJSONIsNotRedactInvalid 钉住两种形态不许混淆：
//
// ★ 「脱敏**之后**结果不是合法 JSON」（builtin.guard.redact_invalid）与
// ★ 「脱敏**之前**输入就不是合法 JSON」（builtin.guard.tool_json_invalid）
// ★ 处置方向相反 ⇒ 规则名必须不同 ★。
func TestOutputSensitiveInvalidToolJSONIsNotRedactInvalid(t *testing.T) {
	if compliancehook.GuardToolJSONInvalidRuleID == compliancehook.GuardRedactInvalidRuleID {
		t.Fatal("两种形态的规则名必须不同（处置方向相反：一个是脱敏规则问题，一个是上游参数问题）")
	}
}
