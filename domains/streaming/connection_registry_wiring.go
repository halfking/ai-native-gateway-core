package streaming

// 会话优化 v4 T4/R1.6：ConnectionRegistry 在流式 ingress 的挂接。
//
// 数据流（docs/会话优化v4/24-实施计划 §3.1）：
//
//	Register(request_id, SerializedFrameWriter(session), meta)
//	    → liveactions 事件（node_switch/model_switch/retry/...）
//	    → ActionBridge.dispatch → registry.WriteFrame(request_id, frame)
//	    → 客户端收到 `: thinking: ...` 注释帧（不进入语义 wire）
//	Unregister(request_id, reason) → admin 投影保留最近关闭记录
//
// 安全边界：Register/Unregister 失败一律静默降级（Debug 日志），
// 绝不影响请求主路径；注册表只存 id/标签，不存正文（R1.6）。

import (
	"log/slog"
)

// registryProtocolLabel 把 ir.DetectProtocol 的协议字符串
// （openai-completions / openai-chat / openai-responses /
// anthropic-messages / gemini-generate / unknown）映射到
// RegistrationMetadata.Protocol 的词汇表（protocolMetricLabel：
// openai_chat / openai_responses / anthropic / unknown）。
// 该标签只用于语义思考帧的方言选择；注释兜底路径不消费它。
func registryProtocolLabel(clientProtocol string) string {
	switch clientProtocol {
	case "anthropic-messages", "anthropic":
		return "anthropic"
	case "openai-responses", "openai_responses", "responses":
		return "openai_responses"
	case "openai-chat", "openai-completions", "openai_chat":
		return "openai_chat"
	default:
		return "unknown"
	}
}

// registerStreamConnection 把 preStream 会话登记进连接注册表。
// 任何一个前置条件不满足都静默返回（T4 是旁路能力，不能成为请求
// 路径的失败源）。重复 request_id 由 registry 以 "replaced" 语义
// 自行处理。
func (h *ChatHandler) registerStreamConnection(psk *preStreamKeepalive, requestID, clientProtocol, clientType, tenantID string) {
	if h == nil || h.connectionRegistry == nil || psk == nil || requestID == "" {
		return
	}
	serialized, ok := psk.Writer().(*serializedResponseWriter)
	if !ok {
		return
	}
	meta := RegistrationMetadata{
		Protocol:   registryProtocolLabel(clientProtocol),
		ClientType: clientType,
		TenantID:   tenantID,
	}
	if err := h.connectionRegistry.Register(
		requestID,
		NewSerializedFrameWriter(serialized.SerializedWriter()),
		meta,
		nil,
	); err != nil {
		// 满容量（ErrConnectionRegistryFull）是设计内的降级：连接照常
		// 流式，只是收不到桥接思考帧、不出现在 admin 投影里。
		slog.Debug("connection_registry_register_skipped",
			"request_id", requestID, "reason", err.Error())
	}
}

// unregisterStreamConnection 是对称的收尾；reason 会进入 admin 投影的
// 关闭审计。not-registered（未注册或已清理）不算错误。
func (h *ChatHandler) unregisterStreamConnection(requestID, reason string) {
	if h == nil || h.connectionRegistry == nil || requestID == "" {
		return
	}
	_ = h.connectionRegistry.Unregister(requestID, reason)
}
