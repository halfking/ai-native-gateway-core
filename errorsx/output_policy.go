package errorsx

import (
	"errors"
	"fmt"
	"strings"
)

// ErrOutputPolicyBlocked is a gateway policy decision made before any response
// bytes are committed. It is terminal for routing, but is not provider failure
// evidence and does not imply a protocol error has already been rendered.
var ErrOutputPolicyBlocked = errors.New("response blocked by output policy")

// 格式类守卫（format guard）的规则 ID。
//
// 格式类守卫拦的不是**敏感内容**，而是**结构性的格式问题**：
//   - GuardRuleToolJSONInvalid:   上游返回的工具参数本身不是合法 JSON
//   - GuardRuleRedactInvalid:     脱敏后的结果不是合法 JSON
//   - GuardRuleFrameEncodeFailed: 帧无法重新编码
//
// ★ 2026-11-17（节点状态/可重试性审计轮）：这份名单此前只存在于
// domains/streaming/policy_block_feedback.go 的 isFormatGuard 里，而
// **执行层（domains/streaming/executors）拿不到它** —— 执行层只拿到
// 一个裸的 errorsx.ErrOutputPolicyBlocked ⇒ 无法区分「内容策略拦截」
// 与「上游输出格式错」⇒ 两者都被判成 KindContentFilter（终态、
// 不可重试）⇒ owner 报障「简单的错误应该可以重试，解决不了」。
//
// 单一事实源搬到这里：errorsx 是所有层都能 import 的低层包 ⇒
// 执行层、dispatch 层、管理面共用同一份名单，不会再漂移。
const (
	GuardRuleToolJSONInvalid   = "builtin.guard.tool_json_invalid"
	GuardRuleRedactInvalid     = "builtin.guard.redact_invalid"
	GuardRuleFrameEncodeFailed = "builtin.guard.frame_encode_failed"
)

// IsFormatGuardRule reports whether ruleID belongs to the format-guard
// family. Format guards block a malformed upstream output, not sensitive
// content: an operator release cannot change the fact that the upstream
// emitted invalid JSON, and the correct recovery is to ask again / try a
// sibling node.
func IsFormatGuardRule(ruleID string) bool {
	switch strings.TrimSpace(ruleID) {
	case GuardRuleToolJSONInvalid, GuardRuleRedactInvalid, GuardRuleFrameEncodeFailed:
		return true
	default:
		return false
	}
}

// OutputPolicyError is the typed form of ErrOutputPolicyBlocked. It carries
// the rule ID so downstream layers can tell a format-guard block (retryable
// upstream-output defect) from a content-policy block (terminal policy
// decision) instead of collapsing both into one opaque sentinel.
type OutputPolicyError struct {
	// RuleID is the rule that blocked the output (e.g. builtin.guard.tool_json_invalid).
	RuleID string
	// Reason is the human-readable reason phrase, when known.
	Reason string
	// Path locates the blocked payload inside the response (e.g. $.tool.0.arguments).
	Path string
	// Digest identifies the blocked payload for the management-side release flow.
	Digest string
}

func (e *OutputPolicyError) Error() string {
	if e == nil {
		return "response blocked by output policy"
	}
	if e.RuleID == "" {
		return ErrOutputPolicyBlocked.Error()
	}
	return fmt.Sprintf("%s (rule=%s)", ErrOutputPolicyBlocked.Error(), e.RuleID)
}

// Unwrap keeps errors.Is(err, ErrOutputPolicyBlocked) true for every typed
// policy error, so all existing terminal-policy handling keeps working.
func (e *OutputPolicyError) Unwrap() error { return ErrOutputPolicyBlocked }

// IsFormatGuardBlock reports whether err is an output-policy block caused by
// a format guard (malformed upstream output) rather than a content rule.
//
// 两种具名形态都认：*OutputPolicyError（非流式执行层从策略信封解析）与
// *PolicyBlockError（流式拦截器构造）。裸 sentinel（无规则名）不认 ——
// 没有规则名就没有「格式守卫 vs 内容策略」的判据。
//
// ★ 边界（务必保留）：调用方必须另行确认**客户端尚未收到任何字节** ——
// 已经提交过内容的流式响应不可重放，否则会把同一段内容发两遍。
func IsFormatGuardBlock(err error) bool {
	if err == nil {
		return false
	}
	rule, _, _, _ := PolicyBlockDetail(err)
	if rule == "" {
		return false
	}
	if errors.Is(err, ErrOutputPolicyBlocked) {
		return IsFormatGuardRule(rule)
	}
	var pe *PolicyBlockError
	if errors.As(err, &pe) {
		return IsFormatGuardRule(rule)
	}
	return false
}
