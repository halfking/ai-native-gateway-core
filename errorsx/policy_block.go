package errorsx

import (
	"errors"
	"fmt"
)

// PolicyBlockError 说明「这次输出被哪条规则拦下了」。
//
// owner 口径（2026-10-20）：「就算是被拦截，应该在中断时反馈给客户因为什么规则
// 被拦截，不要搞空响应」。
//
// 211 事故里这条链路是断的：拦截错误在流式拦截器里被降级成一个不带原因的
// `errors.New("output policy blocked stream event")`，规则名在半路丢了；客户端
// 最终只看到「模型未返回任何内容」（empty_model_response），运维在日志里也只看
// 到 "blocked stream event" 而看不到 reason_codes（lane 那条分支有、event 这条
// 没有）。
//
// RuleID 稳定可机器消费：客户端用它区分「策略拦截」与「上游故障」，运维用它
// 在硬阻断清单里按名开/关。
type PolicyBlockError struct {
	// RuleID 是命中的规则 ID（如 builtin.network_destination）。
	RuleID string
	// Reason 是人类可读的原因短语。
	Reason string
}

func (e *PolicyBlockError) Error() string {
	if e == nil {
		return "output policy blocked"
	}
	if e.RuleID == "" {
		return "output policy blocked: " + e.Reason
	}
	return fmt.Sprintf("output policy blocked (rule=%s): %s", e.RuleID, e.Reason)
}

// PolicyBlockRuleID 从错误链里取出规则 ID；不是策略拦截错误时返回空串。
func PolicyBlockRuleID(err error) string {
	var pe *PolicyBlockError
	if errors.As(err, &pe) && pe != nil {
		return pe.RuleID
	}
	return ""
}
