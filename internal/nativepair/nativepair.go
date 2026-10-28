// Package nativepair 判定「原厂编码智能体 ↔ 原厂模型」直连对。
//
// 背景（2026-10-28 原厂直通轮）：zcode / minimax-code / deepseek-code 是
// 各自厂商官方的编码智能体，它们对自己厂商模型的参数集最权威。网关对这类
// 请求的正确姿态是【首发全参数透传】——不改写 tool_choice / n /
// reasoning_effort，不简化工具 schema，不做学习式预剥离；只有上游报错时
// 才由执行器 reqprobe 的 4xx 剥参重试回退到骨架参数。非原生对（其它客户端、
// 或原生客户端打到别厂模型）行为完全不变。
//
// 映射采用内置代码表而非 provider_catalog 的 DB 属性：
//   - provider_catalog 没有 admin 写路径（只能由迁移/种子工具写），
//     加属性意味着迁移 + admin API + 前端三面工程；
//   - 厂商归属本来就能从模型名前缀推出（catalog/display.go 的
//     nameVendorPrefixes 与 internal/reasoncap 的内置默认表同款先例：
//     内置默认 + 环境开关兜底）；
//   - 原生对集合极小且变动慢（跟随各厂商发官方工具的节奏），新对只需在
//     clientModelPrefixes 加一行。
package nativepair

import (
	"os"
	"strings"
	"sync"
)

// clientModelPrefixes 内置映射：原厂智能体（clienttype 归一化名）→
// 该厂模型名前缀。模型名匹配段：名字等于前缀，或以「前缀-」开头；
// 容忍 "vendor/name" 形态的宿主前缀（如 z-ai/glm-5.2）。
var clientModelPrefixes = map[string][]string{
	"zcode":         {"glm"},
	"minimax-code":  {"minimax"},
	"deepseek-code": {"deepseek"},
	// RISK-6（2026-10-29）：Claude Code↔Claude 原厂对。claude-code 说
	// anthropic-messages 协议，主路径走 executor_anthropic.go（Q4 直传，
	// 无协议转换）；模型名族 claude-*（claude-sonnet-5 / claude-opus-4.5 /
	// claude-haiku-…，宿主前缀形态 anthropic/claude-… 已由 Match 剥离）。
	"claude-code": {"claude"},
}

var enabled = sync.OnceValue(func() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_GATEWAY_NATIVE_PASSTHROUGH")))
	return v != "off" && v != "false" && v != "0"
})

// Enabled 报告原厂直通是否启用。默认开；LLM_GATEWAY_NATIVE_PASSTHROUGH=
// off/false/0 时整体关闭（紧急回退开关，与 PARAMREG_ENABLED 同款惯例）。
func Enabled() bool { return enabled() }

// Match 判定 (clientType, outboundModel) 是否构成原厂直连对。
//
// clientType 须已按 clienttype.Normalize 归一；outboundModel 是实际出站
// 模型名（可带 "vendor/name" 宿主前缀）。未知客户端、空模型一律 false。
func Match(clientType, outboundModel string) bool {
	if !enabled() || clientType == "" || outboundModel == "" {
		return false
	}
	prefixes, ok := clientModelPrefixes[strings.ToLower(strings.TrimSpace(clientType))]
	if !ok {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(outboundModel))
	if i := strings.LastIndexByte(model, '/'); i >= 0 {
		model = model[i+1:]
	}
	if model == "" {
		return false
	}
	for _, p := range prefixes {
		if model == p || strings.HasPrefix(model, p+"-") {
			return true
		}
	}
	return false
}
