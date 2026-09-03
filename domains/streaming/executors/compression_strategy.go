package executors

import (
	"context"
	"encoding/json"
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
	out, applied, _ := e.runCompressionStrategiesWithMeta(ctx, body, contextWindow, mode, force)
	return out, applied
}

func (e *Executor) runCompressionStrategiesWithMeta(ctx context.Context, body []byte, contextWindow *int, mode compression.Mode, force bool) ([]byte, bool, []byte) {
	if e == nil || e.Compressor == nil || !e.Compressor.StrategyRunnerEnabled ||
		e.Compressor.Mode() != mode || contextWindow == nil || *contextWindow <= 0 {
		return body, false, nil
	}
	if mode == compression.ModeAutoThreshold && !force && !e.Compressor.ShouldCompressPreRequest(body, *contextWindow) {
		return body, false, nil
	}

	var out []byte
	var stats strategy.RunStats
	var err error
	runnerMode := "sequential"
	if strings.EqualFold(strings.TrimSpace(e.Compressor.RunnerMode()), "parallel") {
		runnerMode = "parallel"
		out, stats, err = e.Compressor.RunCompressStrategiesParallel(ctx, body, *contextWindow)
	} else {
		out, stats, err = e.Compressor.RunCompressStrategies(ctx, body, *contextWindow)
	}
	if err != nil {
		slog.Warn("compression strategy runner failed open",
			"mode", mode.String(), "error", err)
		return body, false, marshalRunnerStats(runnerMode, mode, stats, "failed")
	}
	if len(out) >= len(body) {
		return body, false, marshalRunnerStats(runnerMode, mode, stats, "skipped")
	}

	slog.Info("compression strategy runner applied",
		"mode", mode.String(),
		"before_bytes", len(body),
		"after_bytes", len(out),
		"strategies", stats.AppliedNames,
		"failed_strategies", stats.FailedNames)
	return out, true, marshalRunnerStats(runnerMode, mode, stats, "applied")
}

func marshalRunnerStats(runnerMode string, mode compression.Mode, stats strategy.RunStats, outcome string) []byte {
	meta := map[string]any{
		"runner_mode":      runnerMode,
		"runner_mode_kind": mode.String(),
		"runner_outcome":   outcome,
		"candidate_count":  stats.CandidateCount,
		"winner":           stats.WinnerName,
		"applied":          append([]string(nil), stats.AppliedNames...),
		"skipped":          append([]string(nil), stats.SkippedNames...),
		"failed":           append([]string(nil), stats.FailedNames...),
		"truncated":        append([]string(nil), stats.TruncatedBy...),
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil
	}
	return encoded
}

// runOptionalCompressionStrategies preserves the existing non-forced call
// sites, including context-length recovery.
func (e *Executor) runOptionalCompressionStrategies(ctx context.Context, body []byte, contextWindow *int, mode compression.Mode) ([]byte, bool) {
	return e.runCompressionStrategies(ctx, body, contextWindow, mode, false)
}
