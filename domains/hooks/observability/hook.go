package observability

import (
	"context"
	"time"

	"github.com/kaixuan/llm-gateway-go/domain"
	"github.com/kaixuan/llm-gateway-go/domains/pipeline"
)

// TracingHook 追踪 Hook
type TracingHook struct {
	tracer Tracer
}

// NewTracingHook 创建追踪 Hook
func NewTracingHook(tracer Tracer) *TracingHook {
	return &TracingHook{tracer: tracer}
}

// Name 返回 Hook 名称
func (h *TracingHook) Name() string { return "observability.trace" }

// Priority 返回优先级（最早执行）
func (h *TracingHook) Priority() int { return 1 }

// Enabled 是否启用
func (h *TracingHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 启动 trace span
func (h *TracingHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	span := h.tracer.StartSpan("pipeline.execute", nil)
	if env.Envelope != nil {
		span.Tags["request_id"] = env.Envelope.RequestID
	}
	span.Tags["tenant_id"] = env.TenantID
	env.Metadata["trace_span"] = span
	return nil
}

// OnError 错误处理
func (h *TracingHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

// MetricsHook 指标 Hook
type MetricsHook struct {
	registry *Registry
}

// NewMetricsHook 创建指标 Hook
func NewMetricsHook(registry *Registry) *MetricsHook {
	return &MetricsHook{registry: registry}
}

// Name 返回 Hook 名称
func (h *MetricsHook) Name() string { return "observability.metrics" }

// Priority 返回优先级
func (h *MetricsHook) Priority() int { return 50 }

// Enabled 是否启用
func (h *MetricsHook) Enabled(ctx context.Context, env *domain.PipelineRequest) bool {
	return env != nil
}

// Execute 记录指标
func (h *MetricsHook) Execute(ctx context.Context, env *domain.PipelineRequest) error {
	latency := time.Since(env.CreatedAt).Milliseconds()

	labels := map[string]string{
		"tenant_id": env.TenantID,
	}
	if env.Error != nil {
		labels["status"] = "error"
	} else {
		labels["status"] = "ok"
	}

	h.registry.Counter("requests_total", labels).Inc()
	h.registry.Histogram("request_latency_ms",
		[]float64{10, 50, 100, 500, 1000, 5000}, labels).Observe(float64(latency))
	return nil
}

// OnError 错误处理
func (h *MetricsHook) OnError(ctx context.Context, env *domain.PipelineRequest, err error) error {
	return nil
}

var (
	_ pipeline.Hook = (*TracingHook)(nil)
	_ pipeline.Hook = (*MetricsHook)(nil)
)
