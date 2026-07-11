package credentialstate

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BatchWriter 批量写入器 - 减少数据库写入压力
type BatchWriter struct {
	db        *pgxpool.Pool
	buffer    []StateUpdate
	bufferMu  sync.Mutex
	interval  time.Duration
	batchSize int
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewBatchWriter 创建批量写入器
func NewBatchWriter(db *pgxpool.Pool, interval time.Duration, batchSize int) *BatchWriter {
	return &BatchWriter{
		db:        db,
		buffer:    make([]StateUpdate, 0, batchSize),
		interval:  interval,
		batchSize: batchSize,
		done:      make(chan struct{}),
	}
}

// Start 启动批量写入器
func (bw *BatchWriter) Start(ctx context.Context) {
	ctx, bw.cancel = context.WithCancel(ctx)
	go bw.run(ctx)
	slog.Info("batch writer started",
		"interval", bw.interval,
		"batch_size", bw.batchSize)
}

// Stop 停止批量写入器
func (bw *BatchWriter) Stop() {
	if bw.cancel != nil {
		bw.cancel()
	}
	<-bw.done
}

// Add 添加状态更新到缓冲区
func (bw *BatchWriter) Add(update StateUpdate) {
	bw.bufferMu.Lock()
	defer bw.bufferMu.Unlock()

	bw.buffer = append(bw.buffer, update)

	// 缓冲区满时立即刷新
	if len(bw.buffer) >= bw.batchSize {
		go bw.flush()
	}
}

func (bw *BatchWriter) run(ctx context.Context) {
	defer close(bw.done)

	ticker := time.NewTicker(bw.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// 退出前刷新剩余数据
			bw.flush()
			return
		case <-ticker.C:
			bw.flush()
		}
	}
}

func (bw *BatchWriter) flush() {
	bw.bufferMu.Lock()
	if len(bw.buffer) == 0 {
		bw.bufferMu.Unlock()
		return
	}

	// 复制缓冲区并清空
	updates := make([]StateUpdate, len(bw.buffer))
	copy(updates, bw.buffer)
	bw.buffer = bw.buffer[:0]
	bw.bufferMu.Unlock()

	// 批量写入数据库
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 使用 ON CONFLICT 更新已存在的记录
	// 注意：这里我们将状态更新写入一个专门的日志表，而不是直接修改主表
	// 主表的更新由探测器负责，这里只记录实时指标
	for _, update := range updates {
		_, err := bw.db.Exec(ctx, `
			INSERT INTO credential_state_log 
				(credential_id, raw_model_name, available, health_status, 
				 latency_ms, last_success_at, last_failure_at, last_error, 
				 recover_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (credential_id, raw_model_name) DO UPDATE SET
				available = COALESCE(EXCLUDED.available, credential_state_log.available),
				health_status = COALESCE(EXCLUDED.health_status, credential_state_log.health_status),
				latency_ms = COALESCE(EXCLUDED.latency_ms, credential_state_log.latency_ms),
				last_success_at = COALESCE(EXCLUDED.last_success_at, credential_state_log.last_success_at),
				last_failure_at = COALESCE(EXCLUDED.last_failure_at, credential_state_log.last_failure_at),
				recover_at = CASE WHEN $11 THEN NULL ELSE COALESCE(EXCLUDED.recover_at, credential_state_log.recover_at) END,
				last_error = CASE WHEN $11 THEN NULL ELSE COALESCE(EXCLUDED.last_error, credential_state_log.last_error) END,
				updated_at = EXCLUDED.updated_at
		`,
			update.CredentialID,
			update.Model,
			update.Available,
			update.HealthStatus,
			update.LatencyMs,
			update.LastSuccessAt,
			update.LastFailureAt,
			update.LastError,
			update.RecoverAt,
			update.UpdatedAt,
			update.ClearRecovery,
		)

		if err != nil {
			slog.Warn("batch writer: write failed",
				"credential_id", update.CredentialID,
				"model", update.Model,
				"error", err)
		}
	}

	slog.Debug("batch writer: flushed",
		"count", len(updates))
}
