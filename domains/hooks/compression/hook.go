package compression

import (
	"context"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// MetaKeyNeedsCompression 启用压缩的 metadata 标记。
//
// 调用方在请求需要压缩时设置 env.Metadata[MetaKeyNeedsCompression] = true。
// 不设置或设为 false 时 Hook 跳过执行（避免在短上下文场景无谓调用）。
const MetaKeyNeedsCompression = "needs_compression"

// MetaKeyMessages 输入消息在 metadata 中的键名。
//
// 调用方负责在 Metadata 中以 []Message 形式存放待压缩消息。
const MetaKeyMessages = "messages"

// MetaKeyCompressedMessages 压缩后消息在 metadata 中的键名。
//
// Hook 执行后把压缩结果写入 env.Metadata[MetaKeyCompressedMessages]。
const MetaKeyCompressedMessages = "compressed_messages"

// MetaKeyCompressionStrategy 记录实际采用的压缩策略。
const MetaKeyCompressionStrategy = "compression_strategy"

// MetaKeyCompressionError 记录压缩错误（失败可降级）。
const MetaKeyCompressionError = "compression_error"

// CompressionHook 把 Compressor 接入 Pipeline。
//
// 行为：
//   - Enabled: env.Metadata[MetaKeyNeedsCompression] == true
//   - Execute: 从 Metadata[MetaKeyMessages] 读取消息，调用 Compressor.Compress，
//     把结果写回 Metadata[MetaKeyCompressedMessages]
//   - OnError: 吞掉 error（压缩失败可降级，原始 messages 仍可被下游使用）
//
// 适用阶段：PhasePreTransform 或 PhaseTransform（与其他转换串联）。
//
// 注意：本 Hook 不直接修改 env.TransformedRequest——压缩结果仅放在
// Metadata 中供下游 Hook 读取。这样设计保证：
//   1. 压缩是可选的（如果不读 Metadata，行为无变化）
//   2. 与 transformation 领域的 TransformHook 不冲突
type CompressionHook struct {
	compressor Compressor
}

// NewCompressionHook 构造 CompressionHook。
func NewCompressionHook(c Compressor) *CompressionHook {
	return &CompressionHook{compressor: c}
}

// Name 返回 Hook 名称。
func (h *CompressionHook) Name() string { return "compression.apply" }

// Priority 返回 Hook 优先级（Transform 阶段）。
func (h *CompressionHook) Priority() int { return 100 }

// Enabled 报告 Hook 是否启用。
func (h *CompressionHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	if env == nil || env.Metadata == nil {
		return false
	}
	needs, ok := env.Metadata[MetaKeyNeedsCompression].(bool)
	if !ok || !needs {
		return false
	}
	// 还需要有 Compressor
	return h.compressor != nil
}

// Execute 执行压缩。
func (h *CompressionHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env.Metadata == nil {
		env.Metadata = make(map[string]any)
	}

	// 从 Metadata 读取消息列表（可能不存在）
	var msgs []Message
	if raw, ok := env.Metadata[MetaKeyMessages]; ok {
		if typed, ok := raw.([]Message); ok {
			msgs = typed
		}
	}

	cctx := Context{
		Messages:  msgs,
		MaxTokens: 4096,
	}

	if err := h.compressor.Compress(&cctx); err != nil {
		env.Metadata[MetaKeyCompressionError] = err.Error()
		return err
	}

	env.Metadata[MetaKeyCompressedMessages] = cctx.Messages
	env.Metadata[MetaKeyCompressionStrategy] = string(h.compressor.Strategy())
	return nil
}

// OnError 压缩失败：吞掉 error（降级到原始消息）。
func (h *CompressionHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil && env.Metadata != nil {
		env.Metadata[MetaKeyCompressionError] = err.Error()
		// 保留原始 messages 作为 fallback
		if _, exists := env.Metadata[MetaKeyCompressedMessages]; !exists {
			if raw, ok := env.Metadata[MetaKeyMessages]; ok {
				env.Metadata[MetaKeyCompressedMessages] = raw
			}
		}
	}
	return nil
}

// Compressor 返回底层 Compressor（用于测试与 telemetry）。
func (h *CompressionHook) Compressor() Compressor { return h.compressor }

// 编译期接口断言。
var _ pipeline.Hook = (*CompressionHook)(nil)
