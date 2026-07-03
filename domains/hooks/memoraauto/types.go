// Package memoraauto 实现 Memora 自动沉淀 Hook。
//
// 功能：
//   - 检测会话空闲（最后活动 > 1小时 且 请求数 ≥ 3）
//   - 异步调用 kxmemory 接收 API 进行会话数据沉淀
//   - 支持重试机制（指数退避，最多3次）
//   - 在 PhasePostResponse 阶段执行（不阻塞响应）
//
// 依赖：
//   - Task A1: Hook 框架（domains/pipeline）
//   - Task A3: kxmemory 会话接收 API
package memoraauto

import (
	"time"
)

// SessionStats 会话统计信息
type SessionStats struct {
	SessionKey   string
	TaskID       string
	TenantID     string
	RequestCount int
	LastActive   time.Time
	CreatedAt    time.Time
}

// IsIdle 判断会话是否空闲
// 空闲条件：最后活动时间 > 1小时 且 请求数 >= 3
func (s *SessionStats) IsIdle() bool {
	if s.RequestCount < 3 {
		return false
	}
	idleDuration := time.Since(s.LastActive)
	return idleDuration > 1*time.Hour
}

// SessionIngestRequest kxmemory 会话接收请求
type SessionIngestRequest struct {
	SessionKey string                 `json:"session_key"`
	TaskID     string                 `json:"task_id"`
	TenantID   string                 `json:"tenant_id"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// SessionIngestResponse kxmemory 会话接收响应
type SessionIngestResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
	JobID   string `json:"job_id,omitempty"`
}

// Config Memora 自动沉淀配置
type Config struct {
	// Enabled 是否启用
	Enabled bool `yaml:"enabled"`

	// KxmemoryURL kxmemory API 地址
	KxmemoryURL string `yaml:"kxmemory_url"`

	// Timeout HTTP 请求超时时间
	Timeout time.Duration `yaml:"timeout"`

	// IdleThreshold 空闲阈值（默认 1 小时）
	IdleThreshold time.Duration `yaml:"idle_threshold"`

	// MinRequestCount 最小请求数（默认 3）
	MinRequestCount int `yaml:"min_request_count"`

	// MaxRetries 最大重试次数（默认 3）
	MaxRetries int `yaml:"max_retries"`

	// RetryBackoff 重试退避基础时间（默认 1 秒）
	RetryBackoff time.Duration `yaml:"retry_backoff"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		Enabled:         true,
		KxmemoryURL:     "http://localhost:8000/api/sessions/ingest",
		Timeout:         10 * time.Second,
		IdleThreshold:   1 * time.Hour,
		MinRequestCount: 3,
		MaxRetries:      3,
		RetryBackoff:    1 * time.Second,
	}
}
