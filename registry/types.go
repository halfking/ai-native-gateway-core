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
	Version    int             `json:"version"`      // Phase 3: 固定为 1
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}
