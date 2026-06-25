// Package streaming 实现 SSE 流式响应领域。
//
// 阶段划分（与 pipeline.Phase 对应）：
//   - PhasePreUpstream   准备流式上下文（设置 ResponseChan、Stream 标志）
//   - PhaseUpstream      实际调用上游（生产中由旧 executor/ 接管）
//   - PhasePostUpstream  流式响应处理（本包的 StreamHook 触发 Streamer.Stream）
//
// 本包是 streaming（生产代码）的领域抽象子集：
//   - 不依赖旧 streaming 包（项目根级无 streaming/ 目录）
//   - 只暴露"流式"这个领域核心契约（Streamer/StreamChunk/StreamContext）
//   - 简化实现：SSEStreamer 把上游字节流包装为 StreamChunk 序列
package streaming

import "time"

// ChunkType 流式块类型。
const (
	// ChunkTypeContent 内容块（增量文本/工具调用参数）。
	ChunkTypeContent = "content"
	// ChunkTypeToolCall 工具调用块（结构化 tool_calls）。
	ChunkTypeToolCall = "tool_call"
	// ChunkTypeDone 流结束标记（上游明确表示结束）。
	ChunkTypeDone = "done"
	// ChunkTypeError 错误标记（流中段失败）。
	ChunkTypeError = "error"
)

// StreamChunk 流式块。
//
// 一次 Stream() 调用产生若干 StreamChunk；Streamer 必须 close(ch)
// 保证消费者能感知流结束。
type StreamChunk struct {
	// Data 块数据（字节切片）。
	Data []byte
	// Type 块类型（content / tool_call / done / error）。
	Type string
	// Index 块序号（从 1 开始；用于顺序追踪）。
	Index int
	// EmitAt 块发出时间。
	EmitAt time.Time
}

// Streamer 流式器接口。
//
// 实现方负责：
//   - 从 StreamContext.ResponseChan 读上游字节
//   - 包装为 StreamChunk 写入 ch
//   - 流结束（上游 ch 关闭）后 close(ch)
type Streamer interface {
	// Name 返回流式器名称（"sse" / "anthropic-sse" / ...）。
	Name() string
	// Stream 启动流式处理。
	// 必须保证：函数返回前会 close(ch)。
	Stream(ctx StreamContext, ch chan<- *StreamChunk) error
}

// StreamContext 流式上下文。
//
// 注意：与 Go 标准库 context.Context 区分；Pipeline 的 Go context 走
// Hook.Execute(ctx, env) 的第一个参数。
type StreamContext struct {
	// Request 转换后的请求体（用于与上游交互）。
	Request []byte
	// Model 目标模型名。
	Model string
	// ResponseChan 上游字节流（可读）。
	// Streamer 从中读取数据，关闭表示流结束。
	ResponseChan <-chan []byte
}
