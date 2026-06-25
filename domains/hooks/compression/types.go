// Package compression 实现上下文压缩领域 (Hook)。
//
// 阶段定位：
//   - CompressionHook → PhasePreTransform / PhaseTransform
//     （在 TransformedRequest 之后、Upstream 之前压缩上下文）
//
// 与旧 compressor/ 包的关系：
//   - 本包是新抽象（与 Hook Pipeline 对齐），不依赖旧 compressor/*.go
//   - 旧代码保持不变；本包定义最小契约（Compressor / Context / Message / Strategy）
//   - LCSCompressor 是一个简化示例实现（截断超出窗口的消息）
//
// 设计动机：
//   - 把"压缩"作为一个 Hook 暴露在 Pipeline 中，便于按需启用
//   - 调用方可通过 env.Metadata[MetaKeyNeedsCompression]=true 显式触发
//   - 策略可插拔（lcs / llm_summ / none）
package compression

import "errors"

// HookStrategy 压缩策略标识（Hook 抽象层）。
//
// 注意：与 compressor.CompressionStrategy（底层 telemetry 用的策略）不同。
// 本类型是 Hook 抽象层定义的最小契约；下游可选用实现（本包提供 LCS / Noop）。
type HookStrategy string

const (
	// HookStrategyNone 不压缩（恒等映射）。
	HookStrategyNone HookStrategy = "none"
	// HookStrategyLCS 最长公共子序列去重 / 滑动窗口截断。
	HookStrategyLCS HookStrategy = "lcs"
	// HookStrategyLLMSumm 调用 LLM 总结（需要外部凭据，未在本包实现）。
	HookStrategyLLMSumm HookStrategy = "llm_summ"
)

// Message 简化的消息结构（用于压缩上下文）。
//
// 只保留压缩所需的最小字段（Role + Content），不绑定 OpenAI/Anthropic
// 等具体 Provider 的消息 schema。调用方负责把 Provider 消息转换为本结构。
type Message struct {
	// Role 角色（"system" / "user" / "assistant" / "tool"）。
	Role string
	// Content 文本内容（多模态暂不支持）。
	Content string
}

// Context 压缩器输入上下文。
//
// 注意：与 Go 标准库 context.Context 不同；Pipeline 的 Go context 走
// Hook.Execute(ctx, env) 的第一个参数。
type Context struct {
	// Messages 待压缩的消息列表（按时间顺序）。
	Messages []Message
	// MaxTokens 上限；<=0 表示不强制限制。
	MaxTokens int
}

// ErrEmptyContext 输入消息为空。
var ErrEmptyContext = errors.New("compression: empty context")

// Compressor 压缩器接口。
//
// 实现必须：
//   - Name() 返回稳定标识（用于 telemetry）
//   - Strategy() 返回策略类型
//   - Compress() 在 ctx 上原地修改 ctx.Messages；返回非 nil error 时表示失败
type HookCompressor interface {
	// Name 返回压缩器名称。
	Name() string
	// Strategy 返回策略类型。
	Strategy() HookStrategy
	// Compress 应用压缩。原地修改 ctx.Messages（通过 *Context 指针）。
	Compress(ctx *Context) error
}

// ---------- LCSCompressor ----------

// LCSCompressor 滑动窗口截断式压缩器（简化 LCS 实现）。
//
// 行为：
//   - MaxTokens<=0 → 默认 4096
//   - 输入消息数 <=10：不做任何处理
//   - 输入消息数 >10：保留最后 10 条（HistoryClass 之前的消息被截断）
//
// 简化说明：
//   - 真正的 LCS 去重需要 O(n*m) DP，超出本 Hook 演示范围
//   - 此实现演示"按窗口大小截断"——保证总长度有界
//   - 调用方可通过实现自己的 Compressor 接入真实 LCS 算法
type LCSCompressor struct {
	maxTokens int
	maxRetain int // 最多保留的消息数
}

// NewLCSCompressor 构造 LCS 压缩器。
// maxTokens<=0 使用默认 4096；maxRetain 固定为 10（简化策略）。
func NewLCSCompressor(maxTokens int) *LCSCompressor {
	if maxTokens <= 0 {
		maxTokens = 4096
	}
	return &LCSCompressor{
		maxTokens: maxTokens,
		maxRetain: 10,
	}
}

// Name 返回压缩器名称。
func (c *LCSCompressor) Name() string { return "lcs" }

// Strategy 返回策略类型。
func (c *LCSCompressor) Strategy() HookStrategy { return HookStrategyLCS }

// Compress 应用截断压缩。
func (c *LCSCompressor) Compress(ctx *Context) error {
	if len(ctx.Messages) == 0 {
		return nil
	}
	if len(ctx.Messages) > c.maxRetain {
		ctx.Messages = ctx.Messages[len(ctx.Messages)-c.maxRetain:]
	}
	return nil
}

// MaxTokens 返回配置的最大 token 数。
func (c *LCSCompressor) MaxTokens() int { return c.maxTokens }

// ---------- NoopCompressor ----------

// NoopCompressor 恒等映射压缩器（用于测试 / 显式禁用场景）。
type NoopCompressor struct{}

// NewNoopCompressor 构造 NoopCompressor。
func NewNoopCompressor() *NoopCompressor { return &NoopCompressor{} }

// Name 返回压缩器名称。
func (NoopCompressor) Name() string { return "noop" }

// Strategy 返回策略类型。
func (NoopCompressor) Strategy() HookStrategy { return HookStrategyNone }

// Compress 恒等映射（不修改消息）。
func (NoopCompressor) Compress(ctx *Context) error { return nil }

// 编译期接口断言。
var (
	_ HookCompressor = (*LCSCompressor)(nil)
	_ HookCompressor = (*NoopCompressor)(nil)
)
