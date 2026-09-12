// Package freediscovery implements automatic free-resource discovery: inspired by Orbi's
// pi-providers template capability, it uses provider template configuration to scan upstream
// /models endpoints automatically, discover free models, and batch-import them into
// free_resource_catalog after manual review.
//
// Data model: sql/migrations/084-freediscovery-schema.sql
//   - provider_templates  provider templates (multi-tenant, RLS)
//   - discovery_tasks     discovery tasks
//   - discovery_results   discovery results (pending review/import)
package freediscovery

import (
	"strings"
	"time"
)

// APIType is the provider API protocol type.
type APIType string

const (
	APITypeOpenAICompletions  APIType = "openai-completions"   // OpenAI-compatible /v1/models
	APITypeGoogleGenerativeAI APIType = "google-generative-ai" // Google Generative Language API
	APITypeAnthropic          APIType = "anthropic"            // Anthropic Messages API
)

// FreeType is the free resource type (aligned with the discovery_results.free_type CHECK
// constraint from migration 084; the value domain matches the existing definition
// in domains/freeresource).
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

// TaskStatus is the discovery task status.
type TaskStatus string

const (
	TaskStatusPending TaskStatus = "pending"
	TaskStatusRunning TaskStatus = "running"
	TaskStatusSuccess TaskStatus = "success"
	TaskStatusFailed  TaskStatus = "failed"
)

// TriggerType is how the discovery task was triggered.
type TriggerType string

const (
	TriggerManual    TriggerType = "manual"
	TriggerScheduled TriggerType = "scheduled"
	TriggerWebhook   TriggerType = "webhook"
)

// ImportStatus is the import status of a discovery result.
type ImportStatus string

const (
	ImportPending  ImportStatus = "pending"
	ImportImported ImportStatus = "imported"
	ImportSkipped  ImportStatus = "skipped"
	ImportConflict ImportStatus = "conflict"
)

// ConflictPolicy is the conflict-handling policy for batch imports.
type ConflictPolicy string

const (
	// ConflictSkip keeps the existing row when (provider_code, model_id) already exists
	// (default, safest).
	ConflictSkip ConflictPolicy = "skip"
	// ConflictOverwrite overwrites the quota/ToS fields with the discovery result on conflict.
	ConflictOverwrite ConflictPolicy = "overwrite"
	// ConflictMerge fills only empty fields on conflict and never overwrites existing values.
	ConflictMerge ConflictPolicy = "merge"
)

// ProviderTemplate is the provider template (multi-tenant shape of the Orbi pi-providers template).
// The credential field (APIKeyEncrypted) is tagged json:"-" and never echoed back to the client.
type ProviderTemplate struct {
	ID              int64     `json:"id"`
	TenantID        string    `json:"tenant_id"`
	ProviderCode    string    `json:"provider_code"`
	DisplayName     string    `json:"display_name"`
	BaseURL         string    `json:"base_url"`
	APIType         APIType   `json:"api_type"`
	APIKeyEnv       string    `json:"api_key_env"` // Env var reference, e.g. "$GROQ_API_KEY"; empty means keyless.
	APIKeyEncrypted []byte    `json:"-"`           // Encrypted ciphertext; never serialized.
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

// HasCredential reports whether the template carries usable upstream auth.
// Keyless providers (no APIKeyEnv and no ciphertext) return false.
func (t *ProviderTemplate) HasCredential() bool {
	return t != nil && (t.APIKeyEnv != "" || len(t.APIKeyEncrypted) > 0)
}

// CreateTemplateRequest is the create-template request payload.
type CreateTemplateRequest struct {
	ProviderCode string  `json:"provider_code"`
	DisplayName  string  `json:"display_name"`
	BaseURL      string  `json:"base_url"`
	APIType      APIType `json:"api_type"`
	APIKeyEnv    string  `json:"api_key_env"`
	// APIKey plaintext is only used for writes (server encrypts before storing) and is never read back or echoed.
	APIKey         string `json:"api_key,omitempty"`
	ModelsEndpoint string `json:"models_endpoint"`
	QuotaEndpoint  string `json:"quota_endpoint,omitempty"`
	TosURL         string `json:"tos_url,omitempty"`
	TosVerdict     string `json:"tos_verdict,omitempty"`
	TosNotes       string `json:"tos_notes,omitempty"`
	Enabled        *bool  `json:"enabled,omitempty"`
	CreatedBy      string `json:"created_by,omitempty"` // Operator (audit); server may inject this from the session.
}

// UpdateTemplateRequest is the update-template request payload (nil pointer fields = no change).
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

// DiscoveryTask is a discovery task record.
type DiscoveryTask struct {
	ID             int64       `json:"id"`
	TenantID       string      `json:"tenant_id"`
	TemplateID     *int64      `json:"template_id"` // NULL after the template is deleted.
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

// DiscoveryRequest triggers a discovery run.
type DiscoveryRequest struct {
	TemplateID  int64
	TenantID    string
	TriggeredBy string
	TriggerType TriggerType // Empty = manual.
}

// DiscoveredModel is a single model found by a scan.
type DiscoveredModel struct {
	ProviderCode  string
	ModelID       string
	DisplayName   string
	ContextWindow int
	MaxTokens     int
	FreeType      string // Empty = free type could not be inferred.
	MonthlyTokens int64
	DailyTokens   int64
	PoolKey       string
	TosVerdict    string
	TosNotes      string
	RawMetadata   map[string]any
}

// DiscoveryResult is a persisted discovery result row.
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

// ImportRequest is the batch-import request payload.
type ImportRequest struct {
	TaskID         int64          `json:"task_id"`
	TenantID       string         `json:"-"`                    // Derived from the session on the server.
	ResultIDs      []int64        `json:"result_ids,omitempty"` // Empty = import all pending results for the task.
	ConflictPolicy ConflictPolicy `json:"conflict_policy,omitempty"`
	ImportedBy     string         `json:"-"` // Derived from the session on the server.
}

// ImportSummary is the batch-import result counters.
type ImportSummary struct {
	Imported   int `json:"imported"`
	Skipped    int `json:"skipped"`
	Conflicted int `json:"conflicted"`
	Failed     int `json:"failed"`
}

// OrbiProviderFile is the structure of an Orbi pi-providers JSON template file
// (templates/pi-providers/*.json).
type OrbiProviderFile struct {
	Providers map[string]OrbiProvider `json:"providers"`
}

// OrbiProvider is a single provider definition in Orbi.
type OrbiProvider struct {
	BaseURL string      `json:"baseUrl"`
	API     string      `json:"api"`
	APIKey  string      `json:"apiKey"` // Env var reference, e.g. "$GROQ_API_KEY".
	Models  []OrbiModel `json:"models"`
}

// OrbiModel is a model entry in an Orbi template.
type OrbiModel struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextWindow int    `json:"contextWindow"`
	MaxTokens     int    `json:"maxTokens"`
}

// ValidateCreate validates the required fields of a create request.
// Returns a human-readable error message; empty string means pass.
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
	if msg := isValidBaseURL(r.BaseURL); msg != "" {
		return msg
	}
	if msg := isValidModelsEndpoint(r.ModelsEndpoint); msg != "" {
		return msg
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

func isValidTosVerdict(s string) bool {
	switch s {
	case "ok", "caution", "ambiguous", "avoid", "unknown":
		return true
	}
	return false
}

// TrimmedDisplayName computes a display name that is safe for the frontend: truncate,
// trim leading/trailing whitespace, and cap the length.
// Reuses rune counting so UTF-8 byte slicing does not produce mojibake.
func TrimmedDisplayName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	const maxRunes = 200
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}
