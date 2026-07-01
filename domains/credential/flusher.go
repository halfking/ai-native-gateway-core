package credential

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BanditFlusher periodically flushes Bandit state updates to the database.
// It batches updates to reduce database write pressure.
type BanditFlusher struct {
	db     *pgxpool.Pool
	bandit *BanditScorer
	ticker *time.Ticker
	stop   chan struct{}
	wg     sync.WaitGroup
	mu     sync.Mutex
	dirty  map[string]bool // credentialID (string) -> needs flush

	flushInterval time.Duration
	batchSize     int
}

// NewBanditFlusher creates a new async batch writer for Bandit state.
// flushInterval: how often to flush (e.g., 10s)
// batchSize: flush early if dirty set reaches this count (e.g., 100)
func NewBanditFlusher(db *pgxpool.Pool, bandit *BanditScorer, flushInterval time.Duration, batchSize int) *BanditFlusher {
	return &BanditFlusher{
		db:            db,
		bandit:        bandit,
		flushInterval: flushInterval,
		batchSize:     batchSize,
		stop:          make(chan struct{}),
		dirty:         make(map[string]bool),
	}
}

// Start begins the background flush loop.
func (f *BanditFlusher) Start() {
	f.ticker = time.NewTicker(f.flushInterval)
	f.wg.Add(1)
	go f.flushLoop()
}

// Stop gracefully stops the flusher and performs a final flush.
func (f *BanditFlusher) Stop() {
	close(f.stop)
	f.wg.Wait()
	if f.ticker != nil {
		f.ticker.Stop()
	}
	// Final flush
	f.Flush()
}

// flushLoop runs in background and flushes periodically or when batch size reached.
func (f *BanditFlusher) flushLoop() {
	defer f.wg.Done()
	for {
		select {
		case <-f.ticker.C:
			f.Flush()
		case <-f.stop:
			return
		}
	}
}

// Flush writes all pending updates to database.
func (f *BanditFlusher) Flush() {
	f.mu.Lock()
	if len(f.dirty) == 0 {
		f.mu.Unlock()
		return
	}

	// Snapshot dirty set and clear it
	dirtyCredIDs := make([]string, 0, len(f.dirty))
	for credID := range f.dirty {
		dirtyCredIDs = append(dirtyCredIDs, credID)
	}
	f.dirty = make(map[string]bool)
	f.mu.Unlock()

	// Batch write to database
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := f.db.Begin(ctx)
	if err != nil {
		slog.Error("bandit flush: begin tx failed", "error", err)
		// Restore dirty set
		f.mu.Lock()
		for _, credID := range dirtyCredIDs {
			f.dirty[credID] = true
		}
		f.mu.Unlock()
		return
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // Go standard pattern for commit-then-rollback

	successCount := 0
	failCount := 0

	for _, credIDStr := range dirtyCredIDs {
		credID, err := strconv.Atoi(credIDStr)
		if err != nil {
			slog.Error("bandit flush: invalid credID", "credID", credIDStr, "error", err)
			failCount++
			continue
		}

		// Get current score from bandit
		score := f.bandit.GetScore(credIDStr)

		// Calculate average latency
		var avgLatency int64
		if score.SuccessRequests > 0 {
			avgLatency = score.TotalLatencyMs / score.SuccessRequests
		}

		// Calculate 429 penalty with decay
		penalty := score.RateLimitPenalty
		if !score.LastRateLimitHit.IsZero() {
			hoursSince := time.Since(score.LastRateLimitHit).Hours()
			penalty *= math.Exp(-hoursSince / 24.0) // 24h half-life
		}

		_, err = tx.Exec(ctx, `
			UPDATE api_keys 
			SET 
				bandit_alpha = $1,
				bandit_beta = $2,
				bandit_success_count = $3,
				bandit_failure_count = $4,
				bandit_429_count = $5,
				bandit_total_latency_ms = $6,
				bandit_avg_latency_ms = $7,
				penalty_429_accumulated = $8,
				penalty_429_last_at = $9,
				last_scored_at = $10
			WHERE id = $11
		`,
			score.Alpha,
			score.Beta,
			score.SuccessRequests,
			score.FailureRequests,
			score.RateLimitHits,
			score.TotalLatencyMs,
			avgLatency,
			penalty,
			nullTime(score.LastRateLimitHit),
			time.Now(),
			credID,
		)
		if err != nil {
			slog.Error("bandit flush: update failed", "credentialID", credID, "error", err)
			failCount++
			continue
		}
		successCount++
	}

	if err := tx.Commit(ctx); err != nil {
		slog.Error("bandit flush: commit failed", "error", err)
		// Restore dirty set
		f.mu.Lock()
		for _, credID := range dirtyCredIDs {
			f.dirty[credID] = true
		}
		f.mu.Unlock()
		return
	}

	slog.Info("bandit flush completed",
		"total", len(dirtyCredIDs),
		"success", successCount,
		"failed", failCount)
}

// nullTime returns nil for zero time, otherwise returns the time pointer
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

// MarkDirty marks a credential's Bandit state as needing flush.
// This should be called after RecordSuccess/RecordFailure/Record429.
func (f *BanditFlusher) MarkDirty(credentialID string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.dirty[credentialID] = true

	// Flush early if batch size reached
	if len(f.dirty) >= f.batchSize {
		go f.Flush()
	}
}
