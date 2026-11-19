package maasapi

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestTracingMiddleware_CreatesSpan verifies each request produces a server span
// with route/tenant attributes when a real TracerProvider is installed.
func TestTracingMiddleware_CreatesSpan(t *testing.T) {
	exp := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exp))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	previous := tracerProviderFunc
	tracerProviderFunc = func() trace.TracerProvider { return tp }
	defer func() { tracerProviderFunc = previous }()

	e := echo.New()
	e.Use(tracingMiddleware())
	e.POST("/v0.1/_trace_probe", func(c *echo.Context) error {
		return c.JSON(200, map[string]string{"ok": "true"})
	})

	req := httptest.NewRequest("POST", "/v0.1/_trace_probe", nil)
	req.Header.Set("X-Tenant-ID", "ten_trace_001")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, 200, rec.Code)

	spans := exp.Ended()
	require.Len(t, spans, 1, "exactly one server span expected")

	span := spans[0]
	assert.Equal(t, "MAAS POST /v0.1/_trace_probe", span.Name())

	attrs := map[string]string{}
	for _, a := range span.Attributes() {
		attrs[string(a.Key)] = a.Value.Emit()
	}
	assert.Equal(t, "ten_trace_001", attrs["maas.tenant_id"])
	assert.Equal(t, "/v0.1/_trace_probe", attrs["http.route"])
	assert.Equal(t, "POST", attrs["http.method"])
	assert.Equal(t, "200", attrs["http.status_code"])
}

// TestTracingMiddleware_RecordsError verifies handler errors are recorded on the span.
func TestTracingMiddleware_RecordsError(t *testing.T) {
	exp := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(exp))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	previous := tracerProviderFunc
	tracerProviderFunc = func() trace.TracerProvider { return tp }
	defer func() { tracerProviderFunc = previous }()

	e := echo.New()
	e.Use(tracingMiddleware())
	e.POST("/v0.1/_trace_err", func(c *echo.Context) error {
		return assert.AnError
	})

	req := httptest.NewRequest("POST", "/v0.1/_trace_err", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	spans := exp.Ended()
	require.Len(t, spans, 1)
	assert.NotEmpty(t, spans[0].Events(), "handler error should be recorded as span event")
}
