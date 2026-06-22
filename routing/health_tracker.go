package routing

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/redis/go-redis/v9"
)

// HealthTracker wraps the credentialhealth package for executor integration.
type HealthTracker struct {
	recorder *credentialhealth.Recorder
	tuner    *credentialhealth.Tuner
	checker  *credentialhealth.Checker
}

// NewHealthTracker creates a health tracker for the executor.
func NewHealthTracker(
	redisClient *redis.Client,
	db credentialhealth.DBQuerier,
	windowTTL time.Duration,
	maxSize int,
) *HealthTracker {
	if redisClient == nil || db == nil {
		return nil // disabled
	}

	recorder := credentialhealth.NewRecorder(redisClient, windowTTL, maxSize)
	tuner := credentialhealth.NewTuner(db, credentialhealth.DefaultTunerConfig())
	checker := credentialhealth.NewChecker(recorder, db, credentialhealth.DefaultCheckerConfig())

	return &HealthTracker{
		recorder: recorder,
		tuner:    tuner,
		checker:  checker,
	}
}

// Enabled returns true if health tracking is enabled.
func (h *HealthTracker) Enabled() bool {
	return h != nil && h.recorder != nil && h.tuner != nil && h.checker != nil
}

// OnSuccess records a successful call and checks for auto-scaleup opportunity.
func (h *HealthTracker) OnSuccess(ctx context.Context, credentialID int, model string, latencyMs int, requestID string) {
	if !h.Enabled() {
		return
	}

	// Record success in sliding window (async, non-blocking)
	go func() {
		entry := credentialhealth.CallEntry{
			RequestID: requestID,
			Timestamp: time.Now().UnixMilli(),
			Success:   true,
			LatencyMs: latencyMs,
		}
		if err := h.recorder.Append(ctx, credentialID, model, entry); err != nil {
			slog.Warn("health_tracker: failed to record success",
				"credential_id", credentialID,
				"model", model,
				"error", err)
		}
	}()

	// Note: Auto-scaleup is handled by background worker, not per-request
}

// OnError records a failed call, triggers concurrency adjustment, and checks for continuous failure.
func (h *HealthTracker) OnError(ctx context.Context, credentialID int, model string, errKind errorsx.ErrorKind, requestID string) {
	if !h.Enabled() {
		return
	}

	// Record failure in sliding window (async, non-blocking)
	go func() {
		entry := credentialhealth.CallEntry{
			RequestID: requestID,
			Timestamp: time.Now().UnixMilli(),
			Success:   false,
			LatencyMs: 0,
			ErrorKind: string(errKind),
		}
		if err := h.recorder.Append(ctx, credentialID, model, entry); err != nil {
			slog.Warn("health_tracker: failed to record failure",
				"credential_id", credentialID,
				"model", model,
				"error", err)
		}
	}()

	// Trigger concurrency auto-tune (429/503 → decrease)
	go func() {
		if err := h.tuner.OnError(ctx, credentialID, model, errKind); err != nil {
			slog.Warn("health_tracker: tuner failed",
				"credential_id", credentialID,
				"model", model,
				"error_kind", errKind,
				"error", err)
		}
	}()

	// Check for continuous failure (80% over 1h → degraded)
	go func() {
		if err := h.checker.CheckAndUpdate(ctx, credentialID, model); err != nil {
			slog.Warn("health_tracker: checker failed",
				"credential_id", credentialID,
				"model", model,
				"error", err)
		}
	}()
}
