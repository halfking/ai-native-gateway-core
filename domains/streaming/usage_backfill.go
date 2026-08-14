package streaming

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/hooks/observability/telemetry"
)

// UsageCorrector is the telemetry surface the estimated-usage backfill needs
// (CO-2, 2026-08-15). Satisfied by *telemetry.Client.
type UsageCorrector interface {
	CorrectEstimatedUsage(ctx context.Context, entry *telemetry.RequestLogEntry) (int64, error)
}

// backfillEstimatedUsage asynchronously corrects a request_logs row that
// still carries usage_source='estimated' with the real usage in entry, and —
// when a row actually transitions — backfills the request's
// response_format_anomalies rows with the real completion tokens.
//
// Best-effort by design: failures only log, never block the request path,
// and repeated calls are idempotent (the SQL only touches estimated rows /
// anomaly rows without actual_tokens). Known limitation: if the earlier
// estimated write is still queued in the telemetry worker when the
// correction runs, the correction matches 0 rows and is dropped — the
// M3 reconciliation job (CO-3) is the safety net for that window.
// usage_ledger_hot is intentionally not rewritten here; ledger/billing
// reconciliation is out of CO-2 scope.
func backfillEstimatedUsage(corrector UsageCorrector, anomalies *FormatAnomalyRecorder, entry *telemetry.RequestLogEntry) {
	if !usageBackfillEligible(entry) {
		return
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("usage backfill panicked",
					"request_id", entry.RequestID, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		runUsageBackfill(ctx, corrector, anomalies, entry)
	}()
}

func usageBackfillEligible(entry *telemetry.RequestLogEntry) bool {
	return entry != nil && entry.RequestID != "" &&
		(entry.PromptTokens != nil || entry.CompletionTokens != nil)
}

// runUsageBackfill is the synchronous core of the backfill, kept separate so
// tests can drive it deterministically.
func runUsageBackfill(ctx context.Context, corrector UsageCorrector, anomalies *FormatAnomalyRecorder, entry *telemetry.RequestLogEntry) {
	if !usageBackfillEligible(entry) {
		return
	}
	if corrector == nil {
		return
	}
	rows, err := corrector.CorrectEstimatedUsage(ctx, entry)
	if err != nil {
		slog.Warn("usage backfill: request_logs correction failed",
			"request_id", entry.RequestID, "error", err)
		return
	}
	if rows == 0 {
		// Row never carried estimated usage (or was already corrected) —
		// keep its authoritative values untouched.
		return
	}
	if anomalies == nil || entry.CompletionTokens == nil {
		return
	}
	if err := anomalies.BackfillActualTokens(ctx, entry.RequestID, *entry.CompletionTokens); err != nil {
		slog.Warn("usage backfill: format_anomalies correction failed",
			"request_id", entry.RequestID, "error", err)
	}
}
