package streaming

import (
	anthropictransform "github.com/kaixuan/llm-gateway-go/domains/transformation/anthropic"
	tooldialect "github.com/kaixuan/llm-gateway-go/internal/tooldialect"
)

// toolArgsDialect 是 anthropic→chat 桥所用的工具参数流方言（生产装配）。
//
// 装配点在此包（它同时依赖 transformation 的校验器与 tooldialect 的接口）：
//   - 注册表覆盖 builtin 种子（json.Valid 兜底版），注入
//     anthropictransform.ValidateStreamingToolArgs 的整文档校验/保守修复器；
//   - 桥直接持有该实例（免去每事件查表，且不受后续注册影响）；
//   - 不经此包装配的消费者（如 internal/ir 的单元测试）仍由 tooldialect
//     的 builtin 种子保证占位符语义正确——语义不依赖装配顺序。
//
// 新厂商/协议接入的模式（OCP）：在 tooldialect 实现并注册该厂商方言，
// 解析器与各桥零改动。
var toolArgsDialect = func() tooldialect.Dialect {
	d := tooldialect.NewAnthropic(anthropictransform.ValidateStreamingToolArgs)
	tooldialect.Register(tooldialect.ProtocolAnthropicMessages, d)
	return d
}()
