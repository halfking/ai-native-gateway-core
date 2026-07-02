// Package approval provides approval workflow management for sensitive LLM requests.
//
// This package implements a human-in-the-loop approval mechanism that can be triggered
// by sensitive content detection, high cost estimation, tool calls, or policy rules.
package approval

import (
	"time"
)

// ApprovalRequest represents a request that requires human approval before proceeding.
type ApprovalRequest struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	TenantID  string `json:"tenant_id"`

	// Trigger information
	TriggerType   ApprovalTriggerType `json:"trigger_type"`
	TriggerReason string              `json:"trigger_reason"`
	RiskLevel     RiskLevel           `json:"risk_level"`

	// Session context
	SessionSummary SessionSummary          `json:"session_summary"`
	SensitiveInfo  []SensitiveItemSummary `json:"sensitive_info,omitempty"`

	// Request content
	UserMessage string `json:"user_message"`
	FullContext []byte `json:"-"` // Stored as JSONB, not exposed directly

	// Cost estimation
	EstimatedCost   float64 `json:"estimated_cost"`
	EstimatedTokens int     `json:"estimated_tokens"`

	// Status
	Status    ApprovalStatus `json:"status"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`

	// Result
	ApprovedBy      string    `json:"approved_by,omitempty"`
	ApprovedAt      time.Time `json:"approved_at,omitempty"`
	ApprovalNote    string    `json:"approval_note,omitempty"`
	Rejected        bool      `json:"rejected"`
	RejectionReason string    `json:"rejection_reason,omitempty"`

	// Metadata for extensibility
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// ApprovalTriggerType defines what triggered the approval requirement.
type ApprovalTriggerType string

const (
	TriggerSensitiveContent ApprovalTriggerType = "sensitive_content"
	TriggerHighCost         ApprovalTriggerType = "high_cost"
	TriggerToolCall         ApprovalTriggerType = "tool_call"
	TriggerPolicyMatch      ApprovalTriggerType = "policy_match"
	TriggerManualMode       ApprovalTriggerType = "manual_mode"
)

// RiskLevel indicates the severity of the detected risk.
type RiskLevel string

const (
	RiskLow      RiskLevel = "LOW"
	RiskMedium   RiskLevel = "MEDIUM"
	RiskHigh     RiskLevel = "HIGH"
	RiskCritical RiskLevel = "CRITICAL"
)

// ApprovalStatus tracks the current state of an approval request.
type ApprovalStatus string

const (
	StatusPending  ApprovalStatus = "pending"
	StatusApproved ApprovalStatus = "approved"
	StatusRejected ApprovalStatus = "rejected"
	StatusTimeout  ApprovalStatus = "timeout"
	StatusCanceled ApprovalStatus = "canceled"
)

// SensitiveItemSummary represents a redacted summary of sensitive information for approval display.
// This is a simplified version for storage and API responses.
// The full SensitiveItem with position info is defined in sensitive_detector.go.
type SensitiveItemSummary struct {
	Type       string  `json:"type"`       // PII/SECRET/FINANCIAL/MEDICAL
	Content    string  `json:"content"`    // Redacted content
	Location   string  `json:"location"`   // e.g., "message[0].content"
	Confidence float64 `json:"confidence"` // Detection confidence 0.0-1.0
}

// SessionSummary provides a high-level overview of the session context.
type SessionSummary struct {
	MessageCount   int      `json:"message_count"`
	TotalTokens    int      `json:"total_tokens"`
	Duration       string   `json:"duration"`
	Topics         []string `json:"topics,omitempty"`
	UserIntent     string   `json:"user_intent,omitempty"`
	LastMessages   []string `json:"last_messages,omitempty"`   // Last 3 messages (redacted)
	RiskAssessment string   `json:"risk_assessment,omitempty"` // LOW/MEDIUM/HIGH from summarizer
}

// ApprovalConfig defines approval settings for a tenant.
type ApprovalConfig struct {
	TenantID            string                `json:"tenant_id"`
	Enabled             bool                  `json:"enabled"`
	Mode                ApprovalMode          `json:"mode"`
	Approvers           []Approver            `json:"approvers"`
	Channels            []NotificationChannel `json:"channels"`
	TimeoutSeconds      int                   `json:"timeout_seconds"`
	AutoRejectOnTimeout bool                  `json:"auto_reject_on_timeout"`
	Rules               []ApprovalRule        `json:"rules"`
	CreatedAt           time.Time             `json:"created_at"`
	UpdatedAt           time.Time             `json:"updated_at"`
}

// ApprovalMode defines how approval is triggered.
type ApprovalMode string

const (
	ModeDisabled  ApprovalMode = "disabled"  // No approval required
	ModeAutomatic ApprovalMode = "automatic" // Rule-based triggering
	ModeManual    ApprovalMode = "manual"    // All requests require approval
)

// Approver represents a person who can approve requests.
type Approver struct {
	UserID   string `json:"user_id"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"`
	Phone    string `json:"phone,omitempty"`
	Role     string `json:"role"`     // admin/auditor/manager
	Priority int    `json:"priority"` // Lower number = higher priority
	Enabled  bool   `json:"enabled"`
}

// NotificationChannel defines how approvers are notified.
type NotificationChannel struct {
	Type    ChannelType       `json:"type"`
	Config  map[string]string `json:"config"`
	Enabled bool              `json:"enabled"`
}

// ChannelType defines supported notification channels.
type ChannelType string

const (
	ChannelFeishu   ChannelType = "feishu"
	ChannelWeChat   ChannelType = "wechat"
	ChannelDingTalk ChannelType = "dingtalk"
	ChannelEmail    ChannelType = "email"
	ChannelWebhook  ChannelType = "webhook"
)

// ApprovalRule defines conditions that trigger approval.
type ApprovalRule struct {
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Priority   int             `json:"priority"` // Higher number = higher priority
	Conditions []RuleCondition `json:"conditions"`
	Action     RuleAction      `json:"action"`
}

// RuleCondition defines a single condition in a rule.
type RuleCondition struct {
	Field    string `json:"field"`    // message_content/token_count/cost/tool_name
	Operator string `json:"operator"` // contains/gt/lt/eq/regex
	Value    string `json:"value"`
}

// RuleAction defines what happens when a rule matches.
type RuleAction struct {
	Type      string    `json:"type"`       // require_approval/auto_approve/auto_reject
	RiskLevel RiskLevel `json:"risk_level"` // Risk level to assign
	Reason    string    `json:"reason"`     // Human-readable reason
}

// ApprovalFilter defines criteria for listing approval requests.
type ApprovalFilter struct {
	TenantID    string
	Status      ApprovalStatus
	RiskLevel   RiskLevel
	TriggerType ApprovalTriggerType
	FromDate    time.Time
	ToDate      time.Time
	Limit       int
	Offset      int
}

// ApprovalDecision represents the outcome of approval detection.
type ApprovalDecision struct {
	RequiresApproval bool
	TriggerType      ApprovalTriggerType
	TriggerReason    string
	RiskLevel        RiskLevel
	MatchedRule      *ApprovalRule
	SensitiveItems   []SensitiveItemSummary
	EstimatedCost    float64
	EstimatedTokens  int
}

// ApprovalResult represents the final outcome after approval completes.
type ApprovalResult struct {
	RequestID  string
	Approved   bool
	Reason     string
	ApprovedBy string
	ApprovedAt time.Time
}
