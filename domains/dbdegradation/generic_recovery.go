package dbdegradation

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// GenericRecovery replays generic request/WAL records through a caller-provided writer.
type GenericRecovery struct {
	reader *FileReader
	tasks  sync.Map
	write  func(context.Context, BackupRecord) error
}

func NewGenericRecovery(reader *FileReader, write func(context.Context, BackupRecord) error) *GenericRecovery {
	return &GenericRecovery{reader: reader, write: write}
}

func (r *GenericRecovery) Recover(ctx context.Context, filename string, archive bool) (string, error) {
	if r == nil || r.reader == nil || r.write == nil {
		return "", fmt.Errorf("generic recovery not configured")
	}
	if err := validateBackupFilename(filename); err != nil {
		return "", err
	}
	task := &RecoveryTask{ID: uuid.New().String(), Filename: filename, Status: "pending"}
	r.tasks.Store(task.ID, task)
	go r.execute(ctx, task, archive)
	return task.ID, nil
}

// Status 返回任务状态快照。
//
// 2026-07-27 concurrency fix: 之前直接返回活指针，admin 侧 JSON 编码时
// execute() 仍在后台写同一批字段 → 读写竞争。改为持 task.mu 逐字段拷贝，
// 与 Recovery.GetTaskStatus 保持一致，活指针不再逃逸。
func (r *GenericRecovery) Status(id string) (*RecoveryTask, bool) {
	v, ok := r.tasks.Load(id)
	if !ok {
		return nil, false
	}
	task := v.(*RecoveryTask)

	task.mu.RLock()
	defer task.mu.RUnlock()

	return &RecoveryTask{
		ID:               task.ID,
		Filename:         task.Filename,
		Status:           task.Status,
		TotalRecords:     task.TotalRecords,
		ProcessedRecords: task.ProcessedRecords,
		SuccessCount:     task.SuccessCount,
		FailureCount:     task.FailureCount,
		StartedAt:        task.StartedAt,
		CompletedAt:      task.CompletedAt,
		Error:            task.Error,
		Progress:         task.Progress,
	}, true
}

// execute 在后台 goroutine 上跑恢复。
//
// 2026-07-27 concurrency fix: 所有 task 字段写入都持 task.mu（RecoveryTask
// 本来就带 mu，这里之前完全没用），避免与 Status() 的 JSON 编码竞争。
// I/O（GetFileSummary / ReadRecords / write / ArchiveFile）一律在锁外执行。
func (r *GenericRecovery) execute(ctx context.Context, task *RecoveryTask, archive bool) {
	task.mu.Lock()
	task.Status = "running"
	task.StartedAt = time.Now().UTC()
	task.mu.Unlock()

	file, err := r.reader.GetFileSummary(ctx, task.Filename)
	if err != nil {
		task.mu.Lock()
		task.Status, task.Error, task.CompletedAt = "failed", err.Error(), time.Now().UTC()
		task.mu.Unlock()
		return
	}

	task.mu.Lock()
	task.TotalRecords = file.RecordCount
	task.mu.Unlock()

	sawLegacy := false
	err = r.reader.ReadRecords(ctx, task.Filename, func(record BackupRecord) error {
		if record.Type != "request_log" && record.Type != "request_wal" {
			sawLegacy = true
			task.mu.Lock()
			task.ProcessedRecords++
			task.mu.Unlock()
			return nil
		}
		writeErr := r.write(ctx, record)
		task.mu.Lock()
		if writeErr != nil {
			task.FailureCount++
		} else {
			task.SuccessCount++
		}
		task.ProcessedRecords++
		if task.TotalRecords > 0 {
			task.Progress = float64(task.ProcessedRecords) / float64(task.TotalRecords) * 100
		}
		task.mu.Unlock()
		if writeErr != nil {
			slog.Warn("generic recovery record failed", "task_id", task.ID, "key", record.RecordKey, "error", writeErr)
		}
		return nil
	})

	// 先在锁内判定终态，需要归档时把 I/O 放到锁外再回写结果。
	task.mu.Lock()
	needArchive := false
	if err != nil {
		task.Status, task.Error = "failed", err.Error()
	} else if task.FailureCount > 0 {
		task.Status = "completed_with_errors"
		task.Error = fmt.Sprintf("recovered %d records, %d failed", task.SuccessCount, task.FailureCount)
	} else if sawLegacy {
		task.Status = "completed_with_errors"
		task.Error = "session records remain in the file; use session recovery before archiving"
	} else {
		task.Status = "completed"
		task.Progress = 100
		needArchive = archive
	}
	task.mu.Unlock()

	if needArchive {
		if archiveErr := r.reader.ArchiveFile(task.Filename); archiveErr != nil {
			task.mu.Lock()
			task.Status, task.Error = "completed_with_errors", archiveErr.Error()
			task.mu.Unlock()
		}
	}

	task.mu.Lock()
	task.CompletedAt = time.Now().UTC()
	task.mu.Unlock()
}
