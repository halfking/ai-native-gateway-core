package routing

import (
	"context"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/credentialhealth"
	"github.com/kaixuan/llm-gateway-go/errorsx"
	"github.com/kaixuan/llm-gateway-go/provider"
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
	// Wire the candidate-cache invalidator so a continuous-failure flip
	// of (cred, model) is visible to the next request — without this, the
	// 30s availableModelsCache would still serve the just-degraded
	// binding until TTL expiry. (2026-06-22 audit, Fix C1.)
	checkerCfg := credentialhealth.DefaultCheckerConfig()
	checkerCfg.InvalidateCandidateCache = provider.InvalidateAllCandidateCache
	checker := credentialhealth.NewChecker(recorder, db, checkerCfg)

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
//
// NOTE on context: callers pass params.R.Context() so the signature is
// uniform with the request lifecycle, but the actual I/O happens in a
// detached goroutine. The request context is canceled the moment the
// handler returns (well before this goroutine runs its Redis pipeline),
// so we must NOT use the caller's ctx inside the goroutine — doing so
// would surface every Append as a "context canceled" error and the
// sliding window would record nothing. We detach with
// context.WithoutCancel (Go 1.21+) plus a bounded timeout so a slow
// Redis can't leak goroutines indefinitely.
func (h *HealthTracker) OnSuccess(ctx context.Context, credentialID int, model string, latencyMs int, requestID string) {
	if !h.Enabled() {
		return
	}

	// Record success in sliding window (async, non-blocking).
	go func() {
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		entry := credentialhealth.CallEntry{
			RequestID: requestID,
			Timestamp: time.Now().UnixMilli(),
			Success:   true,
			LatencyMs: latencyMs,
		}
		if err := h.recorder.Append(bgCtx, credentialID, model, entry); err != nil {
			slog.Warn("health_tracker: failed to record success",
				"credential_id", credentialID,
				"model", model,
				"error", err)
		}
	}()

	// Note: Auto-scaleup is handled by background worker, not per-request
}

// OnError records a failed call, triggers concurrency adjustment, and checks for continuous failure.
//
// The three actions (record / tune / check) run in a SINGLE goroutine and
// in that fixed order, rather than three parallel goroutines. Rationale:
//  1. Parallel goroutines let the checker race the tuner — the checker
//     could mark the credential degraded based on the window BEFORE the
//     tuner lowered the limit, producing a double penalty.
//  2. Every failed candidate spawns one of these, and under a sync-retry
//     storm (120s loop × N candidates) three goroutines per failure
//     floods PG. One goroutine keeps the fan-out bounded.
//
// See P0-1 / P2-6 in the 2026-06-22 audit for details.
//
// The context is detached from the caller's request context for the same
// reason as OnSuccess — the request is already over when this runs.
func (h *HealthTracker) OnError(ctx context.Context, credentialID int, model string, errKind errorsx.ErrorKind, requestID string) {
	if !h.Enabled() {
		return
	}

	go func() {
		// Bound the whole record→tune→check chain so a slow PG can't
		// hold a goroutine open indefinitely; 10s is ample for two
		// queries + one write.
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()

		// 1. Record failure in sliding window first so the tuner/checker
		//    below observe the freshest data.
		entry := credentialhealth.CallEntry{
			RequestID: requestID,
			Timestamp: time.Now().UnixMilli(),
			Success:   false,
			LatencyMs: 0,
			ErrorKind: string(errKind),
		}
		if err := h.recorder.Append(bgCtx, credentialID, model, entry); err != nil {
			slog.Warn("health_tracker: failed to record failure",
				"credential_id", credentialID,
				"model", model,
				"error", err)
			// Don't return: tuner/checker can still run against the
			// previously-recorded window.
		}

		// 2. Trigger concurrency auto-tune (429/503 → decrease).
		if err := h.tuner.OnError(bgCtx, credentialID, model, errKind); err != nil {
			slog.Warn("health_tracker: tuner failed",
				"credential_id", credentialID,
				"model", model,
				"error_kind", errKind,
				"error", err)
		}

		// 3. Check for continuous failure (80% over 1h → degraded).
		if err := h.checker.CheckAndUpdate(bgCtx, credentialID, model); err != nil {
			slog.Warn("health_tracker: checker failed",
				"credential_id", credentialID,
				"model", model,
				"error", err)
		}
	}()
}
