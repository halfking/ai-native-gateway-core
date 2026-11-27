// adaptive_protocol.go — 「协议自适应」选择策略的单一事实源。
//
// 背景（2026-11-28 供应商协议自适应需求）：
//
//	providers 表一行只有一个 (base_url, protocol)，一个供应商下挂的多种模型
//	（vapeur 生产上 474 个，跨 8 家厂商）因此被迫共用同一条线格式。运营把
//	protocol 设成 openai-responses 后，claude-* / gemini-* 也跟着走 Responses
//	线格式，与其原厂格式（Messages / generateContent）不一致。
//
//	本文件引入「自适应」选择器：providers.protocol = 'adaptive' 时不表示某种
//	线格式，而表示「按请求模型在各原厂推荐格式里择优」。
//
//	★ 关键设计约束（三条，缺一不可）：
//
//	 1. "adaptive" 不是线格式，绝不进 Protocols 枚举。
//	    Protocols 被 provider_catalog.protocol 的 CHECK 语义（Validate）共用，
//	    把 'adaptive' 混进去会让脏 catalog 行通过校验，更会让未消解的选择器
//	    漏到执行器（落 default 分支按 chat 处理，行为与配置不符——正是
//	    2026-09-23 vapeur "openai-response" 旧事故的同型缺陷）。
//
//	 2. 消解点唯一：候选加载出口（provider/client.go 两处）。
//	    cand.Protocol 全链路是唯一的线格式维度，在那儿消解之后，分发路由、
//	    请求体转换、出站 URL、响应解析、用量口径全部自动继承，20+ 读取点
//	    零改动。
//
//	 3. 未声明可用协议 ⇒ 自适应完全惰性。
//	    原厂推荐值只说明「该厂商官方怎么走」，不说明「这个上游真的提供那条
//	    端点」。聚合中转（vapeur / openrouter 这类）常常只暴露
//	    /chat/completions。因此推荐值必须经过运营声明（provider_settings
//	    键 protocol.adaptive.allowed）过滤后才会生效；没有声明时行为与启用
//	    自适应之前逐字节相同。
package catalog

// ProtocolAdaptive 是「按模型选原厂推荐协议」的选择器，**不是**线格式。
//
// 它被接受为 providers.protocol 的取值（该列无 CHECK 约束），但：
//   - 不在 Protocols 里（见本文件约束 1）；
//   - NormalizeProviderProtocol 对它返回错误；
//   - 必须在候选加载出口经 ResolveAdaptiveProtocol 消解成具体协议。
const ProtocolAdaptive = "adaptive"

// AdaptiveAllowedSettingKey 是 provider_settings 里承载「上游真实可用的协议」
// 声明的键。**这是唯一真相**：admin 写入侧与 provider 读取侧都引用本常量，
// 两侧曾各自写字面量，改一侧忘另一侧时 SQL 里静默不命中、声明永远读不到。
//
// 值是**有序**的 JSON 字符串数组，顺序即优先级，首项为基协议：
//
//	["openai-completions","openai-responses"]
//
// 之所以要运营声明而不是直接用模型推荐值：推荐值只说明「该厂商官方怎么走」，
// 不说明「这个上游真的提供那条端点」。聚合中转（vapeur / openrouter 这类）常
// 常只暴露 /chat/completions，凭模型名强行切 /v1/messages 会把现在能跑的模型
// 打挂。未声明 ⇒ 自适应完全惰性（见 ResolveAdaptiveProtocol 判定顺序第 1 条）。
const AdaptiveAllowedSettingKey = "protocol.adaptive.allowed"

// IsAdaptiveProtocol 判断 raw 是否是自适应选择器。
// 折叠规则与 NormalizeProviderProtocol 一致：trim、小写。
func IsAdaptiveProtocol(raw string) bool {
	return normalizeProtocolKey(raw) == ProtocolAdaptive
}

// NormalizeAdaptiveAllowed 把运营声明的「上游真实可用的协议」列表归一：
// 别名折叠为 canonical、丢弃无法识别的项与空串、丢弃重复项，**保留顺序**。
//
// 顺序有意义：allowed[0] 是基协议（见 ResolveAdaptiveProtocol）。
func NormalizeAdaptiveAllowed(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, entry := range raw {
		// 选择器本身不接受为「可用协议」——那会形成自我引用的消解环。
		if IsAdaptiveProtocol(entry) {
			continue
		}
		normalized, err := NormalizeProviderProtocol(entry)
		if err != nil {
			continue
		}
		if _, dup := seen[normalized]; dup {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ResolveAdaptiveProtocol 在「自适应」模式下为 rawModel 选出实际出站协议。
//
// allowed 是运营声明的、有序的可用协议列表（provider_settings 键
// protocol.adaptive.allowed，形如 ["openai-responses","openai-completions"]）。
//
// 判定顺序：
//
//  1. allowed 为空（未声明 / 全是脏值）⇒ 自适应惰性，返回 openai-completions。
//     这是全仓 12 处 `COALESCE(p.protocol,'openai-completions')` 已经确立的
//     既有默认，因此「开了自适应但没声明」与「开之前」逐字节等价。
//  2. 模型有原厂推荐 且 推荐协议在 allowed 内 ⇒ 用推荐协议。
//  3. 否则 ⇒ 用 allowed[0]（基协议）。
//
// 返回値恒为 Protocols 的成员：既不会是 ProtocolAdaptive，也不会是空串，
// 因此执行器永远不会看到一个它无法分发的值。
func ResolveAdaptiveProtocol(rawModel string, allowed []string) string {
	normalized := NormalizeAdaptiveAllowed(allowed)
	if len(normalized) == 0 {
		return ProtocolOpenAICompletions
	}
	base := normalized[0]
	if recommended, ok := RecommendedProtocolForModel(rawModel); ok {
		for _, candidate := range normalized {
			if candidate == recommended {
				return recommended
			}
		}
	}
	return base
}
