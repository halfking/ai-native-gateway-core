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

func (r *GenericRecovery) Status(id string) (*RecoveryTask, bool) {
	v, ok := r.tasks.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*RecoveryTask), true
}

func (r *GenericRecovery) execute(ctx context.Context, task *RecoveryTask, archive bool) {
	task.Status = "running"
	task.StartedAt = time.Now().UTC()
	file, err := r.reader.GetFileSummary(ctx, task.Filename)
	if err != nil {
		task.Status, task.Error, task.CompletedAt = "failed", err.Error(), time.Now().UTC()
		return
	}
	task.TotalRecords = file.RecordCount
	sawLegacy := false
	err = r.reader.ReadRecords(ctx, task.Filename, func(record BackupRecord) error {
		if record.Type != "request_log" && record.Type != "request_wal" {
			sawLegacy = true
			task.ProcessedRecords++
			return nil
		}
		if err := r.write(ctx, record); err != nil {
			task.FailureCount++
			slog.Warn("generic recovery record failed", "task_id", task.ID, "key", record.RecordKey, "error", err)
		} else {
			task.SuccessCount++
		}
		task.ProcessedRecords++
		if task.TotalRecords > 0 {
			task.Progress = float64(task.ProcessedRecords) / float64(task.TotalRecords) * 100
		}
		return nil
	})
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
		if archive {
			if err := r.reader.ArchiveFile(task.Filename); err != nil {
				task.Status, task.Error = "completed_with_errors", err.Error()
			}
		}
	}
	task.CompletedAt = time.Now().UTC()
}
