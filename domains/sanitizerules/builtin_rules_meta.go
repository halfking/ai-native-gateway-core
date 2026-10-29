package sanitizerules

// builtin 规则的展示名与匹配说明表（2026-10-29 拦截持久化轮）。
//
// owner 口径：「在触发规则的记录中，需要有详细的匹配说明，用于检查是否是
// 规则问题。」⇒ 审核记录除了规则 ID，还要带**人能读懂的语义说明**——
// 「这条规则到底在判什么、为什么会命中我这个参数」。
//
//   - builtin.* 是代码级判定（不在规则快照里），说明只能在这里给；
//   - 托管（YAML）规则的展示名/表达式由前端按 rule_id 关联规则配置
//     （getSanitizeRules 已有），这里不重复；
//   - 表是**封闭集**：未知 ID 返回 ok=false，前端退化为只显示规则 ID。
//
// 判定链路的完整语义见各规则的实现注释：
//   - builtin.egress_sink / builtin.network_destination：
//     security/sanitize/egress_direction.go / egress_host.go
//   - builtin.secret_field.egress：security/sanitize/output_sensitive.go

import "strings"

// BuiltinRuleInfo 是一条内建规则的展示元数据。
type BuiltinRuleInfo struct {
	// Name 是规则的短展示名（审核列表的主标题）。
	Name string
	// Description 是匹配说明：这条规则判什么形状、命中意味着什么。
	Description string
}

// BuiltinRuleInfoFor 查询内建规则的展示元数据；未知 ID 返回 ok=false。
func BuiltinRuleInfoFor(ruleID string) (BuiltinRuleInfo, bool) {
	info, ok := builtinRuleDescriptions[strings.TrimSpace(ruleID)]
	return info, ok
}

var builtinRuleDescriptions = map[string]BuiltinRuleInfo{
	// 方向规则（egress_direction.go）：只认真正的数据外流——命令段里同时出现
	// 「公网可达主机」与「发送动词/本地数据插值/上传型客户端」。
	"builtin.egress_sink": {
		Name: "外传方向（把本地数据送往公网）",
		Description: "检测命令参数里「把本地数据发送到公网主机」的形态：下载型命令（curl/wget 不带 -d/--data 等发送动词）不判，" +
			"命令段同时含公网可达主机与发送动作（POST/上传/管道本地文件/自定义头）才判。命中说明这条参数看起来要把本地数据送到外部主机。",
	},
	// 形状规则（egress_host.go + output_sensitive.go 的显式判定）：参数里提到
	// 公网主机（URL 主机位 / host:port / netcat 裸主机）就算命中——下载也算。
	"builtin.network_destination": {
		Name: "公网网络目的地（形状）",
		Description: "检测工具参数里出现的公网主机形状（URL 主机位、host:port、netcat 目标）。它与方向规则互相独立：" +
			"提到公网主机就命中（curl 下载一个 SDK、clone 一个仓库都算），因此对 coding agent 误伤面较大；默认不在硬阻断清单里。",
	},
	// 凭据字段明文（output_sensitive.go）：字段名是密码族 + 值不是占位符。
	"builtin.secret_field.egress": {
		Name: "凭据字段明文",
		Description: "检测「密码族字段（password/passwd/pwd/secret/token/api_key/authorization 等）携带非占位符明文值」。" +
			"身份族字段（username/account）不在此列（r1024：身份不是机密）；模型自造的占位拼写（PLACEHOLDER/<...> 等）也放行（r1024b）。",
	},
}

// builtinRuleName 返回内建规则的短展示名；未知 ID 返回空串（前端退化显示 ID）。
func builtinRuleName(ruleID string) string {
	if info, ok := builtinRuleDescriptions[strings.TrimSpace(ruleID)]; ok {
		return info.Name
	}
	return ""
}

// BuiltinHardBlockAll 返回「全部允许硬阻断」的清单：请求侧与输出侧全量 +
// egress 侧逐条内建规则（r1039 禁止 egress 通配，所以这里显式枚举）。
//
// egress 侧的 ID 集合 = 显式内建规则 + 片段回退形态（fragmentRuleID 对没有
// RuleID 的片段给 "builtin.<type>"，类型集合与 structuralType 一致——两边
// 漂移时 r1041 的清单闸会把未知 ID 当「不在清单里」，方向是安全的）。
func BuiltinHardBlockAll() []HardBlockAllow {
	allows := []HardBlockAllow{
		{Scope: "input", Rule: "*"},
		{Scope: "output", Rule: "*"},
		{Scope: "egress", Rule: "builtin.egress_sink"},
		{Scope: "egress", Rule: "builtin.network_destination"},
		{Scope: "egress", Rule: "builtin.secret_field.egress"},
	}
	for _, typ := range []string{"phone", "id_card", "email", "credit_card", "secret", "account", "server_ip", "internal_ip", "custom"} {
		allows = append(allows, HardBlockAllow{Scope: "egress", Rule: "builtin." + typ})
	}
	return allows
}
