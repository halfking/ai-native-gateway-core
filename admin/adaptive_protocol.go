// adaptive_protocol.go — 「协议自适应」在 admin 写入侧的入口。
//
// 写入契约与读取侧（provider/adaptive_protocol.go）必须成对：admin 允许把
// 'adaptive' 存进 providers.protocol，候选加载出口负责把它消解成具体协议。任
// 一侧缺失都会产生「存得进去但没人消解」或「消解了但存不进去」的半条链。
package admin

import (
	providercatalog "github.com/kaixuan/llm-gateway-go/provider/catalog"
)

// ProviderSettingKeyAdaptiveAllowed 是「上游真实可用的协议」声明键。
// 与读取侧共用 catalog 常量，避免 SQL 字面量两侧漂移。
const ProviderSettingKeyAdaptiveAllowed = providercatalog.AdaptiveAllowedSettingKey

// normalizeProviderProtocolInput 是 providers.protocol 写入的唯一校验入口。
//
// 它在既有严格契约（别名归一、未知值报错）之上只多接受一个值：自适应选择器。
// 之所以不复用 providercatalog.NormalizeProviderProtocol 直接放行，是因为那个
// 函数同时被 provider_catalog 的 Validate 共用，"adaptive" 一旦进入 canonical
// 枚举就会让脏 catalog 行通过校验，并让未消解的选择器漏到执行器。
//
// 选择器按**精确值**接受（大小写/空白折叠），不做前缀匹配——"adapt"、
// "adaptive-v2" 之类仍然 400，避免把校验口子开成通配。
func normalizeProviderProtocolInput(raw string) (string, error) {
	if providercatalog.IsAdaptiveProtocol(raw) {
		return providercatalog.ProtocolAdaptive, nil
	}
	return providercatalog.NormalizeProviderProtocol(raw)
}
