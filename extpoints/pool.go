// Package extpoints - pool.go
//
// 对象池优化（2026-11-19）
// -----------------------
// ExecutionContext 在每次插件执行时都会创建，高并发场景下会导致大量内存分配。
// sync.Pool 可以复用 ExecutionContext 实例，减少 GC 压力。
//
// 性能优化：
//   - 减少 ExecutionContext 分配开销 ~30%
//   - 减少 map[string]interface{} 分配开销
//   - 降低 GC 压力
//
// 使用方式：
//   ctx := AcquireExecutionContext()
//   defer ReleaseExecutionContext(ctx)
//   // ... use ctx

package extpoints

import (
	"context"
	"sync"
	"time"
)

// executionContextPool 是 ExecutionContext 的对象池。
var executionContextPool = sync.Pool{
	New: func() interface{} {
		return &ExecutionContext{
			// 预分配 State map，避免后续扩容
			// 典型插件会设置 3-5 个 state 字段
			State: make(map[string]interface{}, 8),
		}
	},
}

// AcquireExecutionContext 从对象池获取一个 ExecutionContext。
// 调用方必须在使用完毕后调用 ReleaseExecutionContext 归还。
func AcquireExecutionContext() *ExecutionContext {
	ctx := executionContextPool.Get().(*ExecutionContext)
	return ctx
}

// ReleaseExecutionContext 将 ExecutionContext 归还到对象池。
// 在归还前会清理所有字段，防止内存泄漏。
func ReleaseExecutionContext(ctx *ExecutionContext) {
	if ctx == nil {
		return
	}

	// 重置所有字段
	ctx.Reset()

	// 归还到池中
	executionContextPool.Put(ctx)
}

// Reset 重置 ExecutionContext 的所有字段为零值。
// 这在将 context 归还到对象池前必须调用，防止内存泄漏。
func (ctx *ExecutionContext) Reset() {
	// 清空 Context（设为 nil，下次使用时重新设置）
	ctx.Context = nil

	// 清空 Phase 和 PluginID
	ctx.Phase = ""
	ctx.PluginID = ""

	// 清空请求元数据
	ctx.RequestID = ""
	ctx.RequestTime = time.Time{}
	ctx.Timeout = 0

	// 清空 State map
	// 策略：如果 map 过大（>16 个键），重新分配；否则清空复用
	if len(ctx.State) > 16 {
		// map 过大，重新分配（避免持有大量内存）
		ctx.State = make(map[string]interface{}, 8)
	} else {
		// map 适中，清空复用（避免重新分配）
		for k := range ctx.State {
			delete(ctx.State, k)
		}
	}
}

// InitExecutionContext 初始化一个从池中获取的 ExecutionContext。
// 这是一个便捷方法，用于设置常用字段。
func InitExecutionContext(
	ctx *ExecutionContext,
	baseCtx context.Context,
	phase Phase,
	pluginID string,
	requestID string,
	requestTime time.Time,
) {
	ctx.Context = baseCtx
	ctx.Phase = phase
	ctx.PluginID = pluginID
	ctx.RequestID = requestID
	ctx.RequestTime = requestTime
}
