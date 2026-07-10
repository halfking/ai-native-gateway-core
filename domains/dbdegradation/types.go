package dbdegradation

import (
	"time"

	"github.com/kaixuan/llm-gateway-go/domains/session"
)

// DBStatus 表示数据库状态
type DBStatus int

const (
	DBStatusUnknown DBStatus = iota
	DBStatusAvailable
	DBStatusDegraded
)

func (s DBStatus) String() string {
	switch s {
	case DBStatusAvailable:
		return "available"
	case DBStatusDegraded:
		return "degraded"
	default:
		return "unknown"
	}
}

// StatusChangeEvent 状态变更事件
type StatusChangeEvent struct {
	OldStatus DBStatus
	NewStatus DBStatus
	Timestamp time.Time
	Message   string
}

// StatusChangeListener 状态变更监听器
type StatusChangeListener func(event StatusChangeEvent)

// BackupRecord 备份记录（写入文件的格式）
type BackupRecord struct {
	Type      string                     `json:"type"`       // "snapshot" | "rotation"
	Timestamp time.Time                  `json:"timestamp"`  // 记录时间
	SessionID string                     `json:"session_id"` // 会话ID
	Session   *session.Session           `json:"session,omitempty"`
	Stats     *session.SessionStats      `json:"stats,omitempty"`
	Rotation  *session.CredRotationEntry `json:"rotation,omitempty"`
	StopArgs  *session.SnapshotArgs      `json:"stop_args,omitempty"` // 停止参数
}

// BackupFile 备份文件信息
type BackupFile struct {
	Filename     string    `json:"filename"`
	Path         string    `json:"path"`
	Date         string    `json:"date"`
	Size         int64     `json:"size"`          // 压缩后大小
	RecordCount  int       `json:"record_count"`  // 总记录数
	SessionCount int       `json:"session_count"` // 会话数
	CreatedAt    time.Time `json:"created_at"`
	ModifiedAt   time.Time `json:"modified_at"`
}

// BackupSummary 备份汇总信息
type BackupSummary struct {
	TotalFiles    int          `json:"total_files"`
	TotalRecords  int          `json:"total_records"`
	TotalSessions int          `json:"total_sessions"`
	TotalSize     int64        `json:"total_size"` // 压缩后总大小
	DateRange     string       `json:"date_range"`
	Files         []BackupFile `json:"files"`
}

// RecoveryTask 恢复任务
type RecoveryTask struct {
	ID               string    `json:"id"`
	Filename         string    `json:"filename"`
	Status           string    `json:"status"` // "pending"|"running"|"completed"|"failed"
	TotalRecords     int       `json:"total_records"`
	ProcessedRecords int       `json:"processed_records"`
	SuccessCount     int       `json:"success_count"`
	FailureCount     int       `json:"failure_count"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	Error            string    `json:"error,omitempty"`
	Progress         float64   `json:"progress"` // 0-100
}

// Stats 文件写入统计
type Stats struct {
	TotalRecords     int64     `json:"total_records"`
	TotalBytes       int64     `json:"total_bytes"`       // 未压缩大小
	CompressedBytes  int64     `json:"compressed_bytes"`  // 压缩后大小
	CurrentFileSize  int64     `json:"current_file_size"` // 当前文件大小（压缩后）
	LastWriteTime    time.Time `json:"last_write_time"`
	CompressionRatio float64   `json:"compression_ratio"` // 压缩率
}
