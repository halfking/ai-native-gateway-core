package streaming

import (
	anthropictransform "github.com/kaixuan/llm-gateway-go/domains/transformation/anthropic"
	tooldialect "github.com/kaixuan/llm-gateway-go/internal/tooldialect"
)

// bridgeToolArgsDialect 返回 anthropic→chat 桥当前生效的工具参数流方言。
//
// 装配分叉收敛（2026-11-11 48h 审计遗留项）：此前桥持有 init 时**钉死的
// 实例**，而 internal/ir 走 ForProtocol **活查表** ⇒ 任何后续
// Register(anthropic, …) 会静默分裂两侧语义（ir 用新方言、桥用旧实例）。
// 现统一收敛到注册表这一**单一语义源**：桥与 ir 同表同读，Register 即
// 全链生效（OCP：新厂商方言注册即被两侧同时看见）。RWMutex RLock 的
// 每事件查表成本（ns 级）远低于该路径上任何一次 JSON 解析。
//
// 注册仍在本包 init（生产装配点）：把 builtin 种子（json.Valid 兜底版）
// 升级为注入 anthropictransform.ValidateStreamingToolArgs 的整文档校验/
// 保守修复版。不经此包装配的消费者（如 internal/ir 的单元测试）仍由
// tooldialect 的内置种子保证占位符语义正确——语义不依赖装配顺序。
func init() {
	tooldialect.Register(tooldialect.ProtocolAnthropicMessages,
		tooldialect.NewAnthropic(anthropictransform.ValidateStreamingToolArgs))
}

func bridgeToolArgsDialect() tooldialect.Dialect {
	return tooldialect.ForProtocol(tooldialect.ProtocolAnthropicMessages)
}
