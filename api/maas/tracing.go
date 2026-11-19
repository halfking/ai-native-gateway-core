package maasapi

import (
	"github.com/labstack/echo/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// tracerProviderFunc 返回 MaaS HTTP 层使用的 TracerProvider。默认指向
// otel.GetTracerProvider（宿主进程由 internal/observability/tracer.go 的
// InitTracer 初始化全局 provider），未初始化时为 no-op，零开销。变量形式
// 以便测试注入 sdktrace.TracerProvider。
var tracerProviderFunc = otel.GetTracerProvider

// tracingMiddleware 为每个请求创建 server span，并把租户/路由作为 span 属性。
// 不把请求体写入 span（PII/凭据风险），只记录结构性元数据。
func tracingMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			ctx := c.Request().Context()
			ri := c.RouteInfo()
			route := redactRoute(ri.Path)

			spanName := "MAAS " + c.Request().Method + " " + route
			ctx, span := tracerProviderFunc().Tracer("maas-api").
				Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			// 结构化属性（无请求体，无 PII）。
			span.SetAttributes(
				attribute.String("maas.tenant_id", c.Request().Header.Get("X-Tenant-ID")),
				attribute.String("http.route", route),
				attribute.String("http.method", c.Request().Method),
			)

			// 把携带 span 的 ctx 注回请求，供下游领域服务透传。
			c.SetRequest(c.Request().WithContext(ctx))

			err := next(c)
			if err != nil {
				span.RecordError(err)
			}
			if res, ok := c.Response().(*echo.Response); ok {
				span.SetAttributes(attribute.Int("http.status_code", res.Status))
			}
			return err
		}
	}
}
