package streaming

// 拦截反馈的统一出口（2026-10-28 拦截反馈明确化轮）。
//
// ## 背景（本需求的直接动因）
//
// 用户侧（ZCode 报错卡）长期只能看到一句
//
//	Response blocked by output policy
//	TraceID: hydrate-trace
//
// ——没有规则、没有原因、没有可对账的网关请求 ID。r1039 已经把 rule/reason
// 放进了错误信封的**旁键**，但只显示 message 的客户端（ZCode 的报错卡就是）
// 一个字都看不到；/v1/responses 与 /v1/messages 的非流式路径甚至连旁键都没有。
// "TraceID: hydrate-trace" 是 ZCode 自己的内部标签——网关没给过它任何可
// 对账的 ID。
//
// ## 契约（本轮定死）
//
//   1. message 文本本身必须携带全部可行动信息（按最弱客户端设计）：
//      规则名、人类可读原因、位置、网关请求 ID、审核状态。机器可读的
//      旁键（rule/reason/field/request_id/review）继续保留。
//   2. 每次硬阻断都登记一条审核记录（rv-N），message 里带审核单号与
//      「等待审核」的提示——这就是「前端的显示是等待审核，有敏感内容」。
//   3. 管理员批准后（admin 后台），用户**重试**即拿到内容。

import (
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/sanitizerules"
)

// registerPolicyBlockReview 登记一次硬阻断的审核记录，返回可展示的审核单号
// （rv-N）；登记不了（极老形态 / 无租户）返回空串，message 走「联系管理员」
// 的兜底话术。
//
// 2026-10-29 拦截持久化轮：记录补 clientType（哪个客户端被拦）、规则展示名
// 与匹配说明（审核员判断「是不是规则问题」的入口）；记录落库（迁移 917），
// 重启可回溯。租户为空时也登记，但批准时空租户不放行（放行表 fail-closed）。
func registerPolicyBlockReview(tenantID, clientType, requestID, rule, reason, field, digest string) string {
	info, _ := sanitizerules.BuiltinRuleInfoFor(rule)
	entry := sanitizerules.AddBlockReview(tenantID, clientType, "egress", rule,
		info.Name, reason, field, digest, requestID, policyMatchDetail(rule, info, reason, field))
	return fmt.Sprintf("rv-%d", entry.ID)
}

// policyMatchDetail 组装审核记录的匹配说明。
//
// 结构化成分（规则语义 / 判定短语 / 参数位置 / 载荷摘要指纹）分行动词开头，
// 给审核员一条「这条规则在判什么 → 为什么命中我 → 是不是规则问题」的阅读线。
// 托管（YAML）规则的语义在前端按 rule_id 关联规则配置（这里不重复）。
func policyMatchDetail(rule string, info sanitizerules.BuiltinRuleInfo, reason, field string) string {
	var b strings.Builder
	if info.Description != "" {
		b.WriteString("规则语义：" + info.Description + "\n")
	} else {
		b.WriteString("规则语义：（托管规则，表达式与适用范围见规则配置）\n")
	}
	if reason != "" {
		b.WriteString("判定：" + reason + "\n")
	}
	if field != "" {
		b.WriteString("参数位置：" + field + "\n")
	}
	if rule != "" {
		b.WriteString("规则 ID：" + rule)
	}
	return strings.TrimSpace(b.String())
}

// policyBlockClientMessage 组装客户端可见的 message 文本。
//
// 形状（前缀逐字稳定，客户端做子串匹配的既有判据不破）：
//
//	Response blocked by output policy (rule=builtin.egress_sink):
//	  <reason> at $.command. request=<id>.
//	  内容含敏感信息，已进入后台审核（审核单 rv-7）：管理员放行后重试即可获得内容。
//
// 中文行动指引是有意的：报错卡的读者是中文用户，机读部分（前缀 + 旁键）
// 保持英文稳定。仓内先例：goal 重试的 SSE thinking 行就是中文上屏。
func policyBlockClientMessage(rule, reason, field, requestID, reviewID string) string {
	var b strings.Builder
	b.WriteString("Response blocked by output policy")
	if rule != "" {
		b.WriteString(" (rule=" + rule + ")")
	}
	if reason != "" {
		b.WriteString(": " + reason)
	}
	if field != "" {
		b.WriteString(" at " + field)
	}
	if requestID != "" {
		b.WriteString(" request=" + requestID + ".")
	} else {
		b.WriteString(".")
	}
	if reviewID != "" {
		b.WriteString(" 内容含敏感信息，等待审核（审核单 " + reviewID +
			"）：管理员在网关后台放行后，重试即可获得内容。")
	} else {
		b.WriteString(" 如需放行，请联系网关管理员（后台可按规则/按载荷放行，或调整硬阻断清单）。")
	}
	return b.String()
}

// policyBlockErrorExtras 组装错误信封的旁键（rule/reason/field/digest/
// request_id/review）。空值不进 map——响应形状里缺省键不出现。
func policyBlockErrorExtras(rule, reason, field, digest, requestID, reviewID string) map[string]any {
	extras := map[string]any{}
	if rule != "" {
		extras["rule"] = rule
	}
	if reason != "" {
		extras["reason"] = reason
	}
	if field != "" {
		extras["field"] = field
	}
	if digest != "" {
		extras["digest"] = digest
	}
	if requestID != "" {
		extras["request_id"] = requestID
	}
	if reviewID != "" {
		extras["review"] = reviewID
	}
	return extras
}
