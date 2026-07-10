package vibecoding

import "time"

// ProjectStatus 项目状态
type ProjectStatus string

const (
	ProjectStatusActive   ProjectStatus = "active"
	ProjectStatusArchived ProjectStatus = "archived"
	ProjectStatusDeleted  ProjectStatus = "deleted"
)

// Project VibeCoding项目
type Project struct {
	ID          int64                  `json:"id"`
	TenantID    string                 `json:"tenant_id"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Language    string                 `json:"language"`
	Framework   string                 `json:"framework"`
	Status      ProjectStatus          `json:"status"`
	Settings    map[string]interface{} `json:"settings"`
	CreatedBy   string                 `json:"created_by"`
	CreatedAt   time.Time              `json:"created_at"`
	UpdatedAt   time.Time              `json:"updated_at"`
}

// SessionStatus 会话状态
type SessionStatus string

const (
	SessionStatusActive    SessionStatus = "active"
	SessionStatusCompleted SessionStatus = "completed"
	SessionStatusFailed    SessionStatus = "failed"
	SessionStatusCancelled SessionStatus = "cancelled"
)

// Session AI编程会话
type Session struct {
	ID          int64                  `json:"id"`
	ProjectID   *int64                 `json:"project_id,omitempty"`
	TenantID    string                 `json:"tenant_id"`
	SessionID   string                 `json:"session_id"`
	TaskType    string                 `json:"task_type"`
	Status      SessionStatus          `json:"status"`
	Messages    []Message              `json:"messages"`
	Metadata    map[string]interface{} `json:"metadata"`
	CreatedAt   time.Time              `json:"created_at"`
	CompletedAt *time.Time             `json:"completed_at,omitempty"`
}

// Message 会话消息
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
}

// Review 代码审查记录
type Review struct {
	ID           int64                  `json:"id"`
	SessionID    *int64                 `json:"session_id,omitempty"`
	TenantID     string                 `json:"tenant_id"`
	FilePath     string                 `json:"file_path"`
	Language     string                 `json:"language"`
	OriginalCode string                 `json:"original_code"`
	ReviewResult map[string]interface{} `json:"review_result"`
	Score        float64                `json:"score"`
	CreatedAt    time.Time              `json:"created_at"`
}

// ReviewResult 审查结果结构
type ReviewResult struct {
	Issues          []ReviewIssue `json:"issues"`
	Suggestions     []string      `json:"suggestions"`
	Summary         string        `json:"summary"`
	Complexity      int           `json:"complexity"`
	Maintainability string        `json:"maintainability"`
}

// ReviewIssue 审查问题
type ReviewIssue struct {
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	Category string `json:"category"`
}
