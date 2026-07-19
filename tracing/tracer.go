package tracing

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.17.0"
	"go.opentelemetry.io/otel/trace"
)

// Config 追踪配置
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	Endpoint       string        // OTLP endpoint
	SampleRate     float64       // 采样率 0.0-1.0
	Timeout        time.Duration
}

// DefaultConfig 默认配置
func DefaultConfig() Config {
	return Config{
		ServiceName:    "llm-gateway",
		ServiceVersion: "1.0.0",
		Environment:    "development",
		Endpoint:       "localhost:4317",
		SampleRate:     1.0, // 100% 采样
		Timeout:        10 * time.Second,
	}
}

// Tracer 追踪器
type Tracer struct {
	config   Config
	provider *sdktrace.TracerProvider
	tracer   trace.Tracer
}

// NewTracer 创建追踪器
func NewTracer(config Config) (*Tracer, error) {
	// 创建资源
	res, err := resource.New(
		context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(config.ServiceName),
			semconv.ServiceVersion(config.ServiceVersion),
			semconv.DeploymentEnvironment(config.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource: %w", err)
	}

	// 创建 OTLP exporter
	ctx, cancel := context.WithTimeout(context.Background(), config.Timeout)
	defer cancel()

	exporter, err := otlptrace.New(
		ctx,
		otlptracegrpc.NewClient(
			otlptracegrpc.WithEndpoint(config.Endpoint),
			otlptracegrpc.WithInsecure(),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create exporter: %w", err)
	}

	// 创建 TracerProvider
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(config.SampleRate)),
	)

	// 设置全局 TracerProvider
	otel.SetTracerProvider(provider)

	// 创建 Tracer
	tracer := provider.Tracer(config.ServiceName)

	return &Tracer{
		config:   config,
		provider: provider,
		tracer:   tracer,
	}, nil
}

// Start 开始一个 span
func (t *Tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, name, opts...)
}

// StartSpan 开始一个命名 span（简化版）
func (t *Tracer) StartSpan(ctx context.Context, operationName string) (context.Context, trace.Span) {
	return t.tracer.Start(ctx, operationName)
}

// RecordError 记录错误
func (t *Tracer) RecordError(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
}

// SetAttributes 设置属性
func (t *Tracer) SetAttributes(span trace.Span, attrs ...attribute.KeyValue) {
	span.SetAttributes(attrs...)
}

// Close 关闭追踪器
func (t *Tracer) Close(ctx context.Context) error {
	if t.provider != nil {
		return t.provider.Shutdown(ctx)
	}
	return nil
}

// Span 辅助函数：自动结束的 span
func (t *Tracer) Span(ctx context.Context, name string, fn func(context.Context, trace.Span) error) error {
	ctx, span := t.Start(ctx, name)
	defer span.End()

	err := fn(ctx, span)
	if err != nil {
		t.RecordError(span, err)
	}

	return err
}

// TraceRequest 追踪 HTTP 请求
func TraceRequest(ctx context.Context, tracer *Tracer, method, path string, fn func() error) error {
	ctx, span := tracer.Start(ctx, fmt.Sprintf("%s %s", method, path))
	defer span.End()

	// 设置请求属性
	span.SetAttributes(
		attribute.String("http.method", method),
		attribute.String("http.path", path),
	)

	// 执行请求
	startTime := time.Now()
	err := fn()
	duration := time.Since(startTime)

	// 记录持续时间
	span.SetAttributes(
		attribute.Int64("http.duration_ms", duration.Milliseconds()),
	)

	// 记录错误
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// TraceFunction 追踪函数执行
func TraceFunction(ctx context.Context, tracer *Tracer, funcName string, fn func() error) error {
	ctx, span := tracer.Start(ctx, funcName)
	defer span.End()

	span.SetAttributes(
		attribute.String("function.name", funcName),
	)

	startTime := time.Now()
	err := fn()
	duration := time.Since(startTime)

	span.SetAttributes(
		attribute.Int64("function.duration_ms", duration.Milliseconds()),
	)

	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		span.SetStatus(codes.Ok, "")
	}

	return err
}

// ExtractTraceID 提取 Trace ID
func ExtractTraceID(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().HasTraceID() {
		return span.SpanContext().TraceID().String()
	}
	return ""
}

// ExtractSpanID 提取 Span ID
func ExtractSpanID(ctx context.Context) string {
	span := trace.SpanFromContext(ctx)
	if span.SpanContext().HasSpanID() {
		return span.SpanContext().SpanID().String()
	}
	return ""
}
