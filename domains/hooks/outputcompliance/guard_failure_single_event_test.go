package outputcompliance

import (
	"context"
	"errors"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// 2026-11-01 12h 审计轮：守卫侧失败的**单事件 / 增量片段**这条路径。
//
// ★ 背景 ★：2026-10-31 拦截归因轮把守卫侧的 fail-closed 分支改成自带
// 规则名 / 原因 / 证据的 PolicyBlockError，但只改了**多片段**那条路
// （releaseCheckedFrames 的 parts 循环）。**单事件**那条路
// （inspectSSEFrame）里语义完全相同的两条分支仍然返回**裸 error** ⇒
// ⇒ 审核单 rule_id / reason / 证据全空 ⇒ ⇒ 与多片段路径**行为不一致**。
//
// ★ 这组用例钉住「同语义 ⇒ 同归因」★★

// TestSingleEventToolLaneRewriteRefusedCarriesRuleAndEvidence 钉住 ①：
// 单事件路径上「工具参数 lane 不许安全改写」⇒ 自带规则名 / 原因 / 证据。
//
// ★ 变异对照 ★：把 inspectSSEFrame 里的 PolicyBlockError 换回裸
// errors.New ⇒ 本用例红（RuleID 空 / 类型不对）。
func TestSingleEventToolLaneRewriteRefusedCarriesRuleAndEvidence(t *testing.T) {
	it := NewMandatoryOutputComplianceInterceptor(redactingChecker{redacted: `{"cmd":"***"}`}, "guard.attribution.single_event")
	meta := &response.StreamMeta{
		TenantID:  "t",
		SessionID: "s",
		RequestID: "r-single-event",
		State:     response.NewStreamState(),
	}
	// chat 形态的工具参数增量帧（形态与 interceptor_protocol_test.go 里
	// 已被验证过的那条一致）。
	frame := []byte(`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":\"echo hello\"}"}}]}}]}` + "\n\n")

	_, err := it.inspectSSEFrame(context.Background(), frame, meta, true, nil)
	if err == nil {
		t.Fatal("不可改写的工具参数 lane 必须阻断（fail-closed），却放行了")
	}
	var pbe *errorsx.PolicyBlockError
	if !errors.As(err, &pbe) || pbe == nil {
		t.Fatalf("必须是 PolicyBlockError（审核单要能说出原因），实际 %T: %v", err, err)
	}
	if pbe.RuleID != GuardRewriteRefusedRuleID {
		t.Fatalf("规则名 = %q，期望 %q（单事件路径必须与多片段路径同款归因）", pbe.RuleID, GuardRewriteRefusedRuleID)
	}
	if pbe.Reason == "" {
		t.Fatal("必须给出人类可读原因（审核员要看得出「不是规则命中」）")
	}
	if pbe.Evidence == "" {
		t.Fatal("必须带证据（被拦的那段内容）——否则审核员判不了「拦得对不对」")
	}
}

// TestGuardToolJSONInvalidRuleIDIsRegistered 钉住新形态「工具参数不是合法
// JSON」：
//
//	① 它必须与 builtin.guard.redact_invalid **不同**（处置方向相反）；
//	② 它必须有内建展示说明（否则审核面指向错误事实源）。
func TestGuardToolJSONInvalidRuleIDIsRegistered(t *testing.T) {
	if GuardToolJSONInvalidRuleID == GuardRedactInvalidRuleID {
		t.Fatal("「工具参数本身不是合法 JSON」与「脱敏结果不是合法 JSON」是两种形态，处置方向相反，不许共用规则名")
	}
	if GuardToolJSONInvalidRuleID != "builtin.guard.tool_json_invalid" {
		t.Fatalf("规则名 = %q，期望 builtin.guard.tool_json_invalid（builtin.guard. 前缀是放行表/自动放行的跳过判据）", GuardToolJSONInvalidRuleID)
	}
	info, ok := sanitizerules.BuiltinRuleInfoFor(GuardToolJSONInvalidRuleID)
	if !ok || info.Name == "" || info.Description == "" {
		t.Fatalf("守卫规则 %q 缺内建展示说明（审核面会显示成「托管规则」⇒ 指错事实源）", GuardToolJSONInvalidRuleID)
	}
}

// TestAllGuardRuleIDsAreRegisteredInShapeTable 钉住「形态表不许漏登记」：
//
// ★ 每新增一条 builtin.guard.* 规则 ID，都必须同时进这两个清单 ★
// —— 清单是形状表的**索引**，漏登记 = 形态表少一条 = 审核面出现一个
// —— 没有展示说明的规则 ID（指错事实源）。
func TestAllGuardRuleIDsAreRegisteredInShapeTable(t *testing.T) {
	all := []string{
		GuardCapacityRuleID,
		GuardNoStateRuleID,
		GuardRewriteRefusedRuleID,
		GuardRedactInvalidRuleID,
		GuardCheckFailedRuleID,
		GuardFrameEncodeRuleID,
		GuardToolJSONInvalidRuleID,
	}
	for _, id := range all {
		if _, ok := sanitizerules.BuiltinRuleInfoFor(id); !ok {
			t.Fatalf("守卫规则 %q 未登记进形态表（builtinRuleDescriptions）", id)
		}
	}
}
