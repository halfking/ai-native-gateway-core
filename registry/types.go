package registry

import (
	"encoding/json"
	"time"
)

// Tool 表示工具定义
type Tool struct {
	ID         int64           `json:"id"`
	ToolID     string          `json:"tool_id"`      // "filesystem.read_file"
	TenantID   string          `json:"tenant_id"`    // "default" or "tenant_xxx"
	Category   string          `json:"category"`     // "filesystem"
	ToolName   string          `json:"tool_name"`    // "read_file"
	Definition json.RawMessage `json:"definition"`   // 完整工具定义 JSON
	Enabled    bool            `json:"enabled"`
	Priority   int             `json:"priority"`
	Version    int             `json:"version"`

	// Phase 3.2: 版本管理
	DeprecationDate  *time.Time     `json:"deprecation_date,omitempty"`
	SupersededBy     string         `json:"superseded_by,omitempty"`
	MinClientVersion string         `json:"min_client_version,omitempty"`
	BreakingChanges  json.RawMessage `json:"breaking_changes,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsDeprecated 检查工具是否已废弃
func (t *Tool) IsDeprecated() bool {
	if t.DeprecationDate == nil {
		return false
	}
	return time.Now().After(*t.DeprecationDate)
}

// IsSuperseded 检查是否被新版本替代
func (t *Tool) IsSuperseded() bool {
	return t.SupersededBy != ""
}

// TenantPolicy 租户工具策略
type TenantPolicy struct {
	ID         int64     `json:"id"`
	TenantID   string    `json:"tenant_id"`
	ToolPattern string   `json:"tool_pattern"` // "filesystem.*" or "filesystem.read_file"
	PolicyType string    `json:"policy_type"`  // "allow" or "deny"
	Reason     string    `json:"reason,omitempty"`
	Enabled    bool      `json:"enabled"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	CreatedBy  string    `json:"created_by,omitempty"`
}

// UsageStats 工具使用统计
type UsageStats struct {
	ToolID       string    `json:"tool_id"`
	TenantID     string    `json:"tenant_id"`
	UsageDate    time.Time `json:"usage_date"`
	CallCount    int64     `json:"call_count"`
	SuccessCount int64     `json:"success_count"`
	ErrorCount   int64     `json:"error_count"`
	AvgLatencyMs int       `json:"avg_latency_ms"`
	LastCalledAt time.Time `json:"last_called_at,omitempty"`
}