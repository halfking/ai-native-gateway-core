package outputcompliance

// 守卫侧失败的**归因**判据（2026-10-31 拦截归因轮）。
//
// ## 这组用例钉的是什么
//
// 211 生产实测：24 条真实拦截的审核单 **rule_id / reason / 证据全空** ⇒
// 审核员面前是一张**空白清单**（既说不出因为什么规则被拦，也看不到拦了什么）
// ⇒ 「拦得对不对」无从判断 ⇒ 规则优化没有输入。
//
// ★ 根因不是规则表达式太宽，而是**守卫侧的 fail-closed 分支不带元数据** ★：
//
//	① 检查器报错 / 返回空结果（check_failed）
//	② 工具参数 lane 不许安全改写（rewrite_refused）
//	③ 脱敏结果不是合法 JSON（redact_invalid）
//
// ★ 这三条此前都返回**裸 error** ⇒ 审核单 rule/reason 全空 ★。
//
// ## 契约
//
//	★ 每条守卫侧失败必须**自带**：规则 ID（builtin.guard.*）、人类可读原因、
//	  有界证据（被拦的那段内容）★★
//	★ 规则 ID 必须**逐条不同** ⇒ ★ 不许都挂 capacity_exceeded ★
//	（★ 那是假归因：审核员会照着「容量」去调上限，而真因是「不许改写」★★）
//	★ 每个守卫规则 ID 都必须有**内建展示说明** ⇒ ★ 否则审核面的「规则语义」
//	  会退化成「（托管规则，表达式与适用范围见规则配置）」⇒ ★★ 那是指向
//	  **错误事实源**的提示（这些根本不是托管规则）★★。

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/response"
	"github.com/kaixuan/llm-gateway-go/domains/outputcompliance"
	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// failingChecker 永远报错（★ 模拟检查器不可用 ★）。
type failingChecker struct{}

func (failingChecker) Check(_ context.Context, _, _ string) (*outputcomplianceResult, error) {
	return nil, errors.New("checker unavailable")
}

// redactingChecker 返回一个**指定的**脱敏结果（★ 用来构造「改写后不是合法
// JSON」与「需要改写」两种形态 ★）。
type redactingChecker struct {
	redacted string
}

func (c redactingChecker) Check(_ context.Context, _, text string) (*outputcomplianceResult, error) {
	return &outputcomplianceResult{
		Compliant:     true,
		RedactedOutput: c.redacted,
		Issues:        []outputcomplianceIssue{{Type: "secret", Subtype: "password", RuleID: "secret.password"}},
	}, nil
}

// ★ outputcomplianceResult / outputcomplianceIssue 是检查器返回类型在���文件
// 里的别名（★ 免得测试文件再 import 一次 outputcompliance ★）。
type outputcomplianceResult = outputcompliance.ComplianceResult
type outputcomplianceIssue = outputcompliance.ComplianceIssue

// toolLaneState 构造一个「工具参数 lane」的待检状态（★ 两条 lane 片段 ⇒
// 合并检查会真的跑起来 ★）。
func toolLaneState(original string, immutable bool) *complianceStreamState {
	return toolLaneStateLabel(original, immutable, "write")
}

// toolLaneStateLabel 构造指定标签的工具参数 lane 状态。
func toolLaneStateLabel(original string, immutable bool, label string) *complianceStreamState {
	field := visibleTextField{
		parent:    map[string]any{"arguments": original},
		key:       "arguments",
		lane:      "chat.tool.1.arguments",
		label:     label,
		immutable: immutable,
	}
	frame := &checkedStreamFrame{
		original: []byte("data: {}\n\n"),
		root:     map[string]any{},
	}
	return &complianceStreamState{
		checkCtx: context.Background(),
		enabled:  true,
		redact:   true,
		// ★ 帧列表非空：releaseCheckedFrames 在 frames 为空时直接返回（★ 没有
		// ★ 待释放的帧 ⇒ 不会走到 lane 检查 ★）。
		frames: []*checkedStreamFrame{frame},
		lanes: map[string][]*streamTextPart{
			"chat.tool.1.arguments": {
				{frame: frame, field: field, original: original},
				{frame: frame, field: field, original: ""},
			},
		},
	}
}

// TestGuardCheckFailureCarriesRuleReasonEvidence 钉住 ①：检查器失败 ⇒ 自带
// 规则名/原因/证据。
func TestGuardCheckFailureCarriesRuleReasonEvidence(t *testing.T) {
	it := NewMandatoryOutputComplianceInterceptor(failingChecker{}, "guard.attribution.check_failed")
	state := toolLaneState(`{"cmd":"rm -rf /tmp/x"}`, false)
	meta := &response.StreamMeta{TenantID: "t", SessionID: "s", RequestID: "r1"}

	_, err := it.releaseCheckedFrames(state.checkCtx, state, meta)
	if err == nil {
		t.Fatal("检查器失败必须阻断（fail-closed），却放行了")
	}
	var pbe *errorsx.PolicyBlockError
	if !errors.As(err, &pbe) || pbe == nil {
		t.Fatalf("必须是 PolicyBlockError（审核单要能说出原因），实际 %T: %v", err, err)
	}
	if pbe.RuleID != GuardCheckFailedRuleID {
		t.Fatalf("规则名 = %q，期望 %q", pbe.RuleID, GuardCheckFailedRuleID)
	}
	if pbe.Reason == "" {
		t.Fatal("必须给出人类可读原因（审核员要看得出「不是规则命中」）")
	}
	if pbe.Evidence == "" {
		t.Fatal("必须带证据（被拦的那段内容）——否则审核员判不了「拦得对不对」")
	}
}

// TestGuardRewriteRefusedCarriesRuleAndEvidence 钉住 ②：lane 不许安全改写 ⇒
// 自带规则名/原因/证据。
func TestGuardRewriteRefusedCarriesRuleAndEvidence(t *testing.T) {
	it := NewMandatoryOutputComplianceInterceptor(redactingChecker{redacted: `{"cmd":"***"}`}, "guard.attribution.rewrite_refused")
	state := toolLaneState(`{"cmd":"echo hello"}`, true)
	meta := &response.StreamMeta{TenantID: "t", SessionID: "s", RequestID: "r2"}

	_, err := it.releaseCheckedFrames(state.checkCtx, state, meta)
	if err == nil {
		t.Fatal("不可改写的 lane 必须阻断（fail-closed），却放行了")
	}
	var pbe *errorsx.PolicyBlockError
	if !errors.As(err, &pbe) || pbe == nil {
		t.Fatalf("必须是 PolicyBlockError，实际 %T: %v", err, err)
	}
	if pbe.RuleID != GuardRewriteRefusedRuleID {
		t.Fatalf("规则名 = %q，期望 %q", pbe.RuleID, GuardRewriteRefusedRuleID)
	}
	if pbe.Evidence == "" {
		t.Fatal("必须带证据（审核员要看「当时拦的是哪段参数」）")
	}
}

// TestGuardRedactInvalidCarriesRuleAndEvidence 钉住 ③：脱敏结果不是合法 JSON
// ⇒ 自带规则名/原因/证据。
func TestGuardRedactInvalidCarriesRuleAndEvidence(t *testing.T) {
	// ★ 非 mandatory 拦截器：★ mandatory 会在改写前先走「不许改写」那条
	// ★ fail-closed（rewrite_refused）⇒ ★ 本用例要钉的是**下一条**形态：
	// ★ 脱敏结果不是合法 JSON ⇒ 不能把坏参数交给客户端 ⇒ fail-closed ★。
	it := NewOutputComplianceInterceptor(redactingChecker{redacted: `{"cmd":`}, nil)
	state := toolLaneStateLabel(`{"cmd":"echo hi"}`, false, "__tool_json")
	state.redact = true
	meta := &response.StreamMeta{TenantID: "t", SessionID: "s", RequestID: "r3"}

	_, err := it.releaseCheckedFrames(state.checkCtx, state, meta)
	if err == nil {
		t.Fatal("脱敏结果非法 JSON 必须阻断（fail-closed），却放行了")
	}
	var pbe *errorsx.PolicyBlockError
	if !errors.As(err, &pbe) || pbe == nil {
		t.Fatalf("必须是 PolicyBlockError，实际 %T: %v", err, err)
	}
	if pbe.RuleID != GuardRedactInvalidRuleID {
		t.Fatalf("规则名 = %q，期望 %q", pbe.RuleID, GuardRedactInvalidRuleID)
	}
	if pbe.Evidence == "" {
		t.Fatal("必须带证据")
	}
}

// TestGuardRuleIDsAreDistinct钉住「规则 ID 逐条不同」：
// ★ 把所有守卫侧失败挂到同一个 ID 上 ⇒ 审核员看到「容量越界」就去调上限，
// ★ 而真因是「这条 lane 不许改写」⇒ ★★ 调不对地方 ★★。
func TestGuardRuleIDsAreDistinct(t *testing.T) {
	ids := []string{
		GuardCapacityRuleID,
		GuardNoStateRuleID,
		GuardRewriteRefusedRuleID,
		GuardRedactInvalidRuleID,
		GuardCheckFailedRuleID,
		GuardFrameEncodeRuleID,
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatal("守卫规则 ID 不许为空（审核单要能说出是哪一种失败）")
		}
		if !strings.HasPrefix(id, "builtin.guard.") {
			t.Fatalf("守卫规则 ID 必须带 builtin.guard. 前缀（放行表/自动放行按前缀跳过）: %q", id)
		}
		if seen[id] {
			t.Fatalf("守卫规则 ID 重复（不同失败形态必须能区分）: %q", id)
		}
		seen[id] = true
	}
}

// TestGuardRulesHaveBuiltinDescriptions 钉住「审核面的规则语义不许指错事实源」：
// ★ 每个守卫规则 ID 都必须有内建说明 ⇒ 否则审核面退化成
// ★ 「（托管规则，表达式与适用范围见规则配置）」⇒ ★★ 审核员会去托管规则里
// ★★ 找一条永远找不到的规则 ★★。
func TestGuardRulesHaveBuiltinDescriptions(t *testing.T) {
	for _, id := range []string{
		GuardCapacityRuleID,
		GuardNoStateRuleID,
		GuardRewriteRefusedRuleID,
		GuardRedactInvalidRuleID,
		GuardCheckFailedRuleID,
		GuardFrameEncodeRuleID,
	} {
		info, ok := sanitizerules.BuiltinRuleInfoFor(id)
		if !ok || info.Name == "" || info.Description == "" {
			t.Fatalf("守卫规则 %q 缺内建展示说明（审核面会显示成「托管规则」⇒ 指错事实源）", id)
		}
		if !strings.Contains(info.Description, "不是") {
			t.Fatalf("守卫规则 %q 的说明必须点明「不是内容规则命中」（否则审核员会去改规则表达式）", id)
		}
	}
}
