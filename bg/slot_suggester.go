package bg

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SlotSuggester analyzes weekly peaks and suggests concurrency limit
// adjustments. It only suggests increases (never decreases) and applies
// them with a 24h preview period.
type SlotSuggester struct {
	db     *pgxpool.Pool
	cancel context.CancelFunc
	done   chan struct{}
}

// NewSlotSuggester creates a new suggester.
func NewSlotSuggester(db *pgxpool.Pool) *SlotSuggester {
	return &SlotSuggester{db: db, done: make(chan struct{})}
}

// Start spawns the background cron-style goroutine.
func (s *SlotSuggester) Start(ctx context.Context) {
	cctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	go s.run(cctx)
	slog.Info("slot suggester started", "schedule", "02:00 UTC daily")
}

// Stop terminates the goroutine and waits for it.
func (s *SlotSuggester) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	<-s.done
}

func (s *SlotSuggester) run(ctx context.Context) {
	defer close(s.done)
	for {
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day(), 2, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Until(next)):
			s.suggest(ctx)
		}
	}
}

// suggest scans the last 7 days of weekly peaks and writes suggestions
// to the credential_model_weekly_peak table.
func (s *SlotSuggester) suggest(ctx context.Context) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	rows, err := s.db.Query(timeoutCtx, `
		SELECT
			wp.credential_id,
			wp.raw_model,
			wp.peak_concurrent_5min,
			wp.peak_concurrent,
			wp.p95_concurrent,
			wp.current_limit,
			wp.week_start
		FROM credential_model_weekly_peak wp
		WHERE wp.week_start >= NOW() - INTERVAL '7 days'
		  AND wp.peak_concurrent_5min > wp.current_limit * 0.8
		  AND wp.current_limit > 0
		  AND wp.sample_days >= 3
		ORDER BY wp.peak_concurrent_5min DESC
		LIMIT 100
	`)
	if err != nil {
		slog.Error("slot suggester query failed", "error", err)
		return
	}
	defer rows.Close()

	var suggestCount int
	for rows.Next() {
		var credID int64
		var rawModel string
		var peak5min, peak int
		var p95, currentLimit float64 // current_limit is numeric in DB; scan as float to be safe
		var weekStart time.Time

		if err := rows.Scan(&credID, &rawModel, &peak5min, &peak, &p95, &currentLimit, &weekStart); err != nil {
			slog.Error("slot suggester scan failed", "error", err)
			continue
		}
		// current_limit stored as int; truncate float to int
		current := int(currentLimit)
		suggested := s.calculateSuggestedLimit(peak5min, int(p95), current)
		if suggested <= current {
			continue
		}

		// Skip if there's already a similar pending suggestion in the
		// last 14 days.
		var existing int
		err := s.db.QueryRow(timeoutCtx, `
			SELECT COALESCE(MAX(suggested_limit), 0)
			FROM credential_model_weekly_peak
			WHERE credential_id = $1
			  AND raw_model     = $2
			  AND week_start   >= NOW() - INTERVAL '14 days'
		`, credID, rawModel).Scan(&existing)
		if err == nil && existing >= suggested {
			continue
		}

		reason := fmt.Sprintf(
			"peak_5min=%d, peak_1m=%d, p95=%.0f, current_limit=%d, suggested=%d",
			peak5min, peak, p95, current, suggested,
		)
		_, err = s.db.Exec(timeoutCtx, `
			UPDATE credential_model_weekly_peak
			SET suggested_limit = $1,
			    suggestion_reason = $2,
			    updated_at = NOW()
			WHERE credential_id = $3
			  AND raw_model     = $4
			  AND week_start   >= NOW() - INTERVAL '14 days'
		`, suggested, reason, credID, rawModel)
		if err != nil {
			slog.Error("slot suggester update failed", "error", err)
			continue
		}
		_, _ = s.db.Exec(timeoutCtx, `
			INSERT INTO auto_tune_audit (
				credential_id, raw_model, action,
				old_limit, new_limit, reason,
				peak_concurrent, p95_concurrent, week_start, applied_by
			) VALUES ($1, $2, 'suggest', $3, $4, $5, $6, $7, $8, 'auto')
		`, credID, rawModel, current, suggested, reason, peak5min, p95, weekStart)
		suggestCount++

		slog.Info("slot suggestion generated",
			"credential_id", credID, "model", rawModel,
			"current_limit", current, "suggested", suggested,
			"peak_5min", peak5min, "peak_1m", peak, "p95", p95,
		)
	}
	slog.Info("slot suggester completed", "candidates", suggestCount)
}

// calculateSuggestedLimit returns the new limit per the documented rules.
// - Only suggest increases (never decreases).
// - Take max(peak * 1.2, current * 1.5) as the floor.
// - Cap at 500 (global ceiling).
// - Minimum +1 above current.
func (s *SlotSuggester) calculateSuggestedLimit(peak, p95, current int) int {
	base := float64(peak)
	if float64(p95) > base {
		base = float64(p95)
	}
	withHeadroom := base * 1.2
	minIncrease := float64(current) * 1.5
	suggested := math.Max(withHeadroom, minIncrease)
	suggestedInt := int(math.Ceil(suggested))
	if suggestedInt > 500 {
		suggestedInt = 500
	}
	if suggestedInt <= current {
		suggestedInt = current + 1
	}
	return suggestedInt
}

// ApplyDueSuggestions applies any suggestion that has been in preview
// for at least 24 hours and has not already been applied. Returns the
// number of suggestions actually applied.
func (s *SlotSuggester) ApplyDueSuggestions(ctx context.Context) (int, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	rows, err := s.db.Query(timeoutCtx, `
		SELECT DISTINCT
			wp.credential_id, wp.raw_model,
			wp.suggested_limit, wp.current_limit,
			wp.peak_concurrent_5min, wp.p95_concurrent, wp.week_start
		FROM credential_model_weekly_peak wp
		WHERE wp.suggested_limit IS NOT NULL
		  AND wp.suggested_limit > wp.current_limit
		  AND wp.updated_at < NOW() - INTERVAL '24 hours'
		  AND NOT EXISTS (
		    SELECT 1 FROM auto_tune_audit a
		    WHERE a.credential_id = wp.credential_id
		      AND a.raw_model     = wp.raw_model
		      AND a.action        = 'apply'
		      AND a.created_at   > wp.updated_at
		  )
		LIMIT 50
	`)
	if err != nil {
		return 0, fmt.Errorf("query due suggestions: %w", err)
	}
	defer rows.Close()

	applied := 0
	for rows.Next() {
		var credID int64
		var rawModel string
		var suggested, current, peak int
		var p95 float64
		var weekStart time.Time
		if err := rows.Scan(&credID, &rawModel, &suggested, &current, &peak, &p95, &weekStart); err != nil {
			slog.Error("apply suggestion scan failed", "error", err)
			continue
		}
		// Only update if current credentials.concurrency_limit is still
		// lower than the suggestion. (Avoid clobbering concurrent manual
		// changes.)  routing_policy is the global singleton — it has
		// no credential_id and no concurrency_limit column.  The
		// per-credential cap lives in credentials.concurrency_limit.
		tag, err := s.db.Exec(timeoutCtx, `
			UPDATE credentials
			SET concurrency_limit = $1, updated_at = NOW()
			WHERE id = $2
			  AND (concurrency_limit IS NULL OR concurrency_limit < $1)
		`, suggested, credID)
		if err != nil {
			slog.Error("apply suggestion update failed", "error", err, "credential_id", credID)
			continue
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		reason := fmt.Sprintf("auto-applied after 24h preview: peak_5min=%d, p95=%.0f, %d -> %d",
			peak, p95, current, suggested)
		_, _ = s.db.Exec(timeoutCtx, `
			INSERT INTO auto_tune_audit (
				credential_id, raw_model, action,
				old_limit, new_limit, reason,
				peak_concurrent, p95_concurrent, week_start, applied_by
			) VALUES ($1, $2, 'apply', $3, $4, $5, $6, $7, $8, 'auto')
		`, credID, rawModel, current, suggested, reason, peak, p95, weekStart)
		applied++
		slog.Info("auto-tune applied",
			"credential_id", credID, "model", rawModel,
			"old_limit", current, "new_limit", suggested,
		)
	}
	return applied, nil
}
