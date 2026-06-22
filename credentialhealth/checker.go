package credentialhealth

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Checker detects continuous failures and marks credentials as degraded.
type Checker struct {
	recorder          *Recorder
	db                DBQuerier
	windowDuration    time.Duration // default 1 hour
	failureThreshold  float64       // default 0.80 (80%)
	minSampleSize     int           // default 5
	degradedCooldown  time.Duration // default 2 hours
	enableCheck       bool          // feature flag
}

// CheckerConfig holds checker configuration.
type CheckerConfig struct {
	WindowDuration   time.Duration
	FailureThreshold float64
	MinSampleSize    int
	DegradedCooldown time.Duration
	EnableCheck      bool
}

// DefaultCheckerConfig returns sensible defaults.
func DefaultCheckerConfig() CheckerConfig {
	return CheckerConfig{
		WindowDuration:   1 * time.Hour,
		FailureThreshold: 0.80, // 80% failure rate
		MinSampleSize:    5,
		DegradedCooldown: 2 * time.Hour,
		EnableCheck:      true,
	}
}

// NewChecker creates a continuous failure checker.
func NewChecker(recorder *Recorder, db DBQuerier, cfg CheckerConfig) *Checker {
	return &Checker{
		recorder:         recorder,
		db:               db,
		windowDuration:   cfg.WindowDuration,
		failureThreshold: cfg.FailureThreshold,
		minSampleSize:    cfg.MinSampleSize,
		degradedCooldown: cfg.DegradedCooldown,
		enableCheck:      cfg.EnableCheck,
	}
}

// Enabled returns true if checking is enabled.
func (c *Checker) Enabled() bool {
	return c != nil && c.recorder != nil && c.db != nil && c.enableCheck
}

// CheckAndUpdate analyzes recent call history and marks credential as degraded if needed.
func (c *Checker) CheckAndUpdate(ctx context.Context, credentialID int, model string) error {
	if !c.Enabled() {
		return nil
	}

	// Get recent entries within window
	since := time.Now().Add(-c.windowDuration)
	entries, err := c.recorder.GetRecent(ctx, credentialID, model, since)
	if err != nil {
		return fmt.Errorf("get recent entries: %w", err)
	}

	// Check sample size
	if len(entries) < c.minSampleSize {
		return nil // not enough data
	}

	// Compute stats (exclude network errors)
	var total, failed int
	errorKinds := make(map[string]int)

	for _, e := range entries {
		// Skip network errors (transient, not credential issue)
		if e.ErrorKind == "network" {
			continue
		}
		total++
		if !e.Success {
			failed++
			if e.ErrorKind != "" {
				errorKinds[e.ErrorKind]++
			}
		}
	}

	if total < c.minSampleSize {
		return nil // not enough non-network samples
	}

	failureRate := float64(failed) / float64(total)

	// Check threshold
	if failureRate < c.failureThreshold {
		return nil // below threshold, credential is healthy
	}

	// Mark as degraded
	return c.markDegraded(ctx, credentialID, model, failureRate, errorKinds, total)
}

// markDegraded updates the credential to degraded state.
func (c *Checker) markDegraded(ctx context.Context, credentialID int, model string, rate float64, kinds map[string]int, sampleSize int) error {
	recoverAt := time.Now().Add(c.degradedCooldown)
	reason := fmt.Sprintf("continuous_failure_%.1f%%_over_%s_samples=%d_errors=%v",
		rate*100, c.windowDuration, sampleSize, kinds)

	_, err := c.db.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = 'degraded',
		    availability_recover_at = $1,
		    state_reason_code       = 'continuous_failure',
		    state_reason_detail     = $2,
		    state_updated_at        = now()
		WHERE id = $3
		  AND availability_state NOT IN ('suspended', 'auth_failed')
	`, recoverAt, reason, credentialID)

	if err != nil {
		return fmt.Errorf("update credential state: %w", err)
	}

	slog.Warn("credential marked degraded due to continuous failures",
		"credential_id", credentialID,
		"model", model,
		"failure_rate", rate,
		"sample_size", sampleSize,
		"error_kinds", kinds,
		"recover_at", recoverAt,
		"window", c.windowDuration)

	return nil
}

// RecoverExpired checks for expired degraded credentials and restores them.
// This is called by the background health_auto_recover worker.
func RecoverExpired(ctx context.Context, db DBQuerier) (int, error) {
	result, err := db.Exec(ctx, `
		UPDATE credentials
		SET availability_state      = 'ready',
		    availability_recover_at = NULL,
		    state_reason_code       = NULL,
		    state_reason_detail     = NULL,
		    state_updated_at        = now()
		WHERE availability_recover_at IS NOT NULL
		  AND availability_recover_at < now()
		  AND availability_state IN ('cooling', 'rate_limited', 'unreachable', 'degraded')
	`)
	if err != nil {
		return 0, fmt.Errorf("recover expired credentials: %w", err)
	}

	// Extract rows affected
	rowsAffected := result.RowsAffected()

	if rowsAffected > 0 {
		slog.Info("auto recovered expired credentials",
			"count", rowsAffected)
	}

	return int(rowsAffected), nil
}
