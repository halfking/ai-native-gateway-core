package pipeline

import "github.com/kaixuan/llm-gateway-go/domain" //nolint:depguard // historical: pipeline↔domain

// DispatchKind 声明 Hook 的执行语义。
// 未实现 Kinded 的 Hook 一律按 Inline 处理（向后兼容）。
type DispatchKind string

const (
	// DispatchInline 即时阻碍式：Execute 返回前必须给出结果；
	// 失败走 phasepolicy（terminate / continue / suspend）。
	DispatchInline DispatchKind = "inline"
	// DispatchTaskBus 异步任务式：Execute 只做「入队」，
	// 真正业务在 taskbus 上由 Handler 最终一致地执行。
	DispatchTaskBus DispatchKind = "task_bus"
)

// Kinded 接入时自动申明类型（实现即声明，无需注册处配置）。
type Kinded interface {
	Hook
	DispatchKind() DispatchKind
}

// TaskSpec 异步任务的合同三件套。
// 实现 TaskBus 的 Hook 还须实现 TaskSpecced；Execute 变为「构造 payload→入队」。
type TaskSpec struct {
	// TaskType 全局唯一任务类型，如 "cache.save.v1" / "asm.event.v1"。
	TaskType string
	// IdempotencyOf 幂等键（at-least-once 必备），如 requestID+turn。
	IdempotencyOf func(env *domain.PipelineRequest) string
	// SchemaVersion payload 版本，跨版本消费用。
	SchemaVersion int
}

// TaskSpecced 是 TaskBus Hook 的完整合同（Kinded + TaskSpec）。
type TaskSpecced interface {
	Kinded
	TaskSpec() TaskSpec
}

// ResolveDispatchKind 返回 hook 的声明语义；未实现 Kinded ⇒ Inline。
func ResolveDispatchKind(h Hook) DispatchKind {
	if k, ok := h.(Kinded); ok {
		if kind := k.DispatchKind(); kind != "" {
			return kind
		}
	}
	return DispatchInline
}
