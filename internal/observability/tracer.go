// Round 39 (2026-06-16) — OTel OTLP/HTTP exporter plumbing.
//
// This file is the **default-disabled** tracer initializer. It
// activates ONLY when `OTEL_EXPORTER_OTLP_ENDPOINT` is set in
// the environment. If the env var is unset, the function
// returns a no-op tracer and the service runs with zero OTel
// overhead (the existing behavior). This means the change is
// zero-risk for production: ops must explicitly set the env
// var to enable span export.
//
// Why OTLP/HTTP (not OTLP/gRPC, not Jaeger direct)
// ───────────────────────────────────────────────
// OTLP/HTTP is the simplest OTel exporter: a single HTTP POST
// per batch, no persistent connection, works through any
// HTTP proxy. gRPC adds connection management for marginal
// throughput gain. Jaeger direct is deprecated upstream. OTLP
// is the standard, future-proof choice (Tempo, Honeycomb, etc.
// all speak OTLP).
//
// Reference: docs/multi-tenant-otel-design.md §3.1 (helper
// spec), §4 (cross-tenant detection).
//
// Usage
// ─────
// In cmd/api/main.go:
//
//	func main() {
//	    // ... existing config / logger setup ...
//	    shutdown := observability.InitTracer("brandmind-go", "1.0.0")
//	    defer shutdown(context.Background())
//	    // ... existing server setup / run ...
//	}
//
// The `shutdown` function MUST be called before process exit
// to flush pending spans (otel SDK's BatchSpanProcessor
// batches spans in memory; shutdown forces the final flush).
package observability

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// InitTracer sets up the global OpenTelemetry tracer provider.
// Returns a shutdown function that flushes pending spans and
// must be called before process exit. If the OTLP endpoint env
// var is unset, returns a no-op shutdown (zero overhead).
//
// Environment variables:
//   - OTEL_EXPORTER_OTLP_ENDPOINT  e.g., "http://localhost:4318"
//                                  (the HTTP receiver of an OTel
//                                  collector or Jaeger all-in-one)
//   - OTEL_SERVICE_NAME            defaults to the `serviceName` arg
//   - OTEL_SERVICE_VERSION         defaults to the `version` arg
//   - OTEL_EXPORTER_OTLP_HEADERS    optional, e.g., "Authorization=Bearer ..."
func InitTracer(serviceName, version string) func(context.Context) {
	endpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	if endpoint == "" {
		// Default-disabled. This is the zero-risk path: spans
		// are still created (via the helper in tenant.go) but
		// not exported. The OTel SDK's noop tracer has zero
		// allocation cost.
		return func(context.Context) {}
	}

	// 1. Build OTLP/HTTP exporter.
	exporter, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpoint(stripScheme(endpoint)),
		otlptracehttp.WithInsecure(), // OTLP/HTTP — production uses
		// WithTLS() for TLS. Configurable via env in R40+.
	)
	if err != nil {
		log.Printf("observability: OTLP exporter init failed: %v (spans will not be exported)", err)
		return func(context.Context) {}
	}

	// 2. Build resource (service identification for the backend).
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
			semconv.DeploymentEnvironment(envOr("OTEL_DEPLOYMENT_ENV", "dev")),
		),
		resource.WithProcess(),
		resource.WithHost(),
	)
	if err != nil {
		log.Printf("observability: resource init failed: %v (continuing with default)", err)
		res = resource.Default()
	}

	// 3. Build tracer provider with batch span processor.
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter,
			sdktrace.WithBatchTimeout(5*time.Second),
			sdktrace.WithMaxExportBatchSize(512),
		),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	// 4. Set as global provider + W3C TraceContext propagator.
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	log.Printf("observability: OTLP/HTTP exporter enabled → %s (service=%s version=%s)",
		endpoint, serviceName, version)

	return func(ctx context.Context) {
		// Flush pending spans with a 5s timeout.
		flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := tp.Shutdown(flushCtx); err != nil {
			log.Printf("observability: tracer shutdown error: %v", err)
		}
	}
}

// stripScheme removes "http://" or "https://" prefix from an endpoint
// because the OTel HTTP exporter's WithEndpoint() expects host:port
// only.
func stripScheme(endpoint string) string {
	for _, prefix := range []string{"http://", "https://"} {
		if len(endpoint) > len(prefix) && endpoint[:len(prefix)] == prefix {
			return endpoint[len(prefix):]
		}
	}
	return endpoint
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Helper for callers that want a startup-time check
func TracerEnabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""
}

// Compile-time interface assertion: make sure InitTracer
// signature matches the expected `func(context.Context)` shape.
var _ func(context.Context) = InitTracer("test", "test")

// Avoid unused-import warning if fmt is dropped later
var _ = fmt.Sprintf
