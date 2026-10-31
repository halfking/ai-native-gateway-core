// Package nativepair 判定「原厂编码智能体 ↔ 原厂模型」直连对。
//
// 背景（2026-10-28 原厂直通轮）：zcode / minimax-code / deepseek-code 是
// 各自厂商官方的编码智能体，它们对**自己厂商 API** 的参数集最权威。网关对
// 这类请求的正确姿态是【首发全参数透传】——不改写 tool_choice / n /
// reasoning_effort，不简化工具 schema，不做学习式预剥离；只有上游报错时
// 才由执行器 reqprobe 的 4xx 剥参重试回退到骨架参数。非原生对（其它客户端、
// 或原生客户端打到别厂模型）行为完全不变。
//
// 2026-10-29 厂商感知修正（nvidia 直通事故轮）：原实现只按「客户端 × 模型名
// 前缀」判对，zcode → **任何厂商托管的** glm-*（含 nvidia 的
// z-ai/glm-5.3-flash）都命中直通。但参数兼容性是【端点 API 形状】的属性，
// 不是模型权重的属性：ZCode 的 reasoning/enable_thinking 是智谱线格式参数，
// nvidia 的 OpenAI 形状端点不认（211 生产实测 2026-10-29 00:04-01:32Z：
// "Validation: Unsupported parameter(s): `reasoning`" / `enable_thinking`
// 每请求两轮 400 剥参重试，53 个请求 ×600KB body ×2 次白费往返，TTFB 29s+；
// 且直通腿豁免学习式预剥离 ⇒ Redis 学习记录（已 recovered_count=1）永远
// 无法回应用 ⇒ 永不收敛）。2026-10-28 生产验证轮 §10.1 自己登记的
// 「第三方宿主路由 + 厂商参数组合部署后尚未自然出现，待自然复现」——
// 本轮就是那次自然复现，结论推翻「按模型名判原厂对」。
//
// 修正语义：原厂直通 = 客户端 × 模型族 × **候选的原厂 catalog code**。
// 第三方宿主（nvidia / 其它已知厂商）与空 catalog 中继一律走正常适配路径：
// paramguard + 扩展方言裁剪 + reqprobe 学习式预剥离全部生效（学习记录
// 已在 Redis，部署后首个请求即前置剥离、首发 200）。
//
// 映射采用内置代码表而非 provider_catalog 的 DB 属性（2026-10-28 轮裁决）：
// 原生对集合极小且变动慢，新对只需在 pairRules 加一行。homeCatalog 的取值
// 与 internal/paramreg 的 catalogToDialect 同源（原厂码 + 已知别名）。
package nativepair

import (
	"os"
	"strings"
	"sync"
)

// pairRule 一条原厂直连对：客户端类型 → 模型族前缀 + 该族原厂 catalog code 集。
// 模型名匹配段：名字等于前缀，或以「前缀-」开头；容忍 "vendor/name" 形态的
// 宿主前缀（如 z-ai/glm-5.2 —— 宿主前缀只用于取名，判对靠 catalogCode）。
type pairRule struct {
	client      string
	modelPrefix string
	// homeCatalog 是该模型族【原厂端点】的 catalog code 集合（含别名）。
	// 候选的 CatalogCode 不在集合内 ⇒ 它是第三方宿主/中继 ⇒ 不直通。
	homeCatalog map[string]bool
}

func catalogSet(codes ...string) map[string]bool {
	m := make(map[string]bool, len(codes))
	for _, c := range codes {
		m[c] = true
	}
	return m
}

// pairRules 内置映射表。clientType 须已按 clienttype.Normalize 归一。
var pairRules = []pairRule{
	{"zcode", "glm", catalogSet("zhipu", "glm", "bigmodel", "zai")},
	{"minimax-code", "minimax", catalogSet("minimax", "minimaxai")},
	{"deepseek-code", "deepseek", catalogSet("deepseek", "deepseek-ai")},
	// RISK-6（2026-10-29）：Claude Code↔Claude 原厂对。claude-code 说
	// anthropic-messages 协议，主路径走 executor_anthropic.go（Q4 直传，
	// 无协议转换）；模型名族 claude-*（宿主前缀形态 anthropic/claude-…
	// 已由 Match 剥离）。转售 claude 的第三方（apiclaude 等）不在原厂集内。
	{"claude-code", "claude", catalogSet("anthropic")},
}

var enabled = sync.OnceValue(func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_NATIVE_PASSTHROUGH")))
	return v != "off" && v != "false" && v != "0"
})

// Enabled 报告原厂直通是否启用。默认开；LLM_GATEWAY_NATIVE_PASSTHROUGH=
// off/false/0 时整体关闭（紧急回退开关，与 PARAMREG_ENABLED 同款惯例）。
func Enabled() bool { return enabled() }

// Match 判定 (clientType, catalogCode, outboundModel) 是否构成原厂直连对。
//
// clientType 须已按 clienttype.Normalize 归一；catalogCode 是候选的
// provider catalog code（provider.Candidate.CatalogCode，可为空——
// 空 = 未挂 catalog 的中转，按第三方宿主处理，不直通）；outboundModel 是
// 实际出站模型名（可带 "vendor/name" 宿主前缀）。未知客户端、空模型、
// 模型族对上但宿主非原厂，一律 false。
func Match(clientType, catalogCode, outboundModel string) bool {
	if !enabled() || clientType == "" || outboundModel == "" {
		return false
	}
	clientType = strings.ToLower(strings.TrimSpace(clientType))
	rule := pairRuleFor(clientType)
	if rule == nil {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(outboundModel))
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	if model == "" {
		return false
	}
	if model != rule.modelPrefix && !strings.HasPrefix(model, rule.modelPrefix+"-") {
		return false
	}
	// 厂商感知闸门（2026-10-29）：模型族对上只是必要条件；直通只授予
	// 该族【原厂端点】。nvidia 托管的 z-ai/glm-5.3-flash 在这里判否——
	// 它的 API 形状不是智谱的，ZCode 的厂商参数对它是 400 工厂。
	return rule.homeCatalog[strings.ToLower(strings.TrimSpace(catalogCode))]
}

// pairRuleFor 找到该客户端的原厂对规则（未登记返回 nil）。
func pairRuleFor(clientType string) *pairRule {
	for i := range pairRules {
		if pairRules[i].client == clientType {
			return &pairRules[i]
		}
	}
	return nil
}
