package executors

import (
	"context"
	"log/slog"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
)

// runOptionalCompressionStrategies runs the selector pipeline only when an
// operator explicitly enabled it for the matching dispatcher mode. It is a
// best-effort optimization: no reduction or an internal failure leaves the
// caller's established compression/recovery path untouched.
func (e *Executor) runOptionalCompressionStrategies(ctx context.Context, body []byte, contextWindow *int, mode compression.Mode) ([]byte, bool) {
	if e == nil || e.Compressor == nil || !e.Compressor.StrategyRunnerEnabled ||
		e.Compressor.Mode() != mode || contextWindow == nil || *contextWindow <= 0 {
		return body, false
	}
	if mode == compression.ModeAutoThreshold && !e.Compressor.ShouldCompressPreRequest(body, *contextWindow) {
		return body, false
	}

	out, stats, err := e.Compressor.RunCompressStrategies(ctx, body, *contextWindow)
	if err != nil {
		slog.Warn("compression strategy runner failed open",
			"mode", mode.String(), "error", err)
		return body, false
	}
	if len(out) >= len(body) {
		return body, false
	}

	slog.Info("compression strategy runner applied",
		"mode", mode.String(),
		"before_bytes", len(body),
		"after_bytes", len(out),
		"strategies", stats.AppliedNames,
		"failed_strategies", stats.FailedNames)
	return out, true
}
