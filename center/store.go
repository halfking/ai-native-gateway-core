package center

import (
	"context"
	"time"
)

// Store 中心运维存储接口
type Store interface {
	// Instance Registry
	RegisterInstance(ctx context.Context, instance *InstanceInfo) error
	GetInstance(ctx context.Context, instanceID string) (*InstanceInfo, error)
	ListInstances(ctx context.Context, status string, offset, limit int) ([]InstanceInfo, int, error)
	UpdateInstanceStatus(ctx context.Context, instanceID, status string) error
	DeleteInstance(ctx context.Context, instanceID string) error

	// Heartbeat
	RecordHeartbeat(ctx context.Context, instanceID string, payload *HeartbeatPayload) error
	GetLastHeartbeat(ctx context.Context, instanceID string) (time.Time, error)
	GetHeartbeatHistory(ctx context.Context, instanceID string, since time.Time, limit int) ([]HeartbeatRecord, error)

	// Commands
	CreateCommand(ctx context.Context, cmd *Command) error
	GetCommand(ctx context.Context, commandID string) (*Command, error)
	ListPendingCommands(ctx context.Context, instanceID string) ([]Command, error)
	UpdateCommandStatus(ctx context.Context, commandID, status string, result *CommandResult) error
	GetCommandHistory(ctx context.Context, instanceID string, limit int) ([]Command, error)

	// Status Reports
	RecordStatusReport(ctx context.Context, instanceID string, payload *StatusReportPayload) error
	GetLatestStatus(ctx context.Context, instanceID string) (*StatusReportPayload, error)
}

// HeartbeatRecord 心跳记录
type HeartbeatRecord struct {
	InstanceID   string    `json:"instance_id"`
	Timestamp    time.Time `json:"timestamp"`
	UptimeSecs   int64     `json:"uptime_secs"`
	NumGoroutine int       `json:"num_goroutine"`
	AllocMB      float64   `json:"alloc_mb"`
	Status       string    `json:"status"`
	CPUUsage     float64   `json:"cpu_usage,omitempty"`
	MemoryUsage  float64   `json:"memory_usage,omitempty"`
	DiskUsage    float64   `json:"disk_usage,omitempty"`
}

// Command 命令定义
type Command struct {
	ID         int64
	CommandID  string
	InstanceID string
	Command    string
	Args       map[string]string
	Status     string
	IssuedAt   time.Time
	IssuedBy   string
	ExpiresAt  *time.Time
	ExecutedAt *time.Time
	Result     *CommandResult
}

// CommandResult 命令执行结果
type CommandResult struct {
	Success bool
	Output  string
	Error   string
	ExecMs  int64
}

const (
	CommandStatusPending  = "pending"
	CommandStatusExecuted = "executed"
	CommandStatusFailed   = "failed"
	CommandStatusExpired  = "expired"
)
