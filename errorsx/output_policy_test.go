package errorsx

import (
	"errors"
	"fmt"
	"testing"
)

// 2026-11-17（节点状态/可重试性审计轮）：格式类守卫名单下沉到 errorsx。
//
// owner 报障实例＝ZCode 报障 31：rule=builtin.guard.tool_json_invalid 被判成
// 终态（retryable=false）。根因＝执行层拿不到规则名 ⇒ 无法区分「内容策略
// 拦截」与「上游输出畸形」⇒ 两者都是 KindContentFilter。
//
// 本文件钉住：名单内容、Unwrap 兼容（既有终态处理不变）、以及
// IsFormatGuardBlock 的判定边界。

func TestIsFormatGuardRule(t *testing.T) {
	for _, rule := range []string{
		GuardRuleToolJSONInvalid,
		GuardRuleRedactInvalid,
		GuardRuleFrameEncodeFailed,
	} {
		if !IsFormatGuardRule(rule) {
			t.Errorf("%q 必须是格式类守卫", rule)
		}
	}
	for _, rule := range []string{
		"",
		"builtin.network_destination",
		"builtin.egress_sink",
		"builtin.guard.capacity_exceeded",
		"builtin.guard.tool_json_invalid.extra",
	} {
		if IsFormatGuardRule(rule) {
			t.Errorf("%q 不得被当成格式类守卫", rule)
		}
	}
}

func TestOutputPolicyErrorUnwrapsToSentinel(t *testing.T) {
	err := &OutputPolicyError{RuleID: GuardRuleToolJSONInvalid, Reason: "tool arguments are not valid JSON"}
	if !errors.Is(err, ErrOutputPolicyBlocked) {
		t.Fatal("具名策略错误必须仍然 errors.Is(ErrOutputPolicyBlocked)")
	}
	// 既有线上处理（终态策略分支）依赖这条 ⇒ 不能被破坏。
	wrapped := fmt.Errorf("policy callback: %w", err)
	if !errors.Is(wrapped, ErrOutputPolicyBlocked) {
		t.Fatal("包装后仍须 errors.Is(ErrOutputPolicyBlocked)")
	}
	if got := PolicyBlockRuleID(wrapped); got != GuardRuleToolJSONInvalid {
		t.Fatalf("规则名在包装后丢失：got %q", got)
	}
	rule, reason, path, digest := PolicyBlockDetail(wrapped)
	if rule != GuardRuleToolJSONInvalid || reason != "tool arguments are not valid JSON" || path != "" || digest != "" {
		t.Fatalf("四元组提取错误：rule=%q reason=%q path=%q digest=%q", rule, reason, path, digest)
	}
}

func TestIsFormatGuardBlock(t *testing.T) {
	if IsFormatGuardBlock(nil) {
		t.Fatal("nil 不得判成格式守卫")
	}
	if IsFormatGuardBlock(errors.New("upstream 500")) {
		t.Fatal("普通错误不得判成格式守卫")
	}
	if IsFormatGuardBlock(ErrOutputPolicyBlocked) {
		t.Fatal("无规则名的裸 sentinel 不得判成格式守卫")
	}
	if !IsFormatGuardBlock(&OutputPolicyError{RuleID: GuardRuleToolJSONInvalid}) {
		t.Fatal("格式守卫必须被判成格式守卫")
	}
	if !IsFormatGuardBlock(&PolicyBlockError{RuleID: GuardRuleFrameEncodeFailed}) {
		t.Fatal("流式面的格式守卫必须被判成格式守卫")
	}
	if IsFormatGuardBlock(&PolicyBlockError{RuleID: "builtin.network_destination"}) {
		t.Fatal("内容策略规则不得被判成格式守卫")
	}
}
