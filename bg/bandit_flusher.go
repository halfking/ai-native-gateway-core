// Package bg provides background workers for the gateway.
package bg

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/credential"
)

// BanditFlusher periodically flushes Bandit scorer state to the database.
// This batches writes to reduce database load while keeping historical data
// for Thompson Sampling relatively fresh.
type BanditFlusher struct {
	Scorer       *credential.BanditScorer
	DB           *sql.DB
	FlushInterval time.Duration
	cancel       context.CancelFunc
}

// NewBanditFlusher creates a new Bandit state flusher.
func NewBanditFlusher(scorer *credential.BanditScorer, db *sql.DB, flushInterval time.Duration) *BanditFlusher {
	if flushInterval <= 0 {
		flushInterval = 10 * time.Second // Default to 10 seconds
	}
	return &BanditFlusher{
		Scorer:        scorer,
		DB:            db,
		FlushInterval: flushInterval,
	}
}

// Start begins the background flush loop.
// Call Stop() to gracefully shut down.
func (f *BanditFlusher) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	f.cancel = cancel

	go f.flushLoop(ctx)
	slog.Info("bandit flusher started",
		"flush_interval", f.FlushInterval,
	)
}

// Stop gracefully stops the flusher.
func (f *BanditFlusher) Stop() {
	if f.cancel != nil {
		f.cancel()
	}
}

func (f *BanditFlusher) flushLoop(ctx context.Context) {
	ticker := time.NewTicker(f.FlushInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Final flush before exit
			if err := f.Flush(context.Background()); err != nil {
				slog.Error("bandit flusher: final flush failed", "error", err)
			}
			slog.Info("bandit flusher stopped")
			return
		case <-ticker.C:
			if err := f.Flush(ctx); err != nil {
				slog.Error("bandit flusher: periodic flush failed", "error", err)
			}
		}
	}
}

// Flush writes all Bandit state to the database in a single transaction.
func (f *BanditFlusher) Flush(ctx context.Context) error {
	if f.Scorer == nil || f.DB == nil {
		return nil
	}

	scores := f.Scorer.GetAllScores()
	if len(scores) == 0 {
		return nil
	}

	start := time.Now()
	tx, err := f.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Prepare update statement
	stmt, err := tx.PrepareContext(ctx, `
		UPDATE credentials
		SET bandit_alpha = $1,
		    bandit_beta = $2,
		    total_requests = $3,
		    success_requests = $4,
		    failure_requests = $5,
		    total_latency_ms = $6,
		    rate_limit_hits = $7,
		    last_rate_limit_hit = $8,
		    rate_limit_penalty = $9,
		    quota_remaining = $10,
		    quota_total = $11,
		    last_quota_update = $12,
		    last_scored_at = $13
		WHERE id = $14
	`)
	if err != nil {
		return fmt.Errorf("prepare statement: %w", err)
	}
	defer stmt.Close()

	updated := 0
	for credIDStr, score := range scores {
		var credID int
		if _, err := fmt.Sscanf(credIDStr, "%d", &credID); err != nil {
			slog.Warn("bandit flusher: invalid credential ID",
				"cred_id", credIDStr,
				"error", err,
			)
			continue
		}

		var lastRateLimitHit *time.Time
		if !score.LastRateLimitHit.IsZero() {
			lastRateLimitHit = &score.LastRateLimitHit
		}

		var lastQuotaUpdate *time.Time
		if !score.LastQuotaUpdate.IsZero() {
			lastQuotaUpdate = &score.LastQuotaUpdate
		}

		var lastScored *time.Time
		if !score.LastScored.IsZero() {
			lastScored = &score.LastScored
		}

		result, err := stmt.ExecContext(ctx,
			score.Alpha,
			score.Beta,
			score.TotalRequests,
			score.SuccessRequests,
			score.FailureRequests,
			score.TotalLatencyMs,
			score.RateLimitHits,
			lastRateLimitHit,
			score.RateLimitPenalty,
			score.QuotaRemaining,
			score.QuotaTotal,
			lastQuotaUpdate,
			lastScored,
			credID,
		)
		if err != nil {
			slog.Warn("bandit flusher: update failed",
				"cred_id", credID,
				"error", err,
			)
			continue
		}

		rowsAffected, _ := result.RowsAffected()
		if rowsAffected > 0 {
			updated++
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	elapsed := time.Since(start)
	slog.Debug("bandit flusher: batch update completed",
		"updated", updated,
		"total", len(scores),
		"elapsed_ms", elapsed.Milliseconds(),
	)

	return nil
}

// FlushCredential flushes a single credential's state immediately.
// This is useful for critical updates that can't wait for the next batch.
func (f *BanditFlusher) FlushCredential(ctx context.Context, credentialID int) error {
	if f.Scorer == nil || f.DB == nil {
		return nil
	}

	credIDStr := fmt.Sprintf("%d", credentialID)
	score := f.Scorer.GetScore(credIDStr)

	var lastRateLimitHit *time.Time
	if !score.LastRateLimitHit.IsZero() {
		lastRateLimitHit = &score.LastRateLimitHit
	}

	var lastQuotaUpdate *time.Time
	if !score.LastQuotaUpdate.IsZero() {
		lastQuotaUpdate = &score.LastQuotaUpdate
	}

	var lastScored *time.Time
	if !score.LastScored.IsZero() {
		lastScored = &score.LastScored
	}

	_, err := f.DB.ExecContext(ctx, `
		UPDATE credentials
		SET bandit_alpha = $1,
		    bandit_beta = $2,
		    total_requests = $3,
		    success_requests = $4,
		    failure_requests = $5,
		    total_latency_ms = $6,
		    rate_limit_hits = $7,
		    last_rate_limit_hit = $8,
		    rate_limit_penalty = $9,
		    quota_remaining = $10,
		    quota_total = $11,
		    last_quota_update = $12,
		    last_scored_at = $13
		WHERE id = $14
	`,
		score.Alpha,
		score.Beta,
		score.TotalRequests,
		score.SuccessRequests,
		score.FailureRequests,
		score.TotalLatencyMs,
		score.RateLimitHits,
		lastRateLimitHit,
		score.RateLimitPenalty,
		score.QuotaRemaining,
		score.QuotaTotal,
		lastQuotaUpdate,
		lastScored,
		credentialID,
	)

	return err
}
