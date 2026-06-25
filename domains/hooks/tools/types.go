// Package tools 实现工具调用拦截领域 (Hook)。
// 阶段: PostTransform (拦截/扩展 tool calls)
package tools

import "time"

// ToolCall 工具调用
type ToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
	Meta      map[string]any
	CreatedAt time.Time
}

// ToolResult 工具结果
type ToolResult struct {
	ID      string
	Success bool
	Output  any
	Error   string
}

// Interceptor 拦截器接口
type Interceptor interface {
	Name() string
	// Intercept 在 tool call 前后调用
	// 返回 (modifiedCalls, nil) 应用修改
	// 返回 (nil, err) 阻断
	Intercept(ctx Context) ([]*ToolCall, error)
}

// Context 拦截器上下文
type Context struct {
	Calls    []*ToolCall
	TenantID string
	Metadata map[string]any
}
