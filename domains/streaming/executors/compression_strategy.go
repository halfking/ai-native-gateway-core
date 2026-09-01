package executors

import (
	"context"
	"log/slog"
	"strings"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression"
	"github.com/kaixuan/llm-gateway-go/domains/hooks/compression/strategy"
)

func forceCompression(params *ExecParams) bool {
	if params == nil {
		return false
	}
	if params.ForceCompression {
		return true
	}
	return params.R != nil && strings.EqualFold(strings.TrimSpace(params.R.Header.Get("X-Gw-Force-Compression")), "true")
}

// runCompressionStrategies runs the selector pipeline only when an operator
// enabled it for the matching dispatcher mode. force bypasses only the auto
// threshold; it does not enable disabled policies or accept an expanding body.
func (e *Executor) runCompressionStrategies(ctx context.Context, body []byte, contextWindow *int, mode compression.Mode, force bool) ([]byte, bool) {
	if e == nil || e.Compressor == nil || !e.Compressor.StrategyRunnerEnabled ||
		e.Compressor.Mode() != mode || contextWindow == nil || *contextWindow <= 0 {
		return body, false
	}
	if mode == compression.ModeAutoThreshold && !force && !e.Compressor.ShouldCompressPreRequest(body, *contextWindow) {
		return body, false
	}

	var out []byte
	var stats strategy.RunStats
	var err error
	if strings.EqualFold(strings.TrimSpace(e.Compressor.RunnerMode()), "parallel") {
		out, stats, err = e.Compressor.RunCompressStrategiesParallel(ctx, body, *contextWindow)
	} else {
		out, stats, err = e.Compressor.RunCompressStrategies(ctx, body, *contextWindow)
	}
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

// runOptionalCompressionStrategies preserves the existing non-forced call
// sites, including context-length recovery.
func (e *Executor) runOptionalCompressionStrategies(ctx context.Context, body []byte, contextWindow *int, mode compression.Mode) ([]byte, bool) {
	return e.runCompressionStrategies(ctx, body, contextWindow, mode, false)
}
