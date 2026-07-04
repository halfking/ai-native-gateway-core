package credentialstate

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelPopularityTracker tracks model usage frequency from request_logs
// to enable heat-based probe scheduling (Phase 2 feature).
type ModelPopularityTracker struct {
	db            *pgxpool.Pool
	updateTicker  *time.Ticker
	stopCh        chan struct{}
	popularModels map[string]int
}

// NewModelPopularityTracker creates a popularity tracker.
func NewModelPopularityTracker(db *pgxpool.Pool) *ModelPopularityTracker {
	return &ModelPopularityTracker{
		db:            db,
		updateTicker:  time.NewTicker(5 * time.Minute),
		stopCh:        make(chan struct{}),
		popularModels: make(map[string]int),
	}
}

// Start begins the background refresh loop.
func (t *ModelPopularityTracker) Start(ctx context.Context) {
	go t.run(ctx)
}

// Stop halts the refresh loop.
func (t *ModelPopularityTracker) Stop() {
	close(t.stopCh)
	t.updateTicker.Stop()
}

func (t *ModelPopularityTracker) run(ctx context.Context) {
	if err := t.refresh(ctx); err != nil {
		slog.Warn("popularity tracker: initial refresh failed", "error", err)
	}

	for {
		select {
		case <-t.updateTicker.C:
			if err := t.refresh(ctx); err != nil {
				slog.Warn("popularity tracker: refresh failed", "error", err)
			}
		case <-t.stopCh:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (t *ModelPopularityTracker) refresh(ctx context.Context) error {
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// 2026-07-05 migration 341: 查询 request_logs_hot（1 小时窗口完全在热表范围内）
	rows, err := t.db.Query(queryCtx, `
		SELECT client_model, COUNT(*) AS request_count
		FROM request_logs_hot
		WHERE created_at > NOW() - INTERVAL '1 hour'
		  AND client_model IS NOT NULL
		  AND client_model != ''
		GROUP BY client_model
		ORDER BY request_count DESC
		LIMIT 100
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	newPopularity := make(map[string]int)
	for rows.Next() {
		var model string
		var count int
		if err := rows.Scan(&model, &count); err != nil {
			continue
		}
		newPopularity[model] = count
	}

	if err := rows.Err(); err != nil {
		return err
	}

	t.popularModels = newPopularity
	slog.Debug("popularity tracker: refreshed", "models_tracked", len(newPopularity))
	return nil
}

// GetProbeInterval returns the recommended probe interval based on heat.
func (t *ModelPopularityTracker) GetProbeInterval(model string) time.Duration {
	count, exists := t.popularModels[model]
	if !exists {
		return 5 * time.Minute
	}

	switch {
	case count >= 100:
		return 10 * time.Second
	case count >= 10:
		return 2 * time.Minute
	default:
		return 10 * time.Minute
	}
}

// GetPopularModels returns the top N most-requested models.
func (t *ModelPopularityTracker) GetPopularModels(topN int) []string {
	type modelCount struct {
		model string
		count int
	}

	models := make([]modelCount, 0, len(t.popularModels))
	for model, count := range t.popularModels {
		models = append(models, modelCount{model, count})
	}

	for i := 0; i < len(models); i++ {
		for j := i + 1; j < len(models); j++ {
			if models[j].count > models[i].count {
				models[i], models[j] = models[j], models[i]
			}
		}
	}

	result := make([]string, 0, topN)
	for i := 0; i < len(models) && i < topN; i++ {
		result = append(result, models[i].model)
	}

	return result
}
