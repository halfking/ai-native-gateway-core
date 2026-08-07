// Package v2: OutboundBuilder 从增量 delta 重建完整会话消息数组
//
// 职责：
//   1. 从多轮 request_delta + response_delta 拼接完整对话
//   2. 可选地过滤或保留压缩 marker（smm_v1:*）
//   3. 提供元数据（token 估算、消息数、轮次数）
//
// 设计参考：docs/会话优化v2/44-V2压缩层集成实施方案.md §2.1

package v2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/tokenest"
)

// OutboundBuilder 从增量重建完整会话上下文
type OutboundBuilder struct {
	reader *TurnReader
}

// NewOutboundBuilder 创建 OutboundBuilder 实例
func NewOutboundBuilder(reader *TurnReader) *OutboundBuilder {
	return &OutboundBuilder{reader: reader}
}

// BuildMeta 包含重建后的元数据
type BuildMeta struct {
	TotalTurns     int  // 重建使用的轮次数
	TotalMessages  int  // 重建后的消息数
	TokenEstimate  int  // token 估算
	HasCompression bool // 是否包含压缩 marker
	LastTurnNo     int  // 最新轮次号
}

// BuildFromDeltas 从增量重建完整会话上下文
//
// Parameters:
//   - ctx: context
//   - tenantID, sessionID: 会话标识
//   - lastN: 加载最近 N 轮（0 = 全部）
//   - preserveCompression: 是否保留压缩 marker（smm_v1:*）
//
// Returns:
//   - messages: 完整消息数组
//   - meta: 重建元数据
//   - error: 数据库或解析错误
func (b *OutboundBuilder) BuildFromDeltas(
	ctx context.Context,
	tenantID, sessionID string,
	lastN int,
	preserveCompression bool,
) ([]Message, *BuildMeta, error) {
	if b == nil || b.reader == nil {
		return nil, nil, fmt.Errorf("builder or reader is nil")
	}

	// Step 1: Load incremental deltas from session_bodies
	deltas, err := b.reader.LoadChain(ctx, tenantID, sessionID, lastN)
	if err != nil {
		return nil, nil, fmt.Errorf("load chain: %w", err)
	}

	if len(deltas) == 0 {
		// Empty session
		return []Message{}, &BuildMeta{}, nil
	}

	// Step 2: Filter compression markers if not preserving
	messages := deltas
	hasCompression := b.hasCompressionMarker(deltas)

	if !preserveCompression && hasCompression {
		messages = b.filterCompressionMarkers(deltas)
	}

	// Step 3: Build metadata
	meta := &BuildMeta{
		TotalMessages:  len(messages),
		TokenEstimate:  estimateTokens(messages),
		HasCompression: hasCompression,
	}

	return messages, meta, nil
}

// BuildFromLatestOutbound 从最近一次 outbound_body 重建（保留压缩状态）
//
// 与 BuildFromDeltas 的区别：
//   - BuildFromDeltas: 从所有轮次的 delta 拼接，可选择是否保留压缩
//   - BuildFromLatestOutbound: 直接返回最后一次发送给 LLM 的完整消息（包含压缩）
//
// 用途：压缩层需要获取上一轮实际发送的内容（含压缩 marker）
func (b *OutboundBuilder) BuildFromLatestOutbound(
	ctx context.Context,
	tenantID, sessionID string,
) ([]Message, *BuildMeta, error) {
	if b == nil || b.reader == nil {
		return nil, nil, fmt.Errorf("builder or reader is nil")
	}

	// Load latest outbound_body (preserves compression)
	messages, err := b.reader.LoadLatestOutbound(ctx, tenantID, sessionID)
	if err != nil {
		return nil, nil, fmt.Errorf("load latest outbound: %w", err)
	}

	if len(messages) == 0 {
		return []Message{}, &BuildMeta{}, nil
	}

	meta := &BuildMeta{
		TotalMessages:  len(messages),
		TokenEstimate:  estimateTokens(messages),
		HasCompression: b.hasCompressionMarker(messages),
	}

	return messages, meta, nil
}

// BuildLatestOutbound is the JSON-marshaled form of BuildFromLatestOutbound,
// matching the compression.V2OutboundBuilder interface. It returns the
// exact message array last forwarded to the upstream model (including
// compression markers) as a JSON []byte so the compression package does
// not need to import the v2 Message type.
func (b *OutboundBuilder) BuildLatestOutbound(
	ctx context.Context,
	tenantID, sessionID string,
) ([]byte, error) {
	messages, _, err := b.BuildFromLatestOutbound(ctx, tenantID, sessionID)
	if err != nil {
		return nil, err
	}
	return json.Marshal(messages)
}

// filterCompressionMarkers 过滤掉压缩 marker 消息
func (b *OutboundBuilder) filterCompressionMarkers(msgs []Message) []Message {
	var result []Message
	for _, msg := range msgs {
		if !b.isCompressionMarker(msg) {
			result = append(result, msg)
		}
	}
	return result
}

// hasCompressionMarker 检查消息数组中是否包含压缩 marker
func (b *OutboundBuilder) hasCompressionMarker(msgs []Message) bool {
	for _, msg := range msgs {
		if b.isCompressionMarker(msg) {
			return true
		}
	}
	return false
}

// isCompressionMarker 判断单条消息是否为压缩 marker
//
// 压缩 marker 格式：
//   - content 以 "[smm_v1:" 开头（session compression marker v1）
//   - content 以 "[summary:" 开头（legacy format）
func (b *OutboundBuilder) isCompressionMarker(msg Message) bool {
	content := msg.Content
	return strings.HasPrefix(content, "[smm_v1:") || strings.HasPrefix(content, "[summary:")
}

// estimateTokens 估算消息数组的 token 数
//
// 简单估算：1 token ≈ 3.5 字符（混合中英文），经 tokenest.FromChars 统一
// 与压缩路径一致（docs/omni-ref3 C4）。更精确的估算需要 tiktoken，但为
// 性能这里用简单规则。
func estimateTokens(msgs []Message) int {
	totalChars := 0
	for _, msg := range msgs {
		// Estimate role tokens
		totalChars += 10 // role + structure overhead

		// Estimate content tokens
		totalChars += len(msg.Content)

		// Estimate tool calls tokens
		if len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				// ToolCalls is []map[string]interface{}, estimate from JSON size
				if name, ok := tc["name"].(string); ok {
					totalChars += len(name)
				}
				if args, ok := tc["arguments"].(string); ok {
					totalChars += len(args)
				}
				totalChars += 20 // overhead
			}
		}

		// Estimate tool result tokens
		if msg.ToolCallID != "" {
			totalChars += len(msg.ToolCallID) + 10
		}
	}

	// docs/omni-ref3 C4: use the shared tokenest helper so the V2 builder agrees
	// with the compression path (previously this divided by 4 while its own
	// comment — and every other site — used 3.5).
	return tokenest.FromChars(totalChars)
}
