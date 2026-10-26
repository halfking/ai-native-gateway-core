package outputcompliance

// r1037（2026-10-20，211）：输出侧的「默认不拦截」与「拦截时说清哪条规则」。
//
// owner 口径：
//
//	「输出信息当前默认不要拦截」
//	「就算是被拦截，应该在中断时反馈给客户因为什么规则被拦截，不要搞空响应」
//
// ## 与 egress 侧的关系
//
// egress 侧（security/sanitize）管的是「模型生成的工具参数里有没有外传目的地」；
// output 侧（本包）管的是「模型输出的文本本身有没有违规内容」。两条路径各有
// 自己的 scope，清单里也各有各的键。
//
// 本包的 TestMain（hardblock_testmain_test.go）把清单打开成「全部可阻断」，
// 所以本文件里**测默认行为**的用例必须显式 withHardBlockAllows(t,
// hardBlockDefault()...)。

import (
	"context"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// r1037EnforcePolicy 是「真的要拦」的策略：enforce + block。
//
// 默认策略是 observe 模式（本来就不拦），用它测「默认不拦」是假绿——
// 分不清是清单关着还是策略本来就不拦。
func r1037EnforcePolicy() *Policy {
	return &Policy{
		TenantID:        "default",
		Enabled:         true,
		EnforcementMode: "enforce",
		ActionOnPII:     "block",
		ActionOnSecrets: "block",
		AutoRedact:      true,
		RedactEmail:     true,
		RedactPhone:     true,
	}
}

// TestR1037OutputDefaultDoesNotBlock 固化「输出侧默认不拦截」。
//
// 前提仍在场：策略确实是 enforce + block，所以「没拦」只能来自清单，
// 不是来自策略本身放水。
func TestR1037OutputDefaultDoesNotBlock(t *testing.T) {
	withHardBlockAllows(t, hardBlockDefault()...)
	policy := r1037EnforcePolicy()
	if policy.EnforcementMode != "enforce" {
		t.Fatal("前提不成立：策略不是 enforce")
	}
	c := &Checker{}
	issues := []ComplianceIssue{
		{Type: "secret", Subtype: "credential", Severity: 9, Content: "***"},
		{Type: "pii", Subtype: "email", Severity: 7, Content: "***"},
	}
	if c.shouldBlock(issues, policy) {
		t.Error("默认清单下输出侧仍终止回合（清单默认值错）")
	}
}

// TestR1037OutputAllowlistReEnablesBlock 是清单的正向对照。
func TestR1037OutputAllowlistReEnablesBlock(t *testing.T) {
	withHardBlockAllows(t,
		sanitizerules.HardBlockAllow{Scope: "input", Rule: "*"},
		sanitizerules.HardBlockAllow{Scope: "output", Rule: "builtin.secret.credential"},
	)
	c := &Checker{}
	policy := r1037EnforcePolicy()

	secret := []ComplianceIssue{{Type: "secret", Subtype: "credential", Severity: 9}}
	if !c.shouldBlock(secret, policy) {
		t.Error("清单里的规则未被拦截（清单不生效）")
	}
	// 粒度：只开了 secret.credential，pii.email 不该跟着开。
	pii := []ComplianceIssue{{Type: "pii", Subtype: "email", Severity: 7}}
	if c.shouldBlock(pii, policy) {
		t.Error("只开了 secret.credential，pii.email 不该被一起打开")
	}
}

// TestR1037IssuesCarryRuleID 钉住「因为什么规则被拦」这句话的原料：
// 每条问题都必须带得出规则名，否则客户端拿不到可机器消费的原因。
func TestR1037IssuesCarryRuleID(t *testing.T) {
	withHardBlockAllows(t, hardBlockDefault()...)
	c := NewBuiltinChecker()
	result, err := c.Check(context.Background(), "default", "联系邮箱 zhang@example.com，手机 13800138000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(result.Issues) == 0 {
		t.Fatal("未检出任何问题，判据失去意义")
	}
	for _, issue := range result.Issues {
		if issue.RuleID == "" {
			t.Errorf("问题 %s/%s 没有规则 ID：客户端将无法说明「因为什么规则」", issue.Type, issue.Subtype)
		}
	}
}

// TestR1037CheckKeepsDetectingWhileNotBlocking 端到端走一次 Check：
// 默认清单下问题**照常检出**（不能因为不拦就把检测一起关掉——那样台账就空了）。
//
// 注意：builtin checker 没有 DB，getPolicy 返回默认策略（observe 模式），
// 所以这条判据测的是「检测与规则名没有丢」，不是「清单关着」；
// 清单闸本身由上面两条 shouldBlock 判据钉死。
func TestR1037CheckKeepsDetectingWhileNotBlocking(t *testing.T) {
	withHardBlockAllows(t, hardBlockDefault()...)
	c := NewBuiltinChecker()
	result, err := c.Check(context.Background(), "default", "手机号 13800138000")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Blocked {
		t.Error("默认清单下 Check 仍返回 Blocked")
	}
	if len(result.Issues) == 0 {
		t.Error("默认清单下问题被整段丢掉了——必须只降级动作，不能降级检测")
	}
	for _, issue := range result.Issues {
		if issue.RuleID == "" {
			t.Errorf("问题 %s/%s 没有规则 ID：台账与客户端都会失去「因为什么规则」", issue.Type, issue.Subtype)
		}
	}
}

// TestR1037NotBlockedStillRecordsAnAction 钉住「降级不是静默」：
// 不拦的时候动作仍要落成可见的一档（warned/logged），不是「无动作」。
func TestR1037NotBlockedStillRecordsAnAction(t *testing.T) {
	withHardBlockAllows(t, hardBlockDefault()...)
	c := &Checker{}
	policy := r1037EnforcePolicy()
	issues := []ComplianceIssue{{Type: "secret", Subtype: "credential", Severity: 9}}
	blocked := c.shouldBlock(issues, policy)
	if blocked {
		t.Fatal("前提不成立：默认清单下不该拦")
	}
	action := c.getActionTaken(issues[0], blocked)
	if action == "blocked" {
		t.Error("不拦时动作仍记成 blocked")
	}
	if action == "" {
		t.Error("不拦时动作为空——命中会从台账里消失")
	}
}
