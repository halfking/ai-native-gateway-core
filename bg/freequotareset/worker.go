package freequotareset

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Worker 配额重置后台任务
type Worker struct {
	db       *sql.DB
	interval time.Duration
}

// NewWorker 创建配额重置 Worker
func NewWorker(db *sql.DB, interval time.Duration) *Worker {
	if interval == 0 {
		interval = 5 * time.Minute // 默认 5 分钟
	}
	return &Worker{
		db:       db,
		interval: interval,
	}
}

// Run 启动后台任务（阻塞）
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	log.Printf("[FreeQuotaReset] Worker started (interval: %v)", w.interval)

	for {
		select {
		case <-ctx.Done():
			log.Printf("[FreeQuotaReset] Worker stopped")
			return
		case <-ticker.C:
			if err := w.resetExpiredWindows(ctx); err != nil {
				log.Printf("[FreeQuotaReset] Error: %v", err)
			}
		}
	}
}

// resetExpiredWindows 重置已过期的耗尽状态
func (w *Worker) resetExpiredWindows(ctx context.Context) error {
	result, err := w.db.ExecContext(ctx, `
        UPDATE free_quota_tracker
        SET is_exhausted = FALSE,
            exhausted_at = NULL
        WHERE is_exhausted = TRUE
          AND auto_reset_at IS NOT NULL
          AND auto_reset_at <= now()
    `)

	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.Printf("[FreeQuotaReset] Reset %d expired quota windows", rows)
	}

	return nil
}
