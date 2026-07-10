package dbdegradation

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// Recovery 数据恢复管理器
type Recovery struct {
	db         *pgxpool.Pool
	fileReader *FileReader
	batchSize  int
	tasks      sync.Map // map[string]*RecoveryTask
}

// NewRecovery 创建恢复管理器
func NewRecovery(db *pgxpool.Pool, fileReader *FileReader, batchSize int) *Recovery {
	if batchSize <= 0 {
		batchSize = 100
	}
	return &Recovery{
		db:         db,
		fileReader: fileReader,
		batchSize:  batchSize,
	}
}

// RecoverFile 恢复单个文件
func (r *Recovery) RecoverFile(ctx context.Context, filename string, deleteAfter bool) (string, error) {
	taskID := uuid.New().String()
	task := &RecoveryTask{
		ID:       taskID,
		Filename: filename,
		Status:   "pending",
	}
	r.tasks.Store(taskID, task)

	// 使用带超时的 context（30 分钟）
	taskCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	
	// 异步执行恢复
	go func() {
		defer cancel()
		r.executeRecovery(taskCtx, task, deleteAfter)
	}()

	return taskID, nil
}

// RecoverAll 恢复所有文件
func (r *Recovery) RecoverAll(ctx context.Context, deleteAfter bool) (string, error) {
	files, err := r.fileReader.ListBackupFiles(ctx)
	if err != nil {
		return "", fmt.Errorf("list backup files: %w", err)
	}

	if len(files) == 0 {
		return "", fmt.Errorf("no backup files found")
	}

	taskID := uuid.New().String()
	task := &RecoveryTask{
		ID:       taskID,
		Filename: fmt.Sprintf("all (%d files)", len(files)),
		Status:   "pending",
	}
	r.tasks.Store(taskID, task)

	// 异步执行批量恢复
	go r.executeRecoveryAll(context.Background(), task, files, deleteAfter)

	return taskID, nil
}

// GetTaskStatus 获取任务状态
func (r *Recovery) GetTaskStatus(taskID string) (*RecoveryTask, bool) {
	value, ok := r.tasks.Load(taskID)
	if !ok {
		return nil, false
	}
	task := value.(*RecoveryTask)
	return task, true
}

// executeRecovery 执行单个文件的恢复
func (r *Recovery) executeRecovery(ctx context.Context, task *RecoveryTask, deleteAfter bool) {
	task.Status = "running"
	task.StartedAt = time.Now()

	// 读取所有记录
	var records []BackupRecord
	err := r.fileReader.ReadRecords(ctx, task.Filename, func(record BackupRecord) error {
		records = append(records, record)
		return nil
	})

	if err != nil {
		task.Status = "failed"
		task.Error = fmt.Sprintf("read records: %v", err)
		task.CompletedAt = time.Now()
		slog.Error("recovery: failed to read records", "task_id", task.ID, "error", err)
		return
	}

	task.TotalRecords = len(records)

	// 按会话分组
	sessionRecords := r.groupBySession(records)

	// 批量恢复
	for sessionID, recs := range sessionRecords {
		if err := r.recoverSession(ctx, sessionID, recs); err != nil {
			task.FailureCount++
			slog.Warn("recovery: failed to recover session",
				"task_id", task.ID,
				"session_id", sessionID,
				"error", err,
			)
		} else {
			task.SuccessCount++
		}
		task.ProcessedRecords += len(recs)
		task.Progress = float64(task.ProcessedRecords) / float64(task.TotalRecords) * 100
	}

	// 完成
	task.CompletedAt = time.Now()
	if task.FailureCount == 0 {
		task.Status = "completed"
		slog.Info("recovery: completed successfully",
			"task_id", task.ID,
			"filename", task.Filename,
			"sessions", task.SuccessCount,
			"records", task.ProcessedRecords,
		)

		// 删除或归档文件
		if deleteAfter {
			if err := r.archiveFile(task.Filename); err != nil {
				slog.Warn("recovery: failed to archive file", "filename", task.Filename, "error", err)
			}
		}
		} else {
			task.Status = "completed_with_errors"
			task.Error = fmt.Sprintf("recovered %d sessions, %d failed", task.SuccessCount, task.FailureCount)
			slog.Warn("recovery: completed with errors",
				"task_id", task.ID,
				"success", task.SuccessCount,
				"failure", task.FailureCount,
			)
		}
}

// executeRecoveryAll 执行所有文件的恢复
func (r *Recovery) executeRecoveryAll(ctx context.Context, task *RecoveryTask, files []BackupFile, deleteAfter bool) {
	task.Status = "running"
	task.StartedAt = time.Now()

	for _, file := range files {
		// 为每个文件创建子任务
		subTask := &RecoveryTask{
			ID:       task.ID + "-" + file.Filename,
			Filename: file.Filename,
			Status:   "running",
		}

		r.executeRecovery(ctx, subTask, deleteAfter)

		// 汇总到主任务
		task.ProcessedRecords += subTask.ProcessedRecords
		task.SuccessCount += subTask.SuccessCount
		task.FailureCount += subTask.FailureCount
	}

	task.CompletedAt = time.Now()
	if task.FailureCount == 0 {
		task.Status = "completed"
	} else {
		task.Status = "completed_with_errors"
		task.Error = fmt.Sprintf("recovered files with %d failures", task.FailureCount)
	}
}

// groupBySession 按会话分组记录
func (r *Recovery) groupBySession(records []BackupRecord) map[string][]BackupRecord {
	grouped := make(map[string][]BackupRecord)
	for _, record := range records {
		grouped[record.SessionID] = append(grouped[record.SessionID], record)
	}
	return grouped
}

// recoverSession 恢复单个会话的所有记录
func (r *Recovery) recoverSession(ctx context.Context, sessionID string, records []BackupRecord) error {
	// 查找快照记录
	var snapshot *BackupRecord
	var rotations []BackupRecord

	for i := range records {
		if records[i].Type == "snapshot" {
			snapshot = &records[i]
		} else if records[i].Type == "rotation" {
			rotations = append(rotations, records[i])
		}
	}

	if snapshot == nil {
		return fmt.Errorf("no snapshot found for session %s", sessionID)
	}

	// 设置事务超时（30 秒）
	txCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 开始事务
	tx, err := r.db.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(txCtx)

	// 写入会话快照
	if err := r.writeSnapshot(txCtx, tx, snapshot); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}

	// 写入凭据轮换记录
	if err := r.writeRotations(txCtx, tx, sessionID, rotations); err != nil {
		return fmt.Errorf("write rotations: %w", err)
	}

	// 提交事务
	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// writeSnapshot 写入会话快照
func (r *Recovery) writeSnapshot(ctx context.Context, tx pgx.Tx, snap *BackupRecord) error {
	sess := snap.Session
	stats := snap.Stats
	
	var costUSD float64
	var firstReq, lastReq any
	var totalTurns, promptTokens, completionTokens int64
	
	if stats != nil {
		costUSD = stats.TotalCostUSD
		totalTurns = stats.TotalTurns
		promptTokens = stats.TotalPromptTokens
		completionTokens = stats.TotalCompletionTokens
		if !stats.FirstRequestAt.IsZero() {
			firstReq = stats.FirstRequestAt
		}
		if !stats.LastRequestAt.IsZero() {
			lastReq = stats.LastRequestAt
		}
	}
	
	var stoppedAt any
	stopReason := ""
	if snap.StopArgs != nil {
		if !snap.StopArgs.StoppedAt.IsZero() {
			stoppedAt = snap.StopArgs.StoppedAt
		}
		stopReason = snap.StopArgs.StopReason
	}
	
	durationSec := 0
	if stats != nil && !stats.FirstRequestAt.IsZero() && !stats.LastRequestAt.IsZero() {
		durationSec = int(stats.LastRequestAt.Sub(stats.FirstRequestAt).Seconds())
	}
	
	// 序列化完整快照
	raw, err := session.MarshalSnapshot(sess, stats)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO session_state_snapshots (
			session_id, tenant_id, api_key_id, task_id,
			status, created_at, first_request_at, last_request_at,
			stopped_at, stop_reason,
			total_turns, total_duration_sec,
			total_prompt_tokens, total_completion_tokens, total_cost_usd,
			title, annotation, raw_snapshot
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18::jsonb)
		ON CONFLICT (session_id) DO UPDATE SET
			status = EXCLUDED.status,
			last_request_at = EXCLUDED.last_request_at,
			stopped_at = EXCLUDED.stopped_at,
			stop_reason = EXCLUDED.stop_reason,
			total_turns = EXCLUDED.total_turns,
			total_duration_sec = EXCLUDED.total_duration_sec,
			total_prompt_tokens = EXCLUDED.total_prompt_tokens,
			total_completion_tokens = EXCLUDED.total_completion_tokens,
			total_cost_usd = EXCLUDED.total_cost_usd,
			title = EXCLUDED.title,
			annotation = EXCLUDED.annotation,
			raw_snapshot = EXCLUDED.raw_snapshot`,
		sess.SessionID, sess.TenantID, sess.APIKeyID, sess.TaskID,
		defaultString(sess.Status, "active"), sess.CreatedAt, firstReq, lastReq,
		stoppedAt, stopReason,
		totalTurns, durationSec,
		promptTokens, completionTokens, costUSD,
		sess.Title, sess.Annotation, raw,
	)

	return err
}

// writeRotations 写入凭据轮换记录
func (r *Recovery) writeRotations(ctx context.Context, tx pgx.Tx, sessionID string, records []BackupRecord) error {
	if len(records) == 0 {
		return nil
	}

	// 获取当前最大序列号
	var maxSeq int
	if err := tx.QueryRow(ctx, 
		`SELECT COALESCE(MAX(seq), 0) FROM session_credential_rotations WHERE session_id = $1`, 
		sessionID,
	).Scan(&maxSeq); err != nil {
		return fmt.Errorf("query max seq: %w", err)
	}

	// 写入轮换记录
	for i, record := range records {
		if record.Rotation == nil {
			continue
		}
		
		rotation := record.Rotation
		seq := maxSeq + i + 1
		costUSD := float64(rotation.CostUSDCents) / 10000.0
		
		var endedAt any
		if rotation.EndedAt != nil {
			endedAt = *rotation.EndedAt
		}
		
		durationSec := 0
		if rotation.EndedAt != nil {
			durationSec = int(rotation.EndedAt.Sub(rotation.StartedAt).Seconds())
		}
		
		_, err := tx.Exec(ctx, `
			INSERT INTO session_credential_rotations (
				session_id, tenant_id, seq,
				credential_id, model, provider,
				started_at, ended_at, turns, duration_sec,
				prompt_tokens, completion_tokens, cost_usd,
				switch_reason, fp_slot_index
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (session_id, seq) DO NOTHING`,
			sessionID, "default", seq,
			rotation.CredentialID, rotation.Model, rotation.Provider,
			rotation.StartedAt, endedAt, rotation.Turns, durationSec,
			rotation.PromptTokens, rotation.CompletionTokens, costUSD,
			rotation.SwitchReason, rotation.FPSlotIndex,
		)
		
		if err != nil {
			return fmt.Errorf("insert rotation %d: %w", seq, err)
		}
	}

	return nil
}

// archiveFile 归档或删除文件
func (r *Recovery) archiveFile(filename string) error {
	backupDir := filepath.Join(r.fileReader.baseDir, "backups")
	path := filepath.Join(backupDir, filename)

	// 创建归档目录
	archiveDir := filepath.Join(r.fileReader.baseDir, "archive")
	if err := os.MkdirAll(archiveDir, 0755); err != nil {
		return fmt.Errorf("create archive dir: %w", err)
	}

	// 移动文件到归档目录
	archivePath := filepath.Join(archiveDir, filename)
	if err := os.Rename(path, archivePath); err != nil {
		return fmt.Errorf("move to archive: %w", err)
	}

	slog.Info("recovery: file archived", "filename", filename, "archive_path", archivePath)
	return nil
}

func defaultString(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
