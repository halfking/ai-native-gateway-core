package dbdegradation

import (
	"encoding/json"
	"sync"
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
	Type      string                     `json:"type"`                 // "snapshot" | "rotation" | "request_log" | "request_wal"
	Timestamp time.Time                  `json:"timestamp"`            // 记录时间
	SessionID string                     `json:"session_id,omitempty"` // 会话ID
	Session   *session.Session           `json:"session,omitempty"`
	Stats     *session.SessionStats      `json:"stats,omitempty"`
	Rotation  *session.CredRotationEntry `json:"rotation,omitempty"`
	StopArgs  *session.SnapshotArgs      `json:"stop_args,omitempty"` // 停止参数
	RecordKey string                     `json:"record_key,omitempty"`
	Payload   json.RawMessage            `json:"payload,omitempty"`
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
	// SessionCountAvailable 报告「TotalSessions 这个口径**当前是否可用**」。
	//
	// ★ R82b 轮（2026-10-24）：154 生产实测发现 —— 备份记录**绝大多数是
	//	`request_wal` / `request_log`**，它们的顶层**没有 `session_id` 字段**
	//	（`session_id` 只在 `snapshot` 类记录上有，且 `omitempty` 会省略空值）
	//	⇒ `BackupRecord.SessionID` 恒为空 ⇒ 「跨文件唯一 session 数」恒为 1
	//	（= 唯一集合里只有那个**空串**）。
	//	⇒ ⇒ 「会话数 = 1」是**读数失真**，不是事实。
	//
	// ★ 本字段让这件事**可见**：false ⇒ 该读数不可用（端点会标 degraded）。
	//	（口径本身的修正 = **不把空串算成会话**，见 file_reader.go。）
	SessionCountAvailable bool `json:"session_count_available"`
}

// RecoveryTask 恢复任务
type RecoveryTask struct {
	mu               sync.RWMutex `json:"-"`
	ID               string       `json:"id"`
	Filename         string       `json:"filename"`
	Status           string       `json:"status"` // "pending"|"running"|"completed"|"completed_with_errors"|"failed"
	TotalRecords     int          `json:"total_records"`
	ProcessedRecords int          `json:"processed_records"`
	SuccessCount     int          `json:"success_count"`
	FailureCount     int          `json:"failure_count"`
	StartedAt        time.Time    `json:"started_at,omitempty"`
	CompletedAt      time.Time    `json:"completed_at,omitempty"`
	Error            string       `json:"error,omitempty"`
	Progress         float64      `json:"progress"` // 0-100
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
