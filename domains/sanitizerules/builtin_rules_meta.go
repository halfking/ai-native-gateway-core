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
	// ★★ 守卫侧失败（2026-10-31 拦截归因轮）★★
	//
	// ★ 这些 ID **不是内容规则**：★ 它们表示「输出守卫没能完成判定 ⇒
	// fail-closed 终止回合」⇒ ⇒ 审核员看到它们时**不该**去改规则表达式
	//（改了也不会让它不再触发）⇒ ⇒ 要改的是**容量 / 形态 / 上游可用性** ★。
	//
	// ★ 为什么必须逐条登记（★ 此前这些 ID **不在本表** ★）：★ 审核单的
	// 「规则语义」行取自本表 ⇒ ★ 取不到就退化成一句
	// 「（托管规则，表达式与适用范围见规则配置）」⇒ ★★ 那是指向**错误事实源**
	// 的提示（这些根本不是托管规则）★★ ⇒ ★★ 审核员会去托管规则里找一条
	// 永远找不到的规则 ★★。
	"builtin.guard.capacity_exceeded": {
		Name: "守卫待检缓冲越界（非规则命中）",
		Description: "响应正文太长，输出守卫的待检缓冲（出厂 1 MiB）装不下 ⇒ **不是任何内容规则命中**，而是基础设施容量问题。" +
			"默认（release）会降级放行并留痕，响应已送达客户端；只有严格模式（LLM_GATEWAY_OUTPUT_GUARD_CAP_MODE=block）才会终止回合。" +
			"处置方向：调大 LLM_GATEWAY_OUTPUT_GUARD_PENDING_MAX_BYTES（注意 2026-10-02 存储审计定的 1 MiB wire cap 契约），不是改规则表达式。",
	},
	"builtin.guard.no_request_state": {
		Name: "守卫缺少请求态（无法跨帧检查）",
		Description: "这一帧没有可用的请求态 ⇒ 输出守卫无法做跨帧检查 ⇒ 无法证明这段输出安全 ⇒ fail-closed 终止回合。" +
			"**不是内容规则命中**。处置方向：查请求态为何缺失（生产写者必须填 StreamMeta.State），不是改规则表达式。",
	},
	"builtin.guard.rewrite_refused": {
		Name: "守卫拒绝改写不可改写的工具参数",
		Description: "这段工具参数命中了需脱敏的规则，但该 lane **不允许安全改写**（已签名 / 敏感字段）⇒ " +
			"既不能漏检放行、也不能改写 ⇒ fail-closed 终止回合。**不是内容规则命中**，也不是误报判定依据。" +
			"处置方向：让该 lane 的脱敏可行（可改写形态），或对该客户端/租户做规则级放行。",
	},
	"builtin.guard.redact_invalid": {
		Name: "脱敏结果不是合法 JSON",
		Description: "工具参数脱敏后**不再是合法 JSON** ⇒ 不能把坏参数交给客户端（会打断工具调用）⇒ fail-closed 终止回合。" +
			"**不是内容规则命中**。处置方向：修脱敏规则/掩码形态（改写要保持 JSON 合法），不是放宽拦截。",
	},
	"builtin.guard.check_failed": {
		Name: "合规检查器失败（判不出合规性）",
		Description: "检查器报错或返回空结果 ⇒ 判不出这段内容是否合规 ⇒ fail-closed 终止回合。**不是内容规则命中**。" +
			"处置方向：查检查器错误（DB / 规则快照 / 上游），不是改规则表达式。",
	},
	"builtin.guard.frame_encode_failed": {
		Name: "帧改写后无法重新编码",
		Description: "改写后的帧无法重新序列化（JSON 编码失败）⇒ 不能把半成品帧发给客户端 ⇒ fail-closed 终止回合。" +
			"**不是内容规则命中**。处置方向：查帧结构与序列化，不是改规则表达式。",
	},
	// ★ 2026-11-01 12h 审计轮：补登记「工具参数本身不是合法 JSON」这条形态。
	//
	// ★ 为什么必须单列（★ 而不能并进 builtin.guard.redact_invalid ★）：
	// ★ redact_invalid 说的是「**脱敏之后**结果不是合法 JSON」（脱敏规则/掩码
	// ★ 形态的问题）★；本条说的是「**脱敏之前**上游给的工具参数就不是合法
	// ★ JSON」（客户端/上游发了坏参数）★ ⇒ ★ 处置方向完全相反 ★ ⇒ ★ 合成规则
	// ★ 名必须能区分 ★，否则审核员会被指向错误的处置面 ★。
	"builtin.guard.tool_json_invalid": {
		Name: "工具参数不是合法 JSON",
		Description: "这段工具参数**本身**就不是合法 JSON ⇒ 无法按 JSON 解析出结构 ⇒ 无法在结构上做脱敏/判定 ⇒ fail-closed 终止回合。" +
			"**不是内容规则命中**（这条路径根本没有走到规则判定）。" +
			"处置方向：查上游客户端/协议适配为何发出非法 JSON 的工具参数，不是改规则表达式、也不是改脱敏掩码。",
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
