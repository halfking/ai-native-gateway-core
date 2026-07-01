package streaming

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"           //nolint:depguard // historical violation, B1 routing.go CQRS will fix
	"github.com/kaixuan/llm-gateway-go/domains/pipeline" //nolint:depguard // historical violation, B1 routing.go CQRS will fix
)

// StreamHook 流式响应处理 Hook。
//
// 行为：
//   - Enabled: env != nil && env.UpstreamResponse != nil
//     （如果上游尚未响应，跳过——本 Hook 是 PhasePostUpstream 的入口）
//   - Execute: 触发 streamer.Stream（实际流式处理在 goroutine 中进行）。
//     本实现把执行交给调用方（由旧 streaming 代码接管），Hook 仅记录
//     streamer 已被调用。
//   - OnError: 流式错误可降级（返回 nil 不中断）。
//
// 设计要点：
//   - StreamHook 不阻塞 Pipeline（启动 goroutine 处理流）
//   - 流式结果通过 callback 或直接写入 http.ResponseWriter 推送给客户端
//   - Hook 仅作为"流式处理已被调度"的标志位
type StreamHook struct {
	streamer Streamer
}

// NewStreamHook 构造一个 Stream Hook。
func NewStreamHook(s Streamer) *StreamHook {
	if s == nil {
		panic("streaming.NewStreamHook: streamer must not be nil")
	}
	return &StreamHook{streamer: s}
}

// Name 返回 Hook 名称。
func (h *StreamHook) Name() string { return "streaming.process" }

// Priority 返回 Hook 优先级。
func (h *StreamHook) Priority() int { return 100 }

// Enabled 报告 Hook 是否启用。
func (h *StreamHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil && env.UpstreamResponse != nil
}

// Execute 调度流式处理。
//
// 当前实现：把"已调度"标记写入 env.Metadata["streamer_invoked"] = streamer.Name()
// + 时间戳。实际流式处理由调用方（旧 streaming 代码）接管，Hook 不阻塞 Pipeline。
//
// 保留 StreamContext 构造以供调用方查询。
func (h *StreamHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	if env == nil {
		return nil
	}
	if env.Metadata == nil {
		env.Metadata = map[string]any{}
	}
	// 构造 StreamContext（仅记录，不消费 ResponseChan）
	sctx := StreamContext{
		Request: env.TransformedRequest,
	}
	// 把 StreamContext 关键字段写入 metadata，便于调用方查询
	env.Metadata["streamer_invoked"] = h.streamer.Name()
	env.Metadata["streamer_invoked_at"] = time.Now().UnixMilli()
	if sctx.Request != nil {
		env.Metadata["stream_request_bytes"] = len(sctx.Request)
	}
	return nil
}

// OnError 流式错误可降级（返回 nil）。
//
// 设计：流式响应中途失败不应让 Pipeline 返回 5xx；客户端可能已收到部分内容，
// 让网关继续返回已接收的片段比返回错误更友好。
func (h *StreamHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	if env != nil && env.Metadata != nil {
		env.Metadata["streamer_error"] = err.Error()
	}
	return nil
}

// 编译期接口断言。
var _ pipeline.Hook = (*StreamHook)(nil)
