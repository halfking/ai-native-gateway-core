package credentialhealth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kaixuan/llm-gateway-go/errorsx"
)

// DBQuerier is the subset of pgxpool.Pool that Tuner needs.
type DBQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row
	Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error)
}

// Tuner dynamically adjusts credential concurrency limits based on error patterns.
type Tuner struct {
	pg                DBQuerier
	decreaseFactor503 float64 // e.g., 0.90 = reduce by 10%
	minConcurrency    int     // minimum limit (default 1)
	maxConcurrency    int     // maximum limit (default 50)
	enableAutoTune    bool    // feature flag
}

// TunerConfig holds tuner configuration.
type TunerConfig struct {
	DecreaseFactor503 float64
	MinConcurrency    int
	MaxConcurrency    int
	EnableAutoTune    bool
}

// DefaultTunerConfig returns sensible defaults.
func DefaultTunerConfig() TunerConfig {
	return TunerConfig{
		DecreaseFactor503: 0.90, // 503 → reduce by 10%
		MinConcurrency:    1,
		MaxConcurrency:    50,
		EnableAutoTune:    true,
	}
}

// NewTuner creates a concurrency tuner.
func NewTuner(pg DBQuerier, cfg TunerConfig) *Tuner {
	return &Tuner{
		pg:                pg,
		decreaseFactor503: cfg.DecreaseFactor503,
		minConcurrency:    cfg.MinConcurrency,
		maxConcurrency:    cfg.MaxConcurrency,
		enableAutoTune:    cfg.EnableAutoTune,
	}
}

// Enabled returns true if auto-tune is enabled and PG is available.
func (t *Tuner) Enabled() bool {
	return t != nil && t.pg != nil && t.enableAutoTune
}

// OnError is called after each failed LLM call to potentially adjust concurrency.
//
// P2-7 fix (2026-06-22 audit): KindRateLimit (429) is intentionally NOT
// handled here. The executor already calls e.Limiter.Shrink on 429, which
// is a fast in-memory backoff (recovers in seconds). Having the tuner also
// cut concurrency_limit_auto by 20% (a slow DB-backed change that only
// recovers via the hourly scaleup worker) double-penalized 429s: a single
// rate-limit spike could collapse the effective limit to the floor and take
// an hour to climb back — directly prolonging the "No available provider"
// state the health system was built to fix. 429 is a provider-side throttle,
// not evidence that OUR concurrency ceiling is too high.
//
// KindConcurrent (503 overload) IS handled here because it is literally the
// upstream telling us we're sending too much concurrent traffic, which is
// exactly what concurrency_limit_auto governs.
func (t *Tuner) OnError(ctx context.Context, credentialID int, model string, errKind errorsx.ErrorKind) error {
	if !t.Enabled() {
		return nil
	}

	switch errKind {
	case errorsx.KindConcurrent: // 503 engine busy — genuinely too much concurrency
		return t.decreaseConcurrency(ctx, credentialID, model, t.decreaseFactor503, "concurrent_503")

	case errorsx.KindRateLimit:
		// See method comment: handled by Limiter.Shrink in the executor, not here.
		return nil

	case errorsx.KindQuotaPeriodic, errorsx.KindQuotaPermanent:
		// Quota exhaustion doesn't mean concurrency is too high
		return nil

	default:
		return nil
	}
}

// decreaseConcurrency reduces the concurrency_limit_auto by the given factor.
func (t *Tuner) decreaseConcurrency(ctx context.Context, credentialID int, model string, factor float64, reason string) error {
	var currentLimit int
	err := t.pg.QueryRow(ctx, `
		SELECT COALESCE(concurrency_limit_auto, concurrency_limit, 5)
		FROM credentials
		WHERE id = $1
	`, credentialID).Scan(&currentLimit)
	if err != nil {
		return fmt.Errorf("query current limit: %w", err)
	}

	newLimit := int(float64(currentLimit) * factor)
	if newLimit < t.minConcurrency {
		newLimit = t.minConcurrency
	}

	if newLimit >= currentLimit {
		// No change needed (already at minimum or factor >= 1.0)
		return nil
	}

	_, err = t.pg.Exec(ctx, `
		UPDATE credentials
		SET concurrency_limit_auto = $1,
		    state_updated_at = now()
		WHERE id = $2
	`, newLimit, credentialID)

	if err != nil {
		return fmt.Errorf("update concurrency limit: %w", err)
	}

	slog.Warn("auto decreased concurrency limit",
		"credential_id", credentialID,
		"model", model,
		"old_limit", currentLimit,
		"new_limit", newLimit,
		"factor", factor,
		"reason", reason)

	return nil
}

// IncreaseConcurrency increases the limit by 1 (called by background worker).
func (t *Tuner) IncreaseConcurrency(ctx context.Context, credentialID int, model string) error {
	if !t.Enabled() {
		return nil
	}

	var currentLimit int
	err := t.pg.QueryRow(ctx, `
		SELECT COALESCE(concurrency_limit_auto, concurrency_limit, 5)
		FROM credentials
		WHERE id = $1
	`, credentialID).Scan(&currentLimit)
	if err != nil {
		return fmt.Errorf("query current limit: %w", err)
	}

	newLimit := currentLimit + 1
	if newLimit > t.maxConcurrency {
		newLimit = t.maxConcurrency
	}

	if newLimit <= currentLimit {
		// Already at max
		return nil
	}

	_, err = t.pg.Exec(ctx, `
		UPDATE credentials
		SET concurrency_limit_auto = $1,
		    state_updated_at = now()
		WHERE id = $2
	`, newLimit, credentialID)

	if err != nil {
		return fmt.Errorf("update concurrency limit: %w", err)
	}

	slog.Info("auto increased concurrency limit",
		"credential_id", credentialID,
		"model", model,
		"old_limit", currentLimit,
		"new_limit", newLimit)

	return nil
}

// GetEffectiveLimit returns the effective concurrency limit for a credential.
// Priority: concurrency_limit (manual) > concurrency_limit_auto > default 5
func (t *Tuner) GetEffectiveLimit(ctx context.Context, credentialID int) (int, error) {
	var manual, auto *int
	err := t.pg.QueryRow(ctx, `
		SELECT concurrency_limit, concurrency_limit_auto
		FROM credentials
		WHERE id = $1
	`, credentialID).Scan(&manual, &auto)
	if err != nil {
		return 5, fmt.Errorf("query limits: %w", err)
	}

	if manual != nil {
		return *manual, nil
	}
	if auto != nil {
		return *auto, nil
	}
	return 5, nil
}
