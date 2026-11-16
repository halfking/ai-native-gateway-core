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
	// Path 是被拦载荷的位置（tool JSON 路径，如 $.command）。2026-10-28
	// 拦截审核轮：与 Digest 一起构成「这一次拦截」的定位信息。
	Path string
	// Digest 是被拦载荷的摘要（security/sanitize.EgressPayloadDigest 的输出）。
	// 管理面据它在审核队列里对这一次载荷精确放行；空串表示该路径没有
	// 可绑定的载荷（如纯文本 lane 的规则级拦截）。
	Digest string
	// Evidence 是被拦内容的**有界摘录**（原始会话内容），2026-10-29 输出
	// 合规误报轮补。
	//
	// ★ 它是**审核面的输入**，不是客户端可见内容：客户端拿到的仍是
	// rule/reason/field/request_id（见 domains/streaming 的
	// policyBlockClientMessage）。摘录由拦截器在**拦截那一刻**裁好并传上来，
	// 管理面落进**证据表**（带 7 天 TTL）——审核员据此判断「这次拦得对不对」。
	//
	// ★ 不进错误信封：PolicyBlockError 会被日志/审计行引用，载荷原文不该
	// 出现在那些地方。
	Evidence string
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
//
// ★ 2026-11-17：同时认 OutputPolicyError（执行层从策略信封里解析出的
// 具名类型）—— 此前只有流式拦截路径会构造 *PolicyBlockError，非流式路径
// 只回一个裸 sentinel ⇒ 规则名在非流式面丢失 ⇒ 无法区分格式守卫与
// 内容策略（见 errorsx/output_policy.go）。
func PolicyBlockRuleID(err error) string {
	var pe *PolicyBlockError
	if errors.As(err, &pe) && pe != nil {
		return pe.RuleID
	}
	var oe *OutputPolicyError
	if errors.As(err, &oe) && oe != nil {
		return oe.RuleID
	}
	return ""
}

// PolicyBlockDetail 从错误链里取出策略拦截的四元组（规则 / 原因 / 位置 /
// 摘要）。两种具名类型都认：*PolicyBlockError（流式拦截路径构造）与
// *OutputPolicyError（非流式执行层从策略信封解析，见 output_policy.go）。
//
// ★ 单一事实源：domains/streaming 侧的 policyBlockErrorDetail /
// policyBlockPathFromError / policyBlockDigestFromError 三个 helper 统一委派
// 到这里 —— 此前它们各自只认 *PolicyBlockError ⇒ 非流式面的规则名/位置/
// 摘要**整段丢失**。
func PolicyBlockDetail(err error) (rule, reason, path, digest string) {
	var pe *PolicyBlockError
	if errors.As(err, &pe) && pe != nil {
		return pe.RuleID, pe.Reason, pe.Path, pe.Digest
	}
	var oe *OutputPolicyError
	if errors.As(err, &oe) && oe != nil {
		return oe.RuleID, oe.Reason, oe.Path, oe.Digest
	}
	return "", "", "", ""
}
