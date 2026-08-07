package freequotacleanup

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Worker 配额历史清理后台任务
type Worker struct {
	db       *sql.DB
	interval time.Duration
}

// NewWorker 创建配额清理 Worker
func NewWorker(db *sql.DB, interval time.Duration) *Worker {
	if interval == 0 {
		interval = 24 * time.Hour // 默认每天执行一次
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

	log.Printf("[FreeQuotaCleanup] Worker started (interval: %v)", w.interval)

	// 启动时立即执行一次清理
	if err := w.cleanupOldWindows(ctx); err != nil {
		log.Printf("[FreeQuotaCleanup] Initial cleanup error: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("[FreeQuotaCleanup] Worker stopped")
			return
		case <-ticker.C:
			if err := w.cleanupOldWindows(ctx); err != nil {
				log.Printf("[FreeQuotaCleanup] Error: %v", err)
			}
		}
	}
}

// cleanupOldWindows 清理过期的配额追踪记录
func (w *Worker) cleanupOldWindows(ctx context.Context) error {
	result, err := w.db.ExecContext(ctx, `
        DELETE FROM free_quota_tracker
        WHERE (window_type = 'hour-5' AND window_end < now() - interval '48 hours')
           OR (window_type = 'day-1' AND window_end < now() - interval '30 days')
           OR (window_type = 'day-7' AND window_end < now() - interval '90 days')
           OR (window_type = 'month-1' AND window_end < now() - interval '12 months')
    `)

	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows > 0 {
		log.Printf("[FreeQuotaCleanup] Cleaned up %d old quota windows", rows)
	}

	return nil
}
