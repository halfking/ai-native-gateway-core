package tools

import "time"

// MetaToolInterceptor meta-tool 拦截器
// 为每个 tool call 注入 meta 信息（拦截时间、租户 ID）
type MetaToolInterceptor struct {
	metaKey string
}

// NewMetaToolInterceptor 创建 meta-tool 拦截器
func NewMetaToolInterceptor(metaKey string) *MetaToolInterceptor {
	if metaKey == "" {
		metaKey = "_meta"
	}
	return &MetaToolInterceptor{metaKey: metaKey}
}

// Name 返回拦截器名称
func (m *MetaToolInterceptor) Name() string { return "metatool" }

// Intercept 注入 meta 字段到每个 tool call
func (m *MetaToolInterceptor) Intercept(ctx Context) ([]*ToolCall, error) {
	now := time.Now()
	for _, call := range ctx.Calls {
		if call.Meta == nil {
			call.Meta = make(map[string]any)
		}
		call.Meta["_intercepted_at"] = now
		call.Meta["_tenant_id"] = ctx.TenantID
	}
	return ctx.Calls, nil
}
