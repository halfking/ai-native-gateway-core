// Package freediscovery 实现免费资源自动发现: 借鉴 Orbi pi-providers 模板能力,
// 通过供应商模板配置自动扫描上游 /models 端点, 发现免费模型并经人工审查后
// 批量导入 free_resource_catalog.
//
// 数据模型: sql/migrations/084-freediscovery-schema.sql
//   - provider_templates  供应商模板 (多租户, RLS)
//   - discovery_tasks     发现任务
//   - discovery_results   发现结果 (待审查导入)
package freediscovery

import (
	"time"
)

// APIType 供应商 API 协议类型
type APIType string

const (
	APITypeOpenAICompletions  APIType = "openai-completions"   // OpenAI 兼容 /v1/models
	APITypeGoogleGenerativeAI APIType = "google-generative-ai" // Google Generative Language API
	APITypeAnthropic          APIType = "anthropic"            // Anthropic Messages API
)

// FreeType 免费资源类型 (与 084 迁移 discovery_results.free_type CHECK 约束对齐;
// 值域同 domains/freeresource 的既有定义).
type FreeType string

const (
	FreeTypeRecurringDaily    FreeType = "recurring-daily"
	FreeTypeRecurringMonthly  FreeType = "recurring-monthly"
	FreeTypeOneTimeInitial    FreeType = "one-time-initial"
	FreeTypeRecurringCredit   FreeType = "recurring-credit"
	FreeTypeRecurringUncapped FreeType = "recurring-uncapped"
	FreeTypeKeyless           FreeType = "keyless"
	FreeTypeDiscontinued      FreeType = "discontinued"
)

// TaskStatus 发现任务状态
type TaskStatus string

const (
	TaskStatusPending TaskStatus = "pending"
	TaskStatusRunning TaskStatus = "running"
	TaskStatusSuccess TaskStatus = "success"
	TaskStatusFailed  TaskStatus = "failed"
)

// TriggerType 发现任务触发方式
type TriggerType string

const (
	TriggerManual    TriggerType = "manual"
	TriggerScheduled TriggerType = "scheduled"
	TriggerWebhook   TriggerType = "webhook"
)

// ImportStatus 发现结果导入状态
type ImportStatus string

const (
	ImportPending  ImportStatus = "pending"
	ImportImported ImportStatus = "imported"
	ImportSkipped  ImportStatus = "skipped"
	ImportConflict ImportStatus = "conflict"
)

// ConflictPolicy 批量导入时的冲突处理策略
type ConflictPolicy string

const (
	// ConflictSkip 已存在 (provider_code, model_id) 时跳过, 保留现有条目 (默认, 最安全)
	ConflictSkip ConflictPolicy = "skip"
	// ConflictOverwrite 已存在时用发现结果覆盖配额/ToS 字段
	ConflictOverwrite ConflictPolicy = "overwrite"
	// ConflictMerge 已存在时仅补充空字段, 不覆盖已有值
	ConflictMerge ConflictPolicy = "merge"
)

// ProviderTemplate 供应商模板 (Orbi pi-providers 模板的多租户化形态).
// 密钥字段 (APIKeyEncrypted) 标记 json:"-", 永不回显给客户端.
type ProviderTemplate struct {
	ID              int64     `json:"id"`
	TenantID        string    `json:"tenant_id"`
	ProviderCode    string    `json:"provider_code"`
	DisplayName     string    `json:"display_name"`
	BaseURL         string    `json:"base_url"`
	APIType         APIType   `json:"api_type"`
	APIKeyEnv       string    `json:"api_key_env"` // 环境变量引用, 如 "$GROQ_API_KEY"; 空表示 keyless
	APIKeyEncrypted []byte    `json:"-"`           // 加密密文, 绝不序列化
	ModelsEndpoint  string    `json:"models_endpoint"`
	QuotaEndpoint   string    `json:"quota_endpoint"`
	TosURL          string    `json:"tos_url"`
	TosVerdict      string    `json:"tos_verdict"`
	TosNotes        string    `json:"tos_notes"`
	Enabled         bool      `json:"enabled"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// HasCredential 模板是否携带可用的上游认证.
// keyless 提供商 (无 APIKeyEnv 也无密文) 返回 false.
func (t *ProviderTemplate) HasCredential() bool {
	return t != nil && (t.APIKeyEnv != "" || len(t.APIKeyEncrypted) > 0)
}

// CreateTemplateRequest 创建模板请求
type CreateTemplateRequest struct {
	ProviderCode string  `json:"provider_code"`
	DisplayName  string  `json:"display_name"`
	BaseURL      string  `json:"base_url"`
	APIType      APIType `json:"api_type"`
	APIKeyEnv    string  `json:"api_key_env"`
	// APIKey 明文仅用于写入 (服务端加密后落库), 永不回读/回显
	APIKey         string `json:"api_key,omitempty"`
	ModelsEndpoint string `json:"models_endpoint"`
	QuotaEndpoint  string `json:"quota_endpoint,omitempty"`
	TosURL         string `json:"tos_url,omitempty"`
	TosVerdict     string `json:"tos_verdict,omitempty"`
	TosNotes       string `json:"tos_notes,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
	CreatedBy      string `json:"created_by,omitempty"` // 操作人 (审计); 服务端可从会话注入
}

// UpdateTemplateRequest 更新模板请求 (指针字段 nil = 不修改)
type UpdateTemplateRequest struct {
	DisplayName    *string  `json:"display_name,omitempty"`
	BaseURL        *string  `json:"base_url,omitempty"`
	APIType        *APIType `json:"api_type,omitempty"`
	APIKeyEnv      *string  `json:"api_key_env,omitempty"`
	APIKey         *string  `json:"api_key,omitempty"`
	ModelsEndpoint *string  `json:"models_endpoint,omitempty"`
	QuotaEndpoint  *string  `json:"quota_endpoint,omitempty"`
	TosURL         *string  `json:"tos_url,omitempty"`
	TosVerdict     *string  `json:"tos_verdict,omitempty"`
	TosNotes       *string  `json:"tos_notes,omitempty"`
	Enabled        *bool    `json:"enabled,omitempty"`
}

// DiscoveryTask 发现任务记录
type DiscoveryTask struct {
	ID             int64       `json:"id"`
	TenantID       string      `json:"tenant_id"`
	TemplateID     *int64      `json:"template_id"` // 模板被删除后为 NULL
	ProviderCode   string      `json:"provider_code"`
	Status         TaskStatus  `json:"status"`
	TriggerType    TriggerType `json:"trigger_type"`
	TriggeredBy    string      `json:"triggered_by"`
	StartedAt      *time.Time  `json:"started_at"`
	CompletedAt    *time.Time  `json:"completed_at"`
	ErrorMessage   string      `json:"error_message"`
	ModelsFound    int         `json:"models_found"`
	ModelsImported int         `json:"models_imported"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// DiscoveryRequest 触发发现请求
type DiscoveryRequest struct {
	TemplateID  int64
	TenantID    string
	TriggeredBy string
	TriggerType TriggerType // 空 = manual
}

// DiscoveredModel 扫描发现的单个模型
type DiscoveredModel struct {
	ProviderCode  string
	ModelID       string
	DisplayName   string
	ContextWindow int
	MaxTokens     int
	FreeType      string // 空 = 无法推断免费类型
	MonthlyTokens int64
	DailyTokens   int64
	PoolKey       string
	TosVerdict    string
	TosNotes      string
	RawMetadata   map[string]any
}

// DiscoveryResult 发现结果持久化行
type DiscoveryResult struct {
	ID            int64        `json:"id"`
	TaskID        int64        `json:"task_id"`
	TenantID      string       `json:"tenant_id"`
	ProviderCode  string       `json:"provider_code"`
	ModelID       string       `json:"model_id"`
	DisplayName   string       `json:"display_name"`
	ContextWindow int          `json:"context_window"`
	MaxTokens     int          `json:"max_tokens"`
	FreeType      string       `json:"free_type"`
	MonthlyTokens int64        `json:"monthly_tokens"`
	DailyTokens   int64        `json:"daily_tokens"`
	PoolKey       string       `json:"pool_key"`
	TosVerdict    string       `json:"tos_verdict"`
	TosNotes      string       `json:"tos_notes"`
	ImportStatus  ImportStatus `json:"import_status"`
	ImportedAt    *time.Time   `json:"imported_at"`
	CreatedAt     time.Time    `json:"created_at"`
}

// ImportRequest 批量导入请求
type ImportRequest struct {
	TaskID         int64          `json:"task_id"`
	TenantID       string         `json:"-"`                    // 服务端从会话派生
	ResultIDs      []int64        `json:"result_ids,omitempty"` // 空 = 导入该任务全部 pending 结果
	ConflictPolicy ConflictPolicy `json:"conflict_policy,omitempty"`
	ImportedBy     string         `json:"-"` // 服务端从会话派生
}

// ImportSummary 批量导入结果统计
type ImportSummary struct {
	Imported   int `json:"imported"`
	Skipped    int `json:"skipped"`
	Conflicted int `json:"conflicted"`
	Failed     int `json:"failed"`
}

// OrbiProviderFile Orbi pi-providers JSON 模板文件结构 (templates/pi-providers/*.json)
type OrbiProviderFile struct {
	Providers map[string]OrbiProvider `json:"providers"`
}

// OrbiProvider Orbi 单个 provider 定义
type OrbiProvider struct {
	BaseURL string      `json:"baseUrl"`
	API     string      `json:"api"`
	APIKey  string      `json:"apiKey"` // 环境变量引用, 如 "$GROQ_API_KEY"
	Models  []OrbiModel `json:"models"`
}

// OrbiModel Orbi 模板中的模型条目
type OrbiModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
}

// ValidateCreate 校验创建请求的必填字段.
// 返回人类可读的错误信息; 空串 = 通过.
func (r *CreateTemplateRequest) ValidateCreate() string {
	if r.ProviderCode == "" {
		return "provider_code is required"
	}
	if !isValidProviderCode(r.ProviderCode) {
		return "provider_code only allows [a-z0-9-] (max 64 chars)"
	}
	if r.DisplayName == "" {
		return "display_name is required"
	}
	if r.BaseURL == "" {
		return "base_url is required"
	}
	if !isValidBaseURL(r.BaseURL) {
		return "base_url must start with http:// or https://"
	}
	if r.APIType == "" {
		r.APIType = APITypeOpenAICompletions
	}
	switch r.APIType {
	case APITypeOpenAICompletions, APITypeGoogleGenerativeAI, APITypeAnthropic:
	default:
		return "api_type must be one of: openai-completions, google-generative-ai, anthropic"
	}
	if r.TosVerdict != "" {
		if !isValidTosVerdict(r.TosVerdict) {
			return "tos_verdict must be one of: ok, caution, ambiguous, avoid, unknown"
		}
	}
	return ""
}

func isValidProviderCode(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func isValidBaseURL(s string) bool {
	return len(s) > 8 && (startsWith(s, "http://") || startsWith(s, "https://"))
}

func isValidTosVerdict(s string) bool {
	switch s {
	case "ok", "caution", "ambiguous", "avoid", "unknown":
		return true
	}
	return false
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
