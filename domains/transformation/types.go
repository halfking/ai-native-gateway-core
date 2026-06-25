// Package transformation 实现请求/响应转换领域。
//
// 阶段划分（与 pipeline.Phase 对应）：
//   - PhasePreTransform   准备转换上下文（model 解析、profile 加载）
//   - PhaseTransform      应用 Transformer 链（sanitize → compress → ...）
//   - PhasePostTransform  转换后处理（审计、metrics）
//
// 本包是 transform/（生产代码）的领域抽象子集：
//   - 不依赖旧 transform 包（保证可独立编译）
//   - 不复刻 sanitizer.go 等 Provider 特定实现
//   - 只暴露"转换"这个领域核心契约（Transformer/Context）
//
// 设计要点：
//   - Transformer 是一条流水线（Chain of Responsibility 模式）
//   - 每个 Transformer 只做一件事（如：sanitize / compress / rename）
//   - TransformHook 把多个 Transformer 串成一个 Pipeline Hook
package transformation

// Transformer 转换器接口。
//
// 一个 Transformer 只做一件事（如：清洗、压缩、字段重命名）。
// 多个 Transformer 通过 TransformHook 串联。
type Transformer interface {
	// Name 返回转换器名称（用于日志/调试）。
	Name() string
	// Transform 在 ctx 上应用转换。
	// 返回 error 表示转换失败；OnError 决定是否继续。
	Transform(ctx Context) error
}

// Context 转换上下文。
//
// 注意：与 Go 标准库 context.Context 区分；Pipeline 的 Go context 走
// Hook.Execute(ctx, env) 的第一个参数。
//
// 设计：Context 是值类型（轻量），Transformer 修改 Context 的 Request/Metadata
// 等字段后，由 TransformHook 把变更回写到 PipelineRequest。
type Context struct {
	// Request 当前请求体（字节切片，约定为 JSON）。
	// 允许为 nil（表示"无请求体"）。
	Request []byte
	// Response 当前响应体（字节切片，约定为 JSON）。
	// 允许为 nil。
	Response []byte
	// Model 目标模型名称（用于模型特定转换）。
	Model string
	// Metadata 跨阶段元数据（转换标记、配置等）。
	Metadata map[string]any
}
